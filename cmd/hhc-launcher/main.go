package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/update"
)

func main() {
	exe, err := os.Executable()
	exit := 5
	if err == nil {
		exit, err = update.Launch(filepath.Dir(exe), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	}
	if err != nil {
		code := "invalid_installation"
		retryable := false
		if errors.Is(err, operation.ErrOperationBusy) {
			code = "operation_busy"
			retryable = true
		}
		if slices.Contains(os.Args[1:], "--json") || slices.Contains(os.Args[1:], "--json=true") {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"schemaVersion": 1, "ok": false, "command": "launcher", "profile": nil, "principal": nil, "data": nil, "error": map[string]any{"code": code, "message": "HHC installation is unavailable or busy.", "retryable": retryable, "requestId": nil}})
		} else {
			fmt.Fprintln(os.Stderr, "HHC installation is unavailable or busy.")
		}
	}
	if exit < 0 {
		exit = 1
	}
	os.Exit(exit)
}
