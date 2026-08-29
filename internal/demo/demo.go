package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/adamtopaz/polis/internal/client"
)

// Run is a deterministic persistent runtime used only to prove the lifecycle plumbing.
func Run(ctx context.Context) error {
	workspace := os.Getenv("POLIS_WORKSPACE")
	if workspace == "" {
		return errors.New("POLIS_WORKSPACE is required")
	}
	url := os.Getenv("POLIS_URL")
	if url == "" {
		return errors.New("POLIS_URL is required")
	}
	token := os.Getenv("POLIS_AGENT_TOKEN")
	if token == "" {
		return errors.New("POLIS_AGENT_TOKEN is required")
	}

	api := client.New(url)
	agent, err := api.Self(ctx, token)
	if err != nil {
		return fmt.Errorf("read identity: %w", err)
	}
	for ctx.Err() == nil {
		messages, err := api.WaitMessages(ctx, token, 30*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("wait for mailbox: %w", err)
		}
		if len(messages) == 0 {
			continue
		}

		line := fmt.Sprintf("%s handled %d unread message(s)\n", time.Now().UTC().Format(time.RFC3339), len(messages))
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

		data, _ := json.Marshal(map[string]any{"messages": len(messages), "workspace": workspace, "interface": "persistent mailbox"})
		if _, err := api.Journal(ctx, token, "demo.observed", data); err != nil {
			return fmt.Errorf("write Polis journal: %w", err)
		}
		if err := api.AckMessages(ctx, token, messages[len(messages)-1].ID); err != nil {
			return fmt.Errorf("ack mailbox: %w", err)
		}
		fmt.Printf("demo agent %s handled %d message(s) and is waiting\n", agent.ID, len(messages))
	}
	return nil
}
