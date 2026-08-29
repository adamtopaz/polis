package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
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
	server := httptest.NewServer(New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "operator-secret").Handler())
	defer server.Close()

	ctx := context.Background()
	api := client.NewOperator(server.URL, "operator-secret")
	if _, err := client.New(server.URL).ListAgents(ctx); err == nil {
		t.Fatal("operator route accepted no token")
	} else {
		var apiError *client.Error
		if !errors.As(err, &apiError) || apiError.Status != 401 {
			t.Fatalf("unauthorized request returned %T %v", err, err)
		}
	}
	if _, err := client.NewOperator(server.URL, "wrong-secret").ListAgents(ctx); err == nil {
		t.Fatal("operator route accepted the wrong token")
	}
	rawTokenRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/agents", nil)
	if err != nil {
		t.Fatal(err)
	}
	rawTokenRequest.Header.Set("Authorization", "operator-secret")
	rawTokenResponse, err := http.DefaultClient.Do(rawTokenRequest)
	if err != nil {
		t.Fatal(err)
	}
	rawTokenResponse.Body.Close()
	if rawTokenResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("raw token status = %d", rawTokenResponse.StatusCode)
	}
	healthResponse, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	healthResponse.Body.Close()
	if healthResponse.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", healthResponse.StatusCode)
	}
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
