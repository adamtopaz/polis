package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adamtopaz/polis/internal/api"
	"github.com/adamtopaz/polis/internal/store"
)

func TestOperatorCommands(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "polis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := httptest.NewServer(api.New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "operator-secret", "worker-secret").Handler())
	defer server.Close()

	t.Setenv("POLIS_URL", server.URL)
	t.Setenv("POLIS_OPERATOR_TOKEN", "operator-secret")
	t.Setenv("POLIS_OPERATOR_TOKEN_FILE", "")
	commands := [][]string{
		{"agent", "apply", "--id", "declared", "--charter", "Persist.", "--runtime", `["runtime"]`},
		{"agent", "apply", "--id", "declared", "--charter", "Persist.", "--runtime", `["runtime"]`},
		{"agent", "list"},
		{"agent", "get", "declared"},
		{"message", "declared", `{"goal":"work"}`},
		{"events", "declared"},
		{"agent", "state", "declared", "paused"},
	}
	for _, command := range commands {
		if err := run(context.Background(), command); err != nil {
			t.Fatalf("polisctl %v: %v", command, err)
		}
	}
}

func TestOperatorCLIRejectsAgentAndInfrastructureCommands(t *testing.T) {
	for _, command := range [][]string{
		{"inspect"},
		{"send", "alpha", `{}`},
		{"spawn"},
		{"server"},
		{"worker"},
		{"demo-agent"},
	} {
		err := run(context.Background(), command)
		if err == nil || !strings.Contains(err.Error(), "operator control CLI") {
			t.Fatalf("non-operator command %v returned %v", command, err)
		}
	}
}

func TestOperatorCLIRejectsImperativeAgentCreation(t *testing.T) {
	err := run(context.Background(), []string{"agent", "create", "--charter", "No longer supported.", "--runtime", `["runtime"]`})
	if err == nil || !strings.Contains(err.Error(), "agent requires apply") {
		t.Fatalf("imperative agent creation returned %v", err)
	}
}

func TestAgentApplyReadsDeclarativeEnvironment(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "polis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := httptest.NewServer(api.New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "operator-secret", "worker-secret").Handler())
	defer server.Close()

	t.Setenv("POLIS_URL", server.URL)
	t.Setenv("POLIS_OPERATOR_TOKEN", "operator-secret")
	t.Setenv("POLIS_OPERATOR_TOKEN_FILE", "")
	t.Setenv("POLIS_AGENT_ID", "declared-from-environment")
	t.Setenv("POLIS_AGENT_CHARTER", "Persist declaratively.")
	t.Setenv("POLIS_AGENT_RUNTIME", `["runtime","--flag"]`)

	if err := run(context.Background(), []string{"agent", "apply"}); err != nil {
		t.Fatal(err)
	}
	agent, err := database.GetAgent("declared-from-environment")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Charter != "Persist declaratively." || len(agent.Runtime) != 2 {
		t.Fatalf("declarative agent = %#v", agent)
	}
}
