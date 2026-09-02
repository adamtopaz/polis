package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/adamtopaz/polis/internal/model"
)

func TestControlPlaneCredentialsAreNotPassedToAgentRuntime(t *testing.T) {
	environment := []string{
		"PATH=/bin",
		"POLIS_OPERATOR_TOKEN=operator-secret",
		"POLIS_OPERATOR_TOKEN_FILE=/run/secrets/operator-token",
		"POLIS_WORKER_TOKEN=worker-secret",
		"POLIS_WORKER_TOKEN_FILE=/run/secrets/worker-token",
		"POLIS_AGENT_ID=alpha",
		`POLIS_ALLOWED_RECIPIENTS=["beta"]`,
		"POLIS_ADDITIONAL_INSTRUCTIONS=forged instructions",
		"POLIS_ADDITIONAL_INSTRUCTIONS_PATH=/tmp/forged-instructions",
		"POLIS_WAKEUP_SECONDS=999",
	}
	filtered := withoutControlPlaneCredentials(environment)
	for _, credential := range environment[1:5] {
		if slices.Contains(filtered, credential) {
			t.Fatalf("control-plane credential reached runtime: %#v", filtered)
		}
	}
	if !slices.Contains(filtered, "PATH=/bin") {
		t.Fatalf("ordinary environment was removed: %#v", filtered)
	}
	if slices.Contains(filtered, "POLIS_AGENT_ID=alpha") {
		t.Fatalf("supervisor identity reached runtime unchanged: %#v", filtered)
	}
	if slices.Contains(filtered, `POLIS_ALLOWED_RECIPIENTS=["beta"]`) {
		t.Fatalf("supervisor messaging policy reached runtime unchanged: %#v", filtered)
	}
	for _, variable := range filtered {
		if variable == "POLIS_ADDITIONAL_INSTRUCTIONS=forged instructions" ||
			variable == "POLIS_ADDITIONAL_INSTRUCTIONS_PATH=/tmp/forged-instructions" ||
			variable == "POLIS_WAKEUP_SECONDS=999" {
			t.Fatalf("forged additional instructions reached runtime: %#v", filtered)
		}
	}
}

func TestWorkerLogsOneAgentKey(t *testing.T) {
	const agentID = "test-agent"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/worker/acquire":
			_ = json.NewEncoder(w).Encode(model.Lease{
				Token:     "lease-token",
				Agent:     model.Agent{ID: agentID},
				ExpiresAt: time.Now().Add(time.Minute),
			})
		case "/v1/worker/exited":
			w.WriteHeader(http.StatusNoContent)
			cancel()
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	runtime, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = runAgent(ctx, Config{
		MailboxURL:    server.URL,
		WorkerToken:   "worker-token",
		AgentID:       agentID,
		ID:            "test-worker",
		Charter:       "Test logs.",
		Runtime:       []string{runtime},
		Workspace:     t.TempDir(),
		LeaseDuration: 30 * time.Second,
		ShutdownGrace: time.Second,
		Logger:        slog.New(slog.NewJSONHandler(&output, nil)),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runAgent returned %v, want context cancellation", err)
	}

	seen := map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode log %q: %v", line, err)
		}
		message, _ := record["msg"].(string)
		if message != "incarnation acquired" && message != "incarnation exited" {
			continue
		}
		if count := topLevelJSONKeyCount(t, line, "agent"); count != 1 {
			t.Errorf("%q log has %d agent keys, want 1: %s", message, count, line)
		}
		if got := record["agent"]; got != agentID {
			t.Errorf("%q log agent = %#v, want %q", message, got, agentID)
		}
		seen[message] = true
	}
	for _, message := range []string{"incarnation acquired", "incarnation exited"} {
		if !seen[message] {
			t.Errorf("did not observe %q log in %s", message, output.Bytes())
		}
	}
}

func topLevelJSONKeyCount(t *testing.T, document []byte, want string) int {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(document))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		t.Fatalf("log is not a JSON object: %s", document)
	}
	count := 0
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			t.Fatalf("decode key from log %s: %v", document, err)
		}
		if key == want {
			count++
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("decode value from log %s: %v", document, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		t.Fatalf("decode end of log %s: %v", document, err)
	}
	return count
}

func TestWritePromptFiles(t *testing.T) {
	metadata := t.TempDir()
	charterPath, additionalPath, err := writePromptFiles(metadata, "Explore carefully.", "Keep reports concise.")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		charterPath:    "Explore carefully.\n",
		additionalPath: "Keep reports concise.\n",
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(contents) != want {
			t.Fatalf("%s = %q, want %q", filepath.Base(path), contents, want)
		}
	}

	_, additionalPath, err = writePromptFiles(metadata, "Explore carefully.", "")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(additionalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "\n" {
		t.Fatalf("cleared additional instructions = %q, want one empty line", contents)
	}
}
