package sessionhistory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

type History struct {
	AgentID     string            `json:"agent_id"`
	SessionFile string            `json:"session_file"`
	Session     json.RawMessage   `json:"session"`
	Items       []json.RawMessage `json:"items"`
}

func Decode(agentID, sessionFile string, contents []byte, tail int) (History, error) {
	if tail < 0 {
		return History{}, errors.New("tail must not be negative")
	}
	lines := bytes.Split(contents, []byte("\n"))
	items := make([]json.RawMessage, 0, len(lines))
	var session json.RawMessage
	for index, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			return History{}, fmt.Errorf("decode session line %d: invalid JSON", index+1)
		}
		entry := append(json.RawMessage(nil), line...)
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return History{}, fmt.Errorf("decode session line %d: %w", index+1, err)
		}
		if envelope.Type == "session" && session == nil {
			session = entry
			continue
		}
		items = append(items, entry)
	}
	if session == nil {
		return History{}, errors.New("session header is missing")
	}
	if tail > 0 && len(items) > tail {
		items = items[len(items)-tail:]
	}
	return History{AgentID: agentID, SessionFile: sessionFile, Session: session, Items: items}, nil
}
