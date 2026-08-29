package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adamtopaz/polis/internal/api"
	"github.com/adamtopaz/polis/internal/client"
	"github.com/adamtopaz/polis/internal/demo"
	"github.com/adamtopaz/polis/internal/model"
	"github.com/adamtopaz/polis/internal/store"
	"github.com/adamtopaz/polis/internal/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "polis:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "server":
		return runServer(ctx, args[1:])
	case "worker":
		return runWorker(ctx, args[1:])
	case "agent":
		return runAgentCommand(ctx, args[1:])
	case "message":
		return runMessageCommand(ctx, args[1:])
	case "events":
		return runEventsCommand(ctx, args[1:])
	case "demo-agent":
		return demo.Run(ctx)
	case "help", "-h", "--help":
		fmt.Print(usage())
		return nil
	default:
		return usageError()
	}
}

func runServer(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	listen := flags.String("listen", env("POLIS_LISTEN", ":8080"), "HTTP listen address")
	dbPath := flags.String("db", env("POLIS_DB_PATH", "./polis.db"), "database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("server takes no positional arguments")
	}
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o750); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	database, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer database.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	server := &http.Server{
		Addr:              *listen,
		Handler:           api.New(database, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	result := make(chan error, 1)
	go func() {
		logger.Info("controller listening", "address", *listen, "database", *dbPath)
		result <- server.ListenAndServe()
	}()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func runWorker(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	controllerURL := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "controller URL")
	id := flags.String("id", env("POLIS_WORKER_ID", env("HOSTNAME", "worker")), "worker id")
	workspaceRoot := flags.String("workspace-root", env("POLIS_WORKSPACE_ROOT", "./workspaces"), "durable workspace root")
	lease := flags.Duration("lease", envDuration("POLIS_LEASE_DURATION", 30*time.Second), "incarnation lease duration")
	grace := flags.Duration("shutdown-grace", envDuration("POLIS_SHUTDOWN_GRACE", 10*time.Second), "runtime shutdown grace")
	slots := flags.Int("slots", envInt("POLIS_SLOTS", 1), "concurrent agent processes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("worker takes no positional arguments")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	return worker.Run(ctx, worker.Config{
		ControllerURL: *controllerURL,
		ID:            *id,
		WorkspaceRoot: *workspaceRoot,
		LeaseDuration: *lease,
		ShutdownGrace: *grace,
		Slots:         *slots,
		Logger:        logger,
	})
}

func runAgentCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("agent requires create, list, get, or state")
	}
	switch args[0] {
	case "create":
		flags := flag.NewFlagSet("agent create", flag.ContinueOnError)
		url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "controller URL")
		id := flags.String("id", "", "stable agent id (generated when omitted)")
		charter := flags.String("charter", "", "agent charter")
		runtimeJSON := flags.String("runtime", "", "runtime argv as a JSON array")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		var runtime []string
		if err := json.Unmarshal([]byte(*runtimeJSON), &runtime); err != nil {
			return fmt.Errorf("parse runtime: %w", err)
		}
		agent, err := client.New(*url).CreateAgent(ctx, *id, *charter, runtime)
		return printJSON(agent, err)
	case "list":
		flags := flag.NewFlagSet("agent list", flag.ContinueOnError)
		url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "controller URL")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		agents, err := client.New(*url).ListAgents(ctx)
		return printJSON(map[string]any{"items": agents}, err)
	case "get":
		flags := flag.NewFlagSet("agent get", flag.ContinueOnError)
		url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "controller URL")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("agent get requires an agent id")
		}
		agent, err := client.New(*url).GetAgent(ctx, flags.Arg(0))
		return printJSON(agent, err)
	case "state":
		flags := flag.NewFlagSet("agent state", flag.ContinueOnError)
		url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "controller URL")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 2 {
			return errors.New("agent state requires an agent id and active, paused, or terminated")
		}
		agent, err := client.New(*url).SetState(ctx, flags.Arg(0), model.State(flags.Arg(1)))
		return printJSON(agent, err)
	default:
		return errors.New("agent requires create, list, get, or state")
	}
}

func runMessageCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("message", flag.ContinueOnError)
	url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "controller URL")
	sender := flags.String("sender", "operator", "sender label")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return errors.New("message requires an agent id and a JSON body")
	}
	body := json.RawMessage(flags.Arg(1))
	if !json.Valid(body) {
		return errors.New("message body must be valid JSON")
	}
	message, err := client.New(*url).SendControlMessage(ctx, flags.Arg(0), *sender, body)
	return printJSON(message, err)
}

func runEventsCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("events", flag.ContinueOnError)
	url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "controller URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("events accepts at most one agent id")
	}
	id := ""
	if flags.NArg() == 1 {
		id = flags.Arg(0)
	}
	events, err := client.New(*url).Events(ctx, id)
	return printJSON(map[string]any{"items": events}, err)
}

func printJSON(value any, err error) error {
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usageError() error {
	return errors.New(strings.TrimSpace(usage()))
}

func usage() string {
	return `Polis manages the existence of autonomous agents, not their work.

Usage:
  polis server [flags]
  polis worker [flags]
  polis agent create --charter TEXT --runtime '["command","arg"]' [--id ID]
  polis agent list
  polis agent get ID
  polis agent state ID active|paused|terminated
  polis message ID JSON
  polis events [ID]
`
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

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscan(value, &parsed); err != nil {
		return fallback
	}
	return parsed
}
