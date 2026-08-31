package sessionhistory

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecode(t *testing.T) {
	contents := []byte("" +
		`{"type":"session","id":"session-1","cwd":"/workspace"}` + "\n" +
		`{"type":"message","id":"one","message":{"role":"user","content":"hello"}}` + "\n" +
		`{"type":"message","id":"two","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n")
	history, err := Decode("researcher", "/workspace/.polis/pi-sessions/session.jsonl", contents, 1)
	if err != nil {
		t.Fatal(err)
	}
	if history.AgentID != "researcher" || len(history.Items) != 1 {
		t.Fatalf("history = %#v", history)
	}
	var header struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(history.Session, &header); err != nil || header.ID != "session-1" {
		t.Fatalf("session header = %s, %v", history.Session, err)
	}
}

func TestDecodeRejectsInvalidSession(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents string
		tail     int
	}{
		{name: "negative tail", contents: `{"type":"session"}` + "\n", tail: -1},
		{name: "missing header", contents: `{"type":"message"}` + "\n"},
		{name: "invalid JSON", contents: `{"type":"session"}` + "\n{"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode("agent", "session.jsonl", []byte(test.contents), test.tail); err == nil {
				t.Fatal("Decode succeeded")
			}
		})
	}
}

func TestWriteHuman(t *testing.T) {
	history := History{
		AgentID:     "researcher",
		SessionFile: "/workspace/.polis/pi-sessions/session.jsonl",
		Session:     json.RawMessage(`{"type":"session","id":"session-1","timestamp":"2026-08-31T20:57:48Z","cwd":"/workspace"}`),
		Items: []json.RawMessage{
			json.RawMessage(`{"type":"model_change","timestamp":"2026-08-31T20:57:48Z","provider":"openai-codex","modelId":"gpt-5.5"}`),
			json.RawMessage(`{"type":"thinking_level_change","timestamp":"2026-08-31T20:57:49Z","thinkingLevel":"high"}`),
			json.RawMessage(`{"type":"message","timestamp":"2026-08-31T20:58:00Z","message":{"role":"user","content":"Please inspect the workspace."}}`),
			json.RawMessage(`{"type":"message","timestamp":"2026-08-31T20:58:01Z","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5.5","stopReason":"toolUse","content":[{"type":"thinking","thinking":"I should list the files.","thinkingSignature":"opaque-secret-signature"},{"type":"toolCall","name":"bash","arguments":{"command":"ls"}},{"type":"image","mimeType":"image/png","data":"base64-image-data"}]}}`),
			json.RawMessage(`{"type":"message","timestamp":"2026-08-31T20:58:02Z","message":{"role":"toolResult","toolName":"bash","isError":false,"content":[{"type":"text","text":"README.md"}]}}`),
			json.RawMessage(`{"type":"compaction","timestamp":"2026-08-31T21:00:00Z","summary":"Earlier work was summarized.","tokensBefore":120000}`),
			json.RawMessage(`{"type":"custom_message","timestamp":"2026-08-31T21:01:00Z","customType":"notice","content":"Remember this."}`),
		},
	}

	var output bytes.Buffer
	if err := WriteHuman(&output, history); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{
		"Agent: researcher",
		"Session: session-1",
		"MODEL · 2026-08-31T20:57:48Z\nopenai-codex/gpt-5.5",
		"THINKING LEVEL · 2026-08-31T20:57:49Z\nhigh",
		"USER · 2026-08-31T20:58:00Z\nPlease inspect the workspace.",
		"ASSISTANT (openai-codex/gpt-5.5) [toolUse]",
		"Thinking:\nI should list the files.",
		"Tool call: bash\n{\n  \"command\": \"ls\"\n}",
		"[image: image/png]",
		"TOOL RESULT bash (ok)",
		"COMPACTION (120000 tokens before)",
		"CUSTOM notice",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("human history does not contain %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"opaque-secret-signature", "base64-image-data"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("human history contains opaque data %q:\n%s", unwanted, got)
		}
	}
}

func TestWriteHumanRejectsInvalidContent(t *testing.T) {
	history := History{
		Session: json.RawMessage(`{"type":"session","id":"session-1"}`),
		Items: []json.RawMessage{
			json.RawMessage(`{"type":"message","message":{"role":"user","content":{"unexpected":true}}}`),
		},
	}
	if err := WriteHuman(&bytes.Buffer{}, history); err == nil {
		t.Fatal("WriteHuman succeeded")
	}
}
