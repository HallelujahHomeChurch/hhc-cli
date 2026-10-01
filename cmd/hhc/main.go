package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/cli"
)

var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	exit := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version)
	cancel()
	os.Exit(exit)
}
