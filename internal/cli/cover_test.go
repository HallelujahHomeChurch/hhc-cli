package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCoverFlagLiteralPaths(t *testing.T) {
	for _, path := range []string{`C:\聚會 資料\封面.jpg`, `/tmp/聚會 封面.png`} {
		for _, prepare := range []bool{false, true} {
			args := []string{"recordings", "upload", path, "--title", "聚會", "--cover", path, "--operation-id", "11111111-1111-4111-8111-111111111111", "--profile", "uploader", "--json", "--no-input"}
			if prepare {
				args = append(args, "--prepare")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var out, diagnostics bytes.Buffer
			code := Run(ctx, args, nil, &out, &diagnostics, "test")
			var value result
			if err := json.Unmarshal(out.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			if code != 130 || value.Error.Code != "cancelled" {
				t.Fatalf("cover parser: %d %s", code, out.String())
			}
		}
	}
}

func TestCoverHelp(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if Run(context.Background(), []string{"recordings", "upload", "-h"}, nil, &out, &diagnostics, "test") != 0 || !strings.Contains(out.String(), "--cover") {
		t.Fatal("cover option missing from help")
	}
}

func TestCoverHelpTokenRemainsLiteralAndVersionCapabilities(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, diagnostics bytes.Buffer
	args := []string{"recordings", "upload", "source.mp4", "--title", "Fixture", "--cover", "-h", "--profile", "uploader", "--operation-id", "11111111-1111-4111-8111-111111111111", "--json", "--no-input"}
	if code := Run(ctx, args, nil, &out, &diagnostics, "test"); code != 130 {
		t.Fatalf("cover treated as help: %d %s", code, out.String())
	}
	out.Reset()
	if code := Run(context.Background(), []string{"version", "--json"}, nil, &out, &diagnostics, "test"); code != 0 {
		t.Fatal(code)
	}
	var value struct {
		Data struct {
			JournalSchema           int
			SupportedJournalSchemas []int
		}
	}
	if err := json.Unmarshal(out.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Data.JournalSchema != 1 || len(value.Data.SupportedJournalSchemas) != 2 || value.Data.SupportedJournalSchemas[1] != 2 {
		t.Fatal("release baseline or capability missing")
	}
}
