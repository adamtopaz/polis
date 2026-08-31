package sessionhistory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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

func WriteHuman(w io.Writer, history History) error {
	var header struct {
		ID        string `json:"id"`
		Timestamp string `json:"timestamp"`
		CWD       string `json:"cwd"`
	}
	if err := json.Unmarshal(history.Session, &header); err != nil {
		return fmt.Errorf("decode session header: %w", err)
	}
	if _, err := fmt.Fprintf(w, "Agent: %s\nSession: %s\nStarted: %s\nWorkspace: %s\nFile: %s\n",
		history.AgentID, header.ID, header.Timestamp, header.CWD, history.SessionFile); err != nil {
		return err
	}
	for _, raw := range history.Items {
		if err := writeHumanEntry(w, raw); err != nil {
			return err
		}
	}
	return nil
}

func writeHumanEntry(w io.Writer, raw json.RawMessage) error {
	var entry struct {
		Type          string          `json:"type"`
		Timestamp     string          `json:"timestamp"`
		Provider      string          `json:"provider"`
		ModelID       string          `json:"modelId"`
		ThinkingLevel string          `json:"thinkingLevel"`
		Summary       string          `json:"summary"`
		TokensBefore  int             `json:"tokensBefore"`
		CustomType    string          `json:"customType"`
		Content       json.RawMessage `json:"content"`
		Message       json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return fmt.Errorf("decode %s entry: %w", entry.Type, err)
	}

	switch entry.Type {
	case "model_change":
		return writeSection(w, entry.Timestamp, "MODEL", strings.Trim(entry.Provider+"/"+entry.ModelID, "/"))
	case "thinking_level_change":
		return writeSection(w, entry.Timestamp, "THINKING LEVEL", entry.ThinkingLevel)
	case "message":
		return writeHumanMessage(w, entry.Timestamp, entry.Message)
	case "compaction":
		label := "COMPACTION"
		if entry.TokensBefore > 0 {
			label = fmt.Sprintf("COMPACTION (%d tokens before)", entry.TokensBefore)
		}
		return writeSection(w, entry.Timestamp, label, entry.Summary)
	case "branch_summary":
		return writeSection(w, entry.Timestamp, "BRANCH SUMMARY", entry.Summary)
	case "custom_message":
		body, err := humanContent(entry.Content)
		if err != nil {
			return err
		}
		return writeSection(w, entry.Timestamp, "CUSTOM "+entry.CustomType, body)
	default:
		return nil
	}
}

func writeHumanMessage(w io.Writer, timestamp string, raw json.RawMessage) error {
	var message struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		Provider   string          `json:"provider"`
		Model      string          `json:"model"`
		ToolName   string          `json:"toolName"`
		IsError    bool            `json:"isError"`
		StopReason string          `json:"stopReason"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return fmt.Errorf("decode message entry: %w", err)
	}
	body, err := humanContent(message.Content)
	if err != nil {
		return err
	}
	label := strings.ToUpper(message.Role)
	switch message.Role {
	case "assistant":
		model := strings.Trim(message.Provider+"/"+message.Model, "/")
		if model != "" {
			label += " (" + model + ")"
		}
		if message.StopReason != "" && message.StopReason != "stop" {
			label += " [" + message.StopReason + "]"
		}
	case "toolResult":
		label = "TOOL RESULT " + message.ToolName
		if message.IsError {
			label += " (error)"
		} else {
			label += " (ok)"
		}
	}
	return writeSection(w, timestamp, label, body)
}

func humanContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("decode message content: %w", err)
	}
	parts := make([]string, 0, len(blocks))
	for _, rawBlock := range blocks {
		var block struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			MimeType  string          `json:"mimeType"`
		}
		if err := json.Unmarshal(rawBlock, &block); err != nil {
			return "", fmt.Errorf("decode message content block: %w", err)
		}
		switch block.Type {
		case "text":
			parts = appendNonEmpty(parts, block.Text)
		case "thinking":
			if strings.TrimSpace(block.Thinking) != "" {
				parts = append(parts, "Thinking:\n"+block.Thinking)
			}
		case "toolCall":
			arguments, err := prettyJSON(block.Arguments)
			if err != nil {
				return "", fmt.Errorf("decode %s tool arguments: %w", block.Name, err)
			}
			parts = append(parts, "Tool call: "+block.Name+"\n"+arguments)
		case "image":
			parts = append(parts, "[image: "+block.MimeType+"]")
		default:
			parts = append(parts, "["+block.Type+"]")
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func appendNonEmpty(parts []string, value string) []string {
	if strings.TrimSpace(value) == "" {
		return parts
	}
	return append(parts, value)
}

func prettyJSON(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "{}", nil
	}
	var output bytes.Buffer
	if err := json.Indent(&output, raw, "", "  "); err != nil {
		return "", err
	}
	return output.String(), nil
}

func writeSection(w io.Writer, timestamp, label, body string) error {
	if timestamp != "" {
		label += " · " + timestamp
	}
	if _, err := fmt.Fprintf(w, "\n%s\n", label); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return nil
	}
	_, err := fmt.Fprintln(w, body)
	return err
}
