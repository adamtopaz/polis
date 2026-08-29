package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/adamtopaz/polis/internal/client"
)

// Run is a deterministic runtime used only to prove the lifecycle plumbing.
// A real Polis agent is any executable that uses the same environment and API.
func Run(ctx context.Context) error {
	controllerURL := os.Getenv("POLIS_URL")
	token := os.Getenv("POLIS_AGENT_TOKEN")
	workspace := os.Getenv("POLIS_WORKSPACE")
	if controllerURL == "" || token == "" || workspace == "" {
		return fmt.Errorf("POLIS_URL, POLIS_AGENT_TOKEN, and POLIS_WORKSPACE are required")
	}

	api := client.New(controllerURL)
	agent, err := api.Self(ctx, token)
	if err != nil {
		return fmt.Errorf("read identity: %w", err)
	}
	messages, err := api.Messages(ctx, token)
	if err != nil {
		return fmt.Errorf("read mailbox: %w", err)
	}

	line := fmt.Sprintf("%s incarnation ran with %d unread message(s)\n", time.Now().UTC().Format(time.RFC3339), len(messages))
	logPath := filepath.Join(workspace, "demo.log")
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open workspace journal: %w", err)
	}
	if _, err := file.WriteString(line); err != nil {
		file.Close()
		return fmt.Errorf("write workspace journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close workspace journal: %w", err)
	}

	data, _ := json.Marshal(map[string]any{"messages": len(messages), "workspace": workspace})
	if _, err := api.Journal(ctx, token, "demo.observed", data); err != nil {
		return fmt.Errorf("write Polis journal: %w", err)
	}
	if len(messages) > 0 {
		if err := api.AckMessages(ctx, token, messages[len(messages)-1].ID); err != nil {
			return fmt.Errorf("ack mailbox: %w", err)
		}
	}
	if _, err := api.Sleep(ctx, token, time.Hour); err != nil {
		return fmt.Errorf("sleep: %w", err)
	}
	fmt.Printf("demo agent %s yielded for one hour\n", agent.ID)
	return nil
}
