package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adamtopaz/polis/internal/client"
	"github.com/adamtopaz/polis/internal/model"
)

const incarnationRetryDelay = 2 * time.Second

type Config struct {
	ControllerURL string
	WorkerToken   string
	AgentID       string
	ID            string
	Workspace     string
	LeaseDuration time.Duration
	ShutdownGrace time.Duration
	Logger        *slog.Logger
}

func Run(ctx context.Context, config Config) error {
	if config.WorkerToken == "" {
		return errors.New("worker token is required")
	}
	if config.ID == "" {
		return errors.New("worker id is required")
	}
	if config.AgentID == "" {
		return errors.New("agent id is required")
	}
	if config.LeaseDuration < 5*time.Second {
		return errors.New("lease duration must be at least 5s")
	}
	if config.ShutdownGrace <= 0 {
		return errors.New("shutdown grace must be positive")
	}
	if config.Workspace == "" {
		return errors.New("workspace is required")
	}
	if err := os.MkdirAll(config.Workspace, 0o750); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	return runAgent(ctx, config)
}

func runAgent(ctx context.Context, config Config) error {
	api := client.NewWorker(config.ControllerURL, config.WorkerToken)
	log := config.Logger.With("worker", config.ID, "agent", config.AgentID)
	for ctx.Err() == nil {
		lease, ok, err := api.Acquire(ctx, config.AgentID, config.ID, config.LeaseDuration, 20*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Warn("acquire failed", "error", err)
			if !wait(ctx, 2*time.Second) {
				return ctx.Err()
			}
			continue
		}
		if !ok {
			continue
		}
		log.Info("incarnation acquired", "agent", lease.Agent.ID)
		runIncarnation(ctx, api, config, lease, log)
		if !wait(ctx, incarnationRetryDelay) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func runIncarnation(parent context.Context, api *client.Client, config Config, lease model.Lease, log *slog.Logger) {
	workspace := config.Workspace
	metadata := filepath.Join(workspace, ".polis")
	if err := os.MkdirAll(metadata, 0o750); err != nil {
		reportExit(api, lease.Token, "prepare workspace: "+err.Error(), log)
		return
	}
	charterPath := filepath.Join(metadata, "charter.md")
	if err := os.WriteFile(charterPath, []byte(lease.Agent.Charter+"\n"), 0o640); err != nil {
		reportExit(api, lease.Token, "write charter: "+err.Error(), log)
		return
	}

	command := exec.Command(lease.Agent.Runtime[0], lease.Agent.Runtime[1:]...)
	command.Dir = workspace
	command.Env = append(withoutControlPlaneCredentials(os.Environ()),
		"POLIS_URL="+config.ControllerURL,
		"POLIS_AGENT_ID="+lease.Agent.ID,
		"POLIS_AGENT_TOKEN="+lease.Token,
		"POLIS_WORKSPACE="+workspace,
		"POLIS_CHARTER_PATH="+charterPath,
	)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = nil
	if err := command.Start(); err != nil {
		reportExit(api, lease.Token, "start runtime: "+err.Error(), log)
		return
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	heartbeatEvery := config.LeaseDuration / 3
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Until(lease.ExpiresAt))
	defer deadline.Stop()

	for {
		select {
		case err := <-done:
			detail := ""
			if err != nil {
				detail = err.Error()
			}
			reportExit(api, lease.Token, detail, log)
			log.Info("incarnation exited", "agent", lease.Agent.ID, "detail", detail)
			return
		case <-ticker.C:
			heartbeat, err := api.Heartbeat(parent, lease.Token, config.LeaseDuration)
			if err != nil {
				var apiError *client.Error
				if errors.As(err, &apiError) && apiError.Status == 401 {
					stop(command, done, config.ShutdownGrace)
					log.Info("incarnation fence revoked", "agent", lease.Agent.ID)
					return
				}
				log.Warn("heartbeat failed", "agent", lease.Agent.ID, "error", err)
				continue
			}
			if !heartbeat.Continue {
				stop(command, done, config.ShutdownGrace)
				log.Info("incarnation stopped by controller", "agent", lease.Agent.ID)
				return
			}
			resetTimer(deadline, time.Until(heartbeat.ExpiresAt))
		case <-deadline.C:
			stop(command, done, config.ShutdownGrace)
			log.Warn("incarnation stopped after lease expired", "agent", lease.Agent.ID)
			return
		case <-parent.Done():
			stop(command, done, config.ShutdownGrace)
			reportExit(api, lease.Token, "worker shutting down", log)
			return
		}
	}
}

func withoutControlPlaneCredentials(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, variable := range environment {
		if strings.HasPrefix(variable, "POLIS_OPERATOR_TOKEN=") ||
			strings.HasPrefix(variable, "POLIS_OPERATOR_TOKEN_FILE=") ||
			strings.HasPrefix(variable, "POLIS_WORKER_TOKEN=") ||
			strings.HasPrefix(variable, "POLIS_WORKER_TOKEN_FILE=") ||
			strings.HasPrefix(variable, "POLIS_AGENT_ID=") ||
			strings.HasPrefix(variable, "POLIS_AGENT_TOKEN=") ||
			strings.HasPrefix(variable, "POLIS_WORKSPACE=") ||
			strings.HasPrefix(variable, "POLIS_CHARTER_PATH=") {
			continue
		}
		filtered = append(filtered, variable)
	}
	return filtered
}

func stop(command *exec.Cmd, done <-chan error, grace time.Duration) {
	if command.Process == nil {
		return
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		_ = command.Process.Kill()
		<-done
	}
}

func reportExit(api *client.Client, token, detail string, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := api.Exited(ctx, token, detail); err != nil {
		log.Debug("could not report incarnation exit", "error", err)
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
