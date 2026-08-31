package sessionhistory

import (
	"encoding/json"
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
