package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/adamtopaz/polis/internal/client"
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
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage())
		return nil
	}
	switch args[0] {
	case "inspect", "messages", "ack", "send", "journal":
	default:
		return usageError()
	}

	api, token, positional, err := agentSession(args[0], args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "inspect":
		if len(positional) != 0 {
			return errors.New("inspect takes no positional arguments")
		}
		agent, err := api.Self(ctx, token)
		return printJSON(agent, err)
	case "messages":
		if len(positional) != 0 {
			return errors.New("messages takes no positional arguments")
		}
		messages, err := api.Messages(ctx, token)
		return printJSON(map[string]any{"items": messages}, err)
	case "ack":
		if len(positional) != 1 {
			return errors.New("ack requires a message id")
		}
		through, err := strconv.ParseUint(positional[0], 10, 64)
		if err != nil {
			return fmt.Errorf("parse message id: %w", err)
		}
		return printJSON(map[string]bool{"ok": true}, api.AckMessages(ctx, token, through))
	case "send":
		if len(positional) != 2 {
			return errors.New("send requires an agent id and a JSON body")
		}
		body, err := rawJSON(positional[1])
		if err != nil {
			return err
		}
		message, err := api.SendMessage(ctx, token, positional[0], body)
		return printJSON(message, err)
	case "journal":
		if len(positional) != 2 {
			return errors.New("journal requires an event kind and JSON data")
		}
		data, err := rawJSON(positional[1])
		if err != nil {
			return err
		}
		event, err := api.Journal(ctx, token, positional[0], data)
		return printJSON(event, err)
	default:
		return usageError()
	}
}

func agentSession(name string, args []string) (*client.Client, string, []string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "mailbox URL")
	if err := flags.Parse(args); err != nil {
		return nil, "", nil, err
	}
	token, err := agentToken()
	if err != nil {
		return nil, "", nil, err
	}
	return client.New(*url), token, flags.Args(), nil
}

func agentToken() (string, error) {
	token := strings.TrimSpace(os.Getenv("POLIS_AGENT_TOKEN"))
	if token == "" {
		return "", errors.New("agent token required: set POLIS_AGENT_TOKEN")
	}
	return token, nil
}

func rawJSON(value string) (json.RawMessage, error) {
	raw := json.RawMessage(value)
	if !json.Valid(raw) {
		return nil, errors.New("value must be valid JSON")
	}
	return raw, nil
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
	return `Polis is the capability CLI for a running autonomous agent.

Usage:
  polis inspect
  polis messages
  polis ack MESSAGE_ID
  polis send AGENT_ID JSON
  polis journal KIND JSON
`
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
