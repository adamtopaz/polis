package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/adamtopaz/polis/internal/api"
	"github.com/adamtopaz/polis/internal/store"
	"github.com/adamtopaz/polis/internal/token"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "polis-mailbox:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("polis-mailbox", flag.ContinueOnError)
	listen := flags.String("listen", env("POLIS_LISTEN", ":8080"), "HTTP listen address")
	dbPath := flags.String("db", env("POLIS_DB_PATH", "./polis.db"), "database path")
	operatorTokenFile := flags.String("operator-token-file", env("POLIS_OPERATOR_TOKEN_FILE", ""), "path to the operator token")
	workerTokenFile := flags.String("worker-token-file", env("POLIS_WORKER_TOKEN_FILE", ""), "path to the worker token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("polis-mailbox takes no positional arguments")
	}
	operatorToken, err := token.Load("POLIS_OPERATOR_TOKEN", *operatorTokenFile, "operator")
	if err != nil {
		return err
	}
	workerToken, err := token.Load("POLIS_WORKER_TOKEN", *workerTokenFile, "worker")
	if err != nil {
		return err
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
		Handler:           api.New(database, logger, operatorToken, workerToken).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	result := make(chan error, 1)
	go func() {
		logger.Info("mailbox listening", "address", *listen, "database", *dbPath)
		result <- server.ListenAndServe()
	}()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		return fmt.Errorf("shut down HTTP server: %w", err)
	}
	if err := <-result; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
