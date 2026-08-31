package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adamtopaz/polis/internal/token"
	"github.com/adamtopaz/polis/internal/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "polis-worker:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("polis-worker", flag.ContinueOnError)
	controllerURL := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "controller URL")
	agentID := flags.String("agent", env("POLIS_AGENT_ID", ""), "agent id this worker exclusively supervises")
	id := flags.String("id", env("POLIS_WORKER_ID", env("HOSTNAME", "worker")), "worker id")
	workspace := flags.String("workspace", env("POLIS_WORKSPACE", "./workspace"), "durable agent workspace")
	lease := flags.Duration("lease", envDuration("POLIS_LEASE_DURATION", 30*time.Second), "incarnation lease duration")
	grace := flags.Duration("shutdown-grace", envDuration("POLIS_SHUTDOWN_GRACE", 10*time.Second), "runtime shutdown grace")
	workerTokenFile := flags.String("worker-token-file", env("POLIS_WORKER_TOKEN_FILE", ""), "consumed path to the worker token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("polis-worker takes no positional arguments")
	}
	workerToken, err := token.Load("POLIS_WORKER_TOKEN", *workerTokenFile, "worker")
	if err != nil {
		return err
	}
	if *workerTokenFile != "" {
		if err := os.Remove(*workerTokenFile); err != nil {
			return fmt.Errorf("consume worker token file: %w", err)
		}
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	return worker.Run(ctx, worker.Config{
		ControllerURL: *controllerURL,
		WorkerToken:   workerToken,
		AgentID:       *agentID,
		ID:            *id,
		Workspace:     *workspace,
		LeaseDuration: *lease,
		ShutdownGrace: *grace,
		Logger:        logger,
	})
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
