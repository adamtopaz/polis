package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/adamtopaz/polis/internal/model"
)

// Run is a deterministic runtime used only to prove the lifecycle plumbing.
// A real Polis agent is any executable that uses the same environment and API.
func Run(ctx context.Context) error {
	workspace := os.Getenv("POLIS_WORKSPACE")
	if workspace == "" {
		return fmt.Errorf("POLIS_WORKSPACE is required")
	}

	var agent model.Agent
	if err := runPolisJSON(ctx, &agent, "self", "inspect"); err != nil {
		return fmt.Errorf("read identity: %w", err)
	}
	var mailbox struct {
		Items []model.Message `json:"items"`
	}
	if err := runPolisJSON(ctx, &mailbox, "self", "messages"); err != nil {
		return fmt.Errorf("read mailbox: %w", err)
	}

	line := fmt.Sprintf("%s incarnation ran with %d unread message(s)\n", time.Now().UTC().Format(time.RFC3339), len(mailbox.Items))
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

	data, _ := json.Marshal(map[string]any{"messages": len(mailbox.Items), "workspace": workspace, "interface": "polis self"})
	var event model.Event
	if err := runPolisJSON(ctx, &event, "self", "journal", "demo.observed", string(data)); err != nil {
		return fmt.Errorf("write Polis journal: %w", err)
	}
	if len(mailbox.Items) > 0 {
		var acknowledged map[string]bool
		through := strconv.FormatUint(mailbox.Items[len(mailbox.Items)-1].ID, 10)
		if err := runPolisJSON(ctx, &acknowledged, "self", "ack", through); err != nil {
			return fmt.Errorf("ack mailbox: %w", err)
		}
	}
	var sleeping model.Agent
	if err := runPolisJSON(ctx, &sleeping, "self", "sleep", "1h"); err != nil {
		return fmt.Errorf("sleep: %w", err)
	}
	fmt.Printf("demo agent %s yielded for one hour\n", agent.ID)
	return nil
}

func runPolisJSON(ctx context.Context, target any, args ...string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate polis executable: %w", err)
	}
	command := exec.CommandContext(ctx, executable, args...)
	output, err := command.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("%w: %s", err, exitError.Stderr)
		}
		return err
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("decode polis output: %w", err)
	}
	return nil
}
