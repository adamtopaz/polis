package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/adamtopaz/polis/internal/client"
	"github.com/adamtopaz/polis/internal/model"
	"github.com/adamtopaz/polis/internal/token"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "polisctl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "agent":
		return runAgent(ctx, args[1:])
	case "message":
		return runMessage(ctx, args[1:])
	case "events":
		return runEvents(ctx, args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage())
		return nil
	default:
		return usageError()
	}
}

func runAgent(ctx context.Context, args []string) error {
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
		api, err := operatorClient(*url)
		if err != nil {
			return err
		}
		agent, err := api.CreateAgent(ctx, *id, *charter, runtime)
		return printJSON(agent, err)
	case "list":
		flags := flag.NewFlagSet("agent list", flag.ContinueOnError)
		url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "controller URL")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		api, err := operatorClient(*url)
		if err != nil {
			return err
		}
		agents, err := api.ListAgents(ctx)
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
		api, err := operatorClient(*url)
		if err != nil {
			return err
		}
		agent, err := api.GetAgent(ctx, flags.Arg(0))
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
		api, err := operatorClient(*url)
		if err != nil {
			return err
		}
		agent, err := api.SetState(ctx, flags.Arg(0), model.State(flags.Arg(1)))
		return printJSON(agent, err)
	default:
		return errors.New("agent requires create, list, get, or state")
	}
}

func runMessage(ctx context.Context, args []string) error {
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
	api, err := operatorClient(*url)
	if err != nil {
		return err
	}
	message, err := api.SendControlMessage(ctx, flags.Arg(0), *sender, body)
	return printJSON(message, err)
}

func runEvents(ctx context.Context, args []string) error {
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
	api, err := operatorClient(*url)
	if err != nil {
		return err
	}
	events, err := api.Events(ctx, id)
	return printJSON(map[string]any{"items": events}, err)
}

func operatorClient(url string) (*client.Client, error) {
	operatorToken, err := token.Load("POLIS_OPERATOR_TOKEN", os.Getenv("POLIS_OPERATOR_TOKEN_FILE"), "operator")
	if err != nil {
		return nil, err
	}
	return client.NewOperator(url, operatorToken), nil
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
	return `Polisctl is the operator control CLI for a Polis fleet.

Usage:
  polisctl agent create --charter TEXT --runtime '["command","arg"]' [--id ID]
  polisctl agent list
  polisctl agent get ID
  polisctl agent state ID active|paused|terminated
  polisctl message ID JSON
  polisctl events [ID]
`
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
