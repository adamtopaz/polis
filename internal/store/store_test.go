package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/adamtopaz/polis/internal/model"
)

func TestAgentLifecycle(t *testing.T) {
	st := openTestStore(t)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }

	agent, err := st.CreateAgent("alpha", "Choose and pursue useful work.", []string{"agent-runtime"}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	assertPhase(t, agent, "ready")

	lease, err := st.Acquire("worker-1", 30*time.Second)
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

func TestExpiredLeaseIsFencedAndReacquired(t *testing.T) {
	st := openTestStore(t)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	if _, err := st.CreateAgent("alpha", "Persist.", []string{"runtime"}, "operator"); err != nil {
		t.Fatal(err)
	}
	first, err := st.Acquire("worker-1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Second)
	second, err := st.Acquire("worker-2", 5*time.Second)
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
}

func TestAgentCanJournalMessageAndSpawn(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.CreateAgent("parent", "Delegate freely.", []string{"runtime"}, "operator"); err != nil {
		t.Fatal(err)
	}
	lease, err := st.Acquire("worker", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	event, err := st.Journal(lease.Token, "decision.made", json.RawMessage(`{"decision":"spawn"}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.ID == 0 {
		t.Fatal("journal event did not receive an id")
	}
	parentID, err := st.AgentIDForToken(lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAgent("child", "Explore independently.", []string{"runtime"}, "agent:"+parentID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SendMessageAs(lease.Token, "child", json.RawMessage(`{"hello":"child"}`)); err != nil {
		t.Fatal(err)
	}
	events, err := st.Events("parent", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("parent events = %#v", events)
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
