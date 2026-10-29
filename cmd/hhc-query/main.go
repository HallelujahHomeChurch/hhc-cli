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
	"syscall"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/mcp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/querybroker"
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
	configPath := os.Getenv("HHC_QUERY_CONFIG")
	if configPath == "" {
		slog.Error("query configuration unavailable")
		return 2
	}
	config, err := querybroker.LoadConfig(configPath)
	if err != nil {
		slog.Error("query configuration unavailable")
		return 2
	}
	var broker *queryruntime.Broker
	if socket := os.Getenv("HHC_QUERY_BROKER_SOCKET"); socket != "" {
		broker, err = queryruntime.NewBroker(socket)
	} else {
		secrets, loadErr := querybroker.LoadRuntimeSecrets()
		if loadErr != nil {
			slog.Error("query secret injection unavailable")
			return 2
		}
		backend, db, composeErr := querybroker.Compose(config, secrets)
		if composeErr != nil {
			slog.Error("query identity configuration unavailable")
			return 2
		}
		defer db.Close()
		broker = queryruntime.NewEmbedded(backend)
	}
	if err != nil {
		slog.Error("query configuration unavailable")
		return 2
	}
	handler, err := mcp.NewServerWithOAuth(broker, config.Hosts, config.Origins, version, mcp.OAuthConfig{ResourceURL: config.ResourceURL, AuthorizationServers: []string{config.Issuer}, ScopesSupported: []string{config.RequiredScope}})
	if err != nil {
		slog.Error("query OAuth configuration unavailable")
		return 2
	}
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
