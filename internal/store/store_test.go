package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/adamtopaz/polis/internal/model"
)

func TestAgentLifecycle(t *testing.T) {
	st := openTestStore(t)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }

	agent, err := st.ApplyAgent("alpha", "Choose and pursue useful work.", []string{"agent-runtime"}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	assertPhase(t, agent, "ready")

	lease, err := st.Acquire("alpha", "worker-1", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertPhase(t, lease.Agent, "running")

	now = now.Add(10 * time.Second)
	heartbeat, err := st.Heartbeat(lease.Token, 30*time.Second)
	if err != nil || !heartbeat.Continue {
		t.Fatalf("heartbeat = %#v, %v", heartbeat, err)
	}

	scheduled, err := st.ScheduleMessage(lease.Token, now.Add(20*time.Second), json.RawMessage(`{"reason":"resume later"}`))
	if err != nil || scheduled.DeliverAt != now.Add(20*time.Second) {
		t.Fatalf("scheduled message = %#v, %v", scheduled, err)
	}
	if _, err := st.SendMessage("alpha", "operator", json.RawMessage(`{"work":"now"}`)); err != nil {
		t.Fatal(err)
	}
	messages, err := st.Messages(lease.Token, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Sender != "operator" {
		t.Fatalf("messages = %#v", messages)
	}
	if err := st.AckMessages(lease.Token, messages[0].ID); err != nil {
		t.Fatal(err)
	}
	messages, err = st.Messages(lease.Token, 100)
	if err != nil || len(messages) != 0 {
		t.Fatalf("acked messages = %#v, %v", messages, err)
	}
	now = now.Add(20 * time.Second)
	messages, err = st.Messages(lease.Token, 100)
	if err != nil || len(messages) != 1 || messages[0].Sender != "agent:alpha" {
		t.Fatalf("scheduled messages = %#v, %v", messages, err)
	}
	if err := st.AckMessages(lease.Token, messages[0].ID); err != nil {
		t.Fatal(err)
	}

	agent, err = st.SetState("alpha", model.StatePaused, "operator")
	if err != nil {
		t.Fatal(err)
	}
	assertPhase(t, agent, "paused")
	if _, err := st.Heartbeat(lease.Token, 30*time.Second); !errors.Is(err, ErrInvalidLease) {
		t.Fatalf("paused incarnation remains authorized: %v", err)
	}

	agent, err = st.SetState("alpha", model.StateActive, "operator")
	if err != nil {
		t.Fatal(err)
	}
	assertPhase(t, agent, "ready")
	agent, err = st.SetState("alpha", model.StateTerminated, "operator")
	if err != nil {
		t.Fatal(err)
	}
	assertPhase(t, agent, "terminated")
	if _, err := st.SendMessage("alpha", "operator", json.RawMessage(`null`)); err == nil {
		t.Fatal("terminated agent accepted a message")
	}
	if _, err := st.SetState("alpha", model.StateActive, "operator"); err == nil {
		t.Fatal("terminated agent was restarted")
	}
}

func TestApplyAgentIsIdempotentAndUpdatesConfiguration(t *testing.T) {
	st := openTestStore(t)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }

	created, err := st.ApplyAgent("alpha", "First charter.", []string{"runtime", "first"}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	assertPhase(t, created, "ready")

	now = now.Add(time.Minute)
	unchanged, err := st.ApplyAgent("alpha", "First charter.", []string{"runtime", "first"}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if !unchanged.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("idempotent apply changed updated_at: %s != %s", unchanged.UpdatedAt, created.UpdatedAt)
	}

	if _, err := st.SetState("alpha", model.StatePaused, "operator"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	updated, err := st.ApplyAgent("alpha", "Second charter.", []string{"runtime", "second"}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Charter != "Second charter." || !slices.Equal(updated.Runtime, []string{"runtime", "second"}) {
		t.Fatalf("updated agent = %#v", updated)
	}
	if updated.State != model.StatePaused {
		t.Fatalf("configuration apply changed desired state: %#v", updated)
	}

	events, err := st.Events("alpha", 100)
	if err != nil {
		t.Fatal(err)
	}
	configurationChanges := 0
	for _, event := range events {
		if event.Kind == "agent.configuration_changed" {
			configurationChanges++
		}
	}
	if configurationChanges != 1 {
		t.Fatalf("configuration events = %#v", events)
	}
}

func TestAcquireIsPinnedToAgent(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.ApplyAgent("alpha", "Alpha.", []string{"runtime"}, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyAgent("beta", "Beta.", []string{"runtime"}, "operator"); err != nil {
		t.Fatal(err)
	}

	lease, err := st.Acquire("beta", "beta-pod", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Agent.ID != "beta" {
		t.Fatalf("pinned worker acquired %#v", lease.Agent)
	}
	alpha, err := st.GetAgent("alpha")
	if err != nil {
		t.Fatal(err)
	}
	assertPhase(t, alpha, "ready")
	if _, err := st.Acquire("missing", "missing-pod", 30*time.Second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing pinned agent returned %v", err)
	}
}

func TestExpiredLeaseIsFencedAndReacquired(t *testing.T) {
	st := openTestStore(t)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	if _, err := st.ApplyAgent("alpha", "Persist.", []string{"runtime"}, "operator"); err != nil {
		t.Fatal(err)
	}
	message, err := st.SendMessage("alpha", "operator", json.RawMessage(`{"work":"survive"}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.Acquire("alpha", "worker-1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	firstDelivery, err := st.Messages(first.Token, 100)
	if err != nil || len(firstDelivery) != 1 || firstDelivery[0].ID != message.ID {
		t.Fatalf("first delivery = %#v, %v", firstDelivery, err)
	}
	now = now.Add(6 * time.Second)
	second, err := st.Acquire("alpha", "worker-2", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token {
		t.Fatal("new incarnation reused a fence token")
	}
	if _, err := st.Heartbeat(first.Token, 5*time.Second); !errors.Is(err, ErrInvalidLease) {
		t.Fatalf("expired token was accepted: %v", err)
	}
	if heartbeat, err := st.Heartbeat(second.Token, 5*time.Second); err != nil || !heartbeat.Continue {
		t.Fatalf("new token rejected: %#v, %v", heartbeat, err)
	}
	secondDelivery, err := st.Messages(second.Token, 100)
	if err != nil || len(secondDelivery) != 1 || secondDelivery[0].ID != message.ID {
		t.Fatalf("recovered delivery = %#v, %v", secondDelivery, err)
	}
	if err := st.AckMessages(second.Token, message.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReportedExitPreservesUnreadMessages(t *testing.T) {
	st := openTestStore(t)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	if _, err := st.ApplyAgent("alpha", "Persist.", []string{"runtime"}, "operator"); err != nil {
		t.Fatal(err)
	}
	message, err := st.SendMessage("alpha", "operator", json.RawMessage(`{"work":"retry"}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.Acquire("alpha", "worker-1", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := st.Messages(first.Token, 100)
	if err != nil || len(delivery) != 1 || delivery[0].ID != message.ID {
		t.Fatalf("first delivery = %#v, %v", delivery, err)
	}
	if err := st.Exit(first.Token, "worker restarting"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Self(first.Token); !errors.Is(err, ErrInvalidLease) {
		t.Fatalf("exited token remains authorized: %v", err)
	}
	agent, err := st.GetAgent("alpha")
	if err != nil {
		t.Fatal(err)
	}
	assertPhase(t, agent, "backoff")

	now = now.Add(5 * time.Second)
	second, err := st.Acquire("alpha", "worker-2", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := st.Messages(second.Token, 100)
	if err != nil || len(recovered) != 1 || recovered[0].ID != message.ID {
		t.Fatalf("recovered delivery = %#v, %v", recovered, err)
	}
	if err := st.AckMessages(second.Token, message.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReopenPreservesLeaseMailboxScheduleAndJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polis.db")
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.now = func() time.Time { return now }
	if _, err := st.ApplyAgent("alpha", "Persist.", []string{"runtime"}, "operator"); err != nil {
		t.Fatal(err)
	}
	lease, err := st.Acquire("alpha", "worker-1", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	immediate, err := st.SendMessage("alpha", "operator", json.RawMessage(`{"work":"now"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ScheduleMessage(lease.Token, now.Add(10*time.Second), json.RawMessage(`{"work":"later"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Journal(lease.Token, "before.restart", json.RawMessage(`{"durable":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return now }
	self, err := reopened.Self(lease.Token)
	if err != nil || self.ID != "alpha" {
		t.Fatalf("reopened lease = %#v, %v", self, err)
	}
	if heartbeat, err := reopened.Heartbeat(lease.Token, 30*time.Second); err != nil || !heartbeat.Continue {
		t.Fatalf("reopened heartbeat = %#v, %v", heartbeat, err)
	}
	messages, err := reopened.Messages(lease.Token, 100)
	if err != nil || len(messages) != 1 || messages[0].ID != immediate.ID {
		t.Fatalf("reopened mailbox = %#v, %v", messages, err)
	}
	events, err := reopened.Events("alpha", 100)
	if err != nil {
		t.Fatal(err)
	}
	foundJournal := false
	for _, event := range events {
		if event.Kind == "before.restart" {
			foundJournal = true
		}
	}
	if !foundJournal {
		t.Fatalf("reopened journal = %#v", events)
	}

	now = now.Add(10 * time.Second)
	messages, err = reopened.Messages(lease.Token, 100)
	if err != nil || len(messages) != 2 || messages[0].ID != immediate.ID || messages[1].Sender != "agent:alpha" {
		t.Fatalf("reopened scheduled delivery = %#v, %v", messages, err)
	}
}

func TestAgentCanJournalAndMessage(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.ApplyAgent("sender", "Coordinate freely.", []string{"runtime"}, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyAgent("target", "Receive messages.", []string{"runtime"}, "operator"); err != nil {
		t.Fatal(err)
	}
	lease, err := st.Acquire("sender", "worker", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	event, err := st.Journal(lease.Token, "decision.made", json.RawMessage(`{"decision":"message"}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.ID == 0 {
		t.Fatal("journal event did not receive an id")
	}
	if _, err := st.SendMessageAs(lease.Token, "target", json.RawMessage(`{"hello":"target"}`)); err != nil {
		t.Fatal(err)
	}
	events, err := st.Events("sender", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("sender events = %#v", events)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "polis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return st
}

func assertPhase(t *testing.T, agent model.Agent, want string) {
	t.Helper()
	if agent.Phase != want {
		t.Fatalf("phase = %q, want %q (%#v)", agent.Phase, want, agent)
	}
}
