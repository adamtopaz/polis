package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/adamtopaz/polis/internal/api"
	"github.com/adamtopaz/polis/internal/model"
	"github.com/adamtopaz/polis/internal/store"
)

func TestAgentCommands(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "polis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := httptest.NewServer(api.New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "operator-secret", "worker-secret").Handler())
	defer server.Close()

	if _, err := database.CreateAgent("parent", "Act independently.", []string{"runtime"}, "operator"); err != nil {
		t.Fatal(err)
	}
	lease, err := database.Acquire("worker", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAgent("y-target", "Receive messages.", []string{"runtime"}, "operator"); err != nil {
		t.Fatal(err)
	}
	message, err := database.SendMessage("parent", "operator", json.RawMessage(`{"hello":"parent"}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POLIS_URL", server.URL)
	t.Setenv("POLIS_AGENT_TOKEN", lease.Token)
	t.Setenv("POLIS_OPERATOR_TOKEN", "")

	ctx := context.Background()
	commands := [][]string{
		{"inspect"},
		{"messages"},
		{"ack", strconv.FormatUint(message.ID, 10)},
		{"journal", "cli.tested", `{"ok":true}`},
		{"spawn", "--id", "z-child", "--charter", "Explore.", "--runtime", `["runtime"]`},
		{"send", "y-target", `{"hello":"target"}`},
		{"schedule", "1h", `{"reason":"continue"}`},
	}
	for _, command := range commands {
		if err := run(ctx, command); err != nil {
			t.Fatalf("polis %v: %v", command, err)
		}
	}

	targetLease, err := database.Acquire("target-worker", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	targetMessages, err := database.Messages(targetLease.Token, 100)
	if err != nil || len(targetMessages) != 1 || targetMessages[0].Sender != "agent:parent" {
		t.Fatalf("target messages = %#v, %v", targetMessages, err)
	}
	if _, err := database.SetState("y-target", model.StatePaused, "operator"); err != nil {
		t.Fatal(err)
	}

	parent, err := database.GetAgent("parent")
	if err != nil || parent.Phase != "running" {
		t.Fatalf("running parent = %#v, %v", parent, err)
	}

	child, err := database.SetState("z-child", model.StateTerminated, "operator")
	if err != nil || child.State != model.StateTerminated {
		t.Fatalf("terminated child = %#v, %v", child, err)
	}

	events, err := database.Events("parent", 100)
	if err != nil {
		t.Fatal(err)
	}
	foundJournal := false
	for _, event := range events {
		if event.Kind == "cli.tested" {
			foundJournal = true
		}
	}
	if !foundJournal {
		t.Fatalf("self journal event missing: %#v", events)
	}
}

func TestAgentCLIRequiresAgentToken(t *testing.T) {
	t.Setenv("POLIS_AGENT_TOKEN", "")
	if err := run(context.Background(), []string{"inspect"}); err == nil {
		t.Fatal("agent command accepted a missing agent token")
	}
}

func TestAgentCLIRejectsNonAgentCommands(t *testing.T) {
	t.Setenv("POLIS_AGENT_TOKEN", "agent-token")
	for _, command := range [][]string{
		{"agent", "list"},
		{"message", "alpha", `{}`},
		{"events"},
		{"server"},
		{"worker"},
		{"demo-agent"},
		{"self", "inspect"},
	} {
		err := run(context.Background(), command)
		if err == nil || !strings.Contains(err.Error(), "capability CLI") {
			t.Fatalf("non-agent command %v returned %v", command, err)
		}
	}
}
