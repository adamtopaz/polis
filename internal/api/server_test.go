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
	server := httptest.NewServer(New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "operator-secret", "worker-secret").Handler())
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
	removedCreateRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/agents", nil)
	if err != nil {
		t.Fatal(err)
	}
	removedCreateRequest.Header.Set("Authorization", "Bearer operator-secret")
	removedCreateResponse, err := http.DefaultClient.Do(removedCreateRequest)
	if err != nil {
		t.Fatal(err)
	}
	removedCreateResponse.Body.Close()
	if removedCreateResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("removed create route returned %d", removedCreateResponse.StatusCode)
	}
	removedApplyRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, server.URL+"/v1/agents/http-agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	removedApplyRequest.Header.Set("Authorization", "Bearer operator-secret")
	removedApplyResponse, err := http.DefaultClient.Do(removedApplyRequest)
	if err != nil {
		t.Fatal(err)
	}
	removedApplyResponse.Body.Close()
	if removedApplyResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("removed apply route returned %d", removedApplyResponse.StatusCode)
	}
	if _, _, err := client.New(server.URL).Acquire(ctx, "http-agent", "test-worker", 30*time.Second, 0, nil); err == nil {
		t.Fatal("worker acquire accepted no token")
	} else {
		var apiError *client.Error
		if !errors.As(err, &apiError) || apiError.Status != 401 {
			t.Fatalf("unauthorized worker acquire returned %T %v", err, err)
		}
	}
	if _, _, err := client.NewWorker(server.URL, "wrong-secret").Acquire(ctx, "http-agent", "test-worker", 30*time.Second, 0, nil); err == nil {
		t.Fatal("worker acquire accepted the wrong token")
	}
	workerAPI := client.NewWorker(server.URL, "worker-secret")
	lease, ok, err := workerAPI.Acquire(ctx, "http-agent", "test-worker", 30*time.Second, 0, nil)
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
	message, err := client.New(server.URL).SendMessage(ctx, lease.Token, "http-agent", json.RawMessage(`{"reason":"self message"}`))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := client.New(server.URL).WaitMessages(ctx, lease.Token, time.Second)
	if err != nil || len(messages) != 1 || messages[0].ID != message.ID || messages[0].Sender != "agent:http-agent" {
		t.Fatalf("self messages = %#v, %v", messages, err)
	}
	for _, path := range []string{"/v1/self/sleep", "/v1/self/terminate", "/v1/self/spawn", "/v1/self/schedule"} {
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
	agent, err := api.SetState(ctx, "http-agent", model.StateTerminated)
	if err != nil || agent.State != model.StateTerminated {
		t.Fatalf("terminated agent = %#v, %v", agent, err)
	}
}

func TestHTTPAcknowledgementCursorIsMonotonic(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "polis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := httptest.NewServer(New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "operator-secret", "worker-secret").Handler())
	defer server.Close()

	ctx := context.Background()
	operatorAPI := client.NewOperator(server.URL, "operator-secret")
	lease, ok, err := client.NewWorker(server.URL, "worker-secret").Acquire(ctx, "ack-agent", "test-worker", 30*time.Second, 0, nil)
	if err != nil || !ok {
		t.Fatalf("acquire = %#v, %v, %v", lease, ok, err)
	}
	agentAPI := client.New(server.URL)
	messageN, err := operatorAPI.SendControlMessage(ctx, "ack-agent", "operator", json.RawMessage(`{"sequence":"N"}`))
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := agentAPI.Messages(ctx, lease.Token)
	if err != nil || len(delivered) != 1 || delivered[0].ID != messageN.ID {
		t.Fatalf("delivery N = %#v, %v", delivered, err)
	}

	messageNPlusOne, err := operatorAPI.SendControlMessage(ctx, "ack-agent", "operator", json.RawMessage(`{"sequence":"N+1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := agentAPI.AckMessages(ctx, lease.Token, messageNPlusOne.ID); err != nil {
		t.Fatalf("manual ack N+1: %v", err)
	}
	if err := agentAPI.AckMessages(ctx, lease.Token, messageN.ID); err != nil {
		t.Fatalf("older end-of-turn ack N: %v", err)
	}
	if err := agentAPI.AckMessages(ctx, lease.Token, messageNPlusOne.ID); err != nil {
		t.Fatalf("equal ack N+1: %v", err)
	}
	if err := agentAPI.AckMessages(ctx, lease.Token, messageNPlusOne.ID+1); err == nil {
		t.Fatal("forward nonexistent ack succeeded")
	} else {
		var apiError *client.Error
		if !errors.As(err, &apiError) || apiError.Status != http.StatusBadRequest || apiError.Message != "message does not exist" {
			t.Fatalf("forward nonexistent ack returned %T %v", err, err)
		}
	}
	if err := agentAPI.AckMessages(ctx, "invalid-token", messageN.ID); err == nil {
		t.Fatal("invalid token ack succeeded")
	} else {
		var apiError *client.Error
		if !errors.As(err, &apiError) || apiError.Status != http.StatusUnauthorized {
			t.Fatalf("invalid token ack returned %T %v", err, err)
		}
	}

	messageNPlusTwo, err := operatorAPI.SendControlMessage(ctx, "ack-agent", "operator", json.RawMessage(`{"sequence":"N+2"}`))
	if err != nil {
		t.Fatal(err)
	}
	delivered, err = agentAPI.Messages(ctx, lease.Token)
	if err != nil || len(delivered) != 1 || delivered[0].ID != messageNPlusTwo.ID {
		t.Fatalf("delivery after older ack = %#v, %v", delivered, err)
	}
	if _, err := operatorAPI.SetState(ctx, "ack-agent", model.StatePaused); err != nil {
		t.Fatal(err)
	}
	if err := agentAPI.AckMessages(ctx, lease.Token, messageNPlusTwo.ID); err == nil {
		t.Fatal("revoked lease ack succeeded")
	} else {
		var apiError *client.Error
		if !errors.As(err, &apiError) || apiError.Status != http.StatusUnauthorized {
			t.Fatalf("revoked lease ack returned %T %v", err, err)
		}
	}
}

func TestHTTPMessagingPolicy(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "polis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := httptest.NewServer(New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "operator-secret", "worker-secret").Handler())
	defer server.Close()

	ctx := context.Background()
	workerAPI := client.NewWorker(server.URL, "worker-secret")
	for _, id := range []string{"allowed", "blocked"} {
		lease, ok, err := workerAPI.Acquire(ctx, id, id+"-worker", 30*time.Second, 0, nil)
		if err != nil || !ok {
			t.Fatalf("register %s = %#v, %v, %v", id, lease, ok, err)
		}
		if err := workerAPI.Exited(ctx, lease.Token, "registered"); err != nil {
			t.Fatal(err)
		}
	}

	allowedRecipients := []string{"allowed"}
	sender, ok, err := workerAPI.Acquire(ctx, "sender", "sender-worker", 30*time.Second, 0, &allowedRecipients)
	if err != nil || !ok {
		t.Fatalf("acquire sender = %#v, %v, %v", sender, ok, err)
	}
	agentAPI := client.New(server.URL)
	if _, err := agentAPI.SendMessage(ctx, sender.Token, "allowed", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := agentAPI.SendMessage(ctx, sender.Token, "blocked", json.RawMessage(`{"ok":false}`)); err == nil {
		t.Fatal("restricted HTTP send succeeded")
	} else {
		var apiError *client.Error
		if !errors.As(err, &apiError) || apiError.Status != http.StatusForbidden {
			t.Fatalf("restricted HTTP send returned %T %v", err, err)
		}
	}
}
