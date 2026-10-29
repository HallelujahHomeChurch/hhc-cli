// hhc-query-admin is an explicit provisioning-owner utility, never an MCP tool.
// It creates no provider credential or OAuth grant. Executing it against a real
// environment requires separate action-time authorization and an owner DB role.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/querybroker"
	"io"
	"os"
	"time"
)

func main() { os.Exit(run()) }
func run() int {
	fs := flag.NewFlagSet("hhc-query-admin", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operation := fs.String("operation", "", "")
	if fs.Parse(os.Args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	switch *operation {
	case "bind-key", "import-grant", "set-policy", "set-quota", "revoke-caller":
	default:
		return 2
	}
	config, err := querybroker.LoadConfig(os.Getenv("HHC_QUERY_CONFIG"))
	if err != nil {
		return 2
	}
	secrets, err := querybroker.LoadRuntimeSecrets()
	if err != nil {
		return 2
	}
	master, err := base64.StdEncoding.DecodeString(secrets.MasterKey)
	if err != nil {
		return 2
	}
	defer clear(master)
	vault, err := querybroker.NewVault(master, config.ResourceURL)
	if err != nil {
		return 2
	}
	db, err := querybroker.OpenDatabase(secrets.DatabaseURL)
	if err != nil {
		return 2
	}
	defer db.Close()
	store := &querybroker.PostgreSQL{DB: db, Vault: vault}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if *operation != "bind-key" {
		if store.Ready(ctx) != nil {
			return 1
		}
	}
	if *operation == "bind-key" {
		err = store.BindVault(ctx)
	} else {
		if *operation == "import-grant" {
			info, err := os.Stdin.Stat()
			if err != nil || info.Mode()&os.ModeNamedPipe == 0 || info.Mode().Perm()&0077 != 0 {
				return 2
			}
		}
		raw, readErr := io.ReadAll(io.LimitReader(os.Stdin, 32769))
		if readErr != nil || len(raw) > 32768 || !json.Valid(raw) {
			return 2
		}
		defer clear(raw)
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		switch *operation {
		case "import-grant":
			var g querybroker.Grant
			if decoder.Decode(&g) != nil {
				return 2
			}
			err = store.ImportGrant(ctx, g)
		case "set-policy":
			var in struct {
				Policy           querybroker.Policy `json:"policy"`
				ExpectedRevision int64              `json:"expectedRevision"`
			}
			if decoder.Decode(&in) != nil {
				return 2
			}
			err = store.SetPolicy(ctx, in.Policy, in.ExpectedRevision)
		case "set-quota":
			var in struct {
				QuotaID       string `json:"quotaId"`
				Bucket        string `json:"bucket"`
				PeriodSeconds int    `json:"periodSeconds"`
				Maximum       int64  `json:"maximum"`
			}
			if decoder.Decode(&in) != nil {
				return 2
			}
			err = store.SetQuota(ctx, in.QuotaID, in.Bucket, in.PeriodSeconds, in.Maximum)
		case "revoke-caller":
			var in struct {
				CallerID string `json:"callerId"`
			}
			if decoder.Decode(&in) != nil {
				return 2
			}
			err = store.RevokeCaller(ctx, in.CallerID)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return 2
		}
	}
	if err != nil {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "error": "owner_operation_failed"})
		return 1
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "operation": *operation})
	return 0
}
