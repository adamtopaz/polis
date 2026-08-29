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
		{"agent", "create", "--id", "managed", "--charter", "Act.", "--runtime", `["runtime"]`},
		{"agent", "list"},
		{"agent", "get", "managed"},
		{"message", "managed", `{"goal":"work"}`},
		{"events", "managed"},
		{"agent", "state", "managed", "paused"},
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
