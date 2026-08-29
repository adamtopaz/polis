package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/adamtopaz/polis/internal/client"
	"github.com/adamtopaz/polis/internal/model"
	"github.com/adamtopaz/polis/internal/store"
)

func TestHTTPAgentLifecycle(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "polis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := httptest.NewServer(New(database, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer server.Close()

	ctx := context.Background()
	api := client.New(server.URL)
	agent, err := api.CreateAgent(ctx, "http-agent", "Act autonomously.", []string{"runtime"})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Phase != "ready" {
		t.Fatalf("created agent phase = %q", agent.Phase)
	}
	lease, ok, err := api.Acquire(ctx, "test-worker", 30*time.Second, 0)
	if err != nil || !ok {
		t.Fatalf("acquire = %#v, %v, %v", lease, ok, err)
	}
	self, err := api.Self(ctx, lease.Token)
	if err != nil || self.ID != "http-agent" {
		t.Fatalf("self = %#v, %v", self, err)
	}
	if _, err := api.Journal(ctx, lease.Token, "test.observed", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Sleep(ctx, lease.Token, time.Minute); err != nil {
		t.Fatal(err)
	}
	agent, err = api.GetAgent(ctx, "http-agent")
	if err != nil || agent.Phase != "sleeping" {
		t.Fatalf("sleeping agent = %#v, %v", agent, err)
	}
	if _, err := api.SendControlMessage(ctx, "http-agent", "operator", json.RawMessage(`{"wake":true}`)); err != nil {
		t.Fatal(err)
	}
	agent, err = api.GetAgent(ctx, "http-agent")
	if err != nil || agent.Phase != "ready" {
		t.Fatalf("woken agent = %#v, %v", agent, err)
	}
	agent, err = api.SetState(ctx, "http-agent", model.StateTerminated)
	if err != nil || agent.State != model.StateTerminated {
		t.Fatalf("terminated agent = %#v, %v", agent, err)
	}
}
