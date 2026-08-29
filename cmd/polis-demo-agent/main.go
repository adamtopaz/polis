package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/adamtopaz/polis/internal/demo"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := demo.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "polis-demo-agent:", err)
		os.Exit(1)
	}
}
