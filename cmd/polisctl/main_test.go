package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	lease, err := database.Acquire("declared", "worker", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exit(lease.Token, "registered"); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
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
	} {
		err := run(context.Background(), command)
		if err == nil || !strings.Contains(err.Error(), "operator control CLI") {
			t.Fatalf("non-operator command %v returned %v", command, err)
		}
	}
}

func TestOperatorCLIRejectsImperativeAgentCreation(t *testing.T) {
	for _, command := range [][]string{
		{"agent", "create", "--charter", "No longer supported.", "--runtime", `["runtime"]`},
		{"agent", "apply", "--id", "also-not-supported", "--charter", "No longer supported.", "--runtime", `["runtime"]`},
	} {
		err := run(context.Background(), command)
		if err == nil || !strings.Contains(err.Error(), "agent requires list") {
			t.Fatalf("imperative agent configuration %v returned %v", command, err)
		}
	}
}
