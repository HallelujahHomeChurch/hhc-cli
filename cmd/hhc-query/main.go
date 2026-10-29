// hhc-query is a media-free Linux service entrypoint in the CLI repository.
package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/mcp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryruntime"
)

var version = "dev"

func main() { os.Exit(run()) }
func run() int {
	fs := flag.NewFlagSet("hhc-query", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	listen := fs.String("listen", "127.0.0.1:8080", "HTTP listener behind approved TLS ingress")
	if fs.Parse(os.Args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	broker, err := queryruntime.NewBroker(os.Getenv("HHC_QUERY_BROKER_SOCKET"))
	hosts := split(os.Getenv("HHC_QUERY_HOSTS"))
	if err != nil || len(hosts) == 0 {
		slog.Error("query configuration unavailable")
		return 2
	}
	handler := mcp.NewServer(broker, hosts, split(os.Getenv("HHC_QUERY_ORIGINS")), version)
	handler.Audit = func(a mcp.Audit) {
		slog.Info("query", "callerHash", a.CallerHash, "operation", a.Operation, "outcome", a.Outcome, "durationMs", a.DurationMS)
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384, ErrorLog: slog.NewLogLogger(slog.NewTextHandler(os.Stderr, nil), slog.LevelError)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		handler.Drain()
		shutdown, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			server.Close()
			slog.Error("query drain incomplete")
			return 1
		}
		return 0
	case err := <-done:
		if err != http.ErrServerClosed {
			slog.Error("query listener failed")
			return 1
		}
		return 0
	}
}
func split(raw string) []string {
	out := []string{}
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
