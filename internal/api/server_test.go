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
	if _, err := client.New(server.URL).ScheduleMessage(ctx, lease.Token, time.Second, json.RawMessage(`{"reason":"scheduled wake"}`)); err != nil {
		t.Fatal(err)
	}
	messages, err := client.New(server.URL).WaitMessages(ctx, lease.Token, 3*time.Second)
	if err != nil || len(messages) != 1 || messages[0].Sender != "agent:http-agent" {
		t.Fatalf("scheduled messages = %#v, %v", messages, err)
	}
	for _, path := range []string{"/v1/self/sleep", "/v1/self/terminate"} {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+lease.Token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("removed self route %s returned %d", path, response.StatusCode)
		}
	}
	agent, err = api.SetState(ctx, "http-agent", model.StateTerminated)
	if err != nil || agent.State != model.StateTerminated {
		t.Fatalf("terminated agent = %#v, %v", agent, err)
	}
}
