package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/cli"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

var version = "dev"

func main() {
	if handled, status := operation.HandleSupervisor(os.Args[1:]); handled {
		os.Exit(status)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	exit := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version)
	cancel()
	os.Exit(exit)
}
