package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/mcp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

func TestVideoCLIAndMCPParityWithoutMediaOrCleanup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HHC_CLI_OPERATIONS_DIR", dir)
	sentinel := filepath.Join(dir, "original-recording")
	os.WriteFile(sentinel, []byte("preserve"), 0600)
	observed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	query := func(_ context.Context, tool string, in videos.Input) (videos.Result, error) {
		if tool != "search_videos" || in.Query != "聚會" || in.Source != "hhc" {
			t.Errorf("%s %+v", tool, in)
		}
		return videos.Result{Sources: []videos.SourceResult{{Source: "hhc", Page: videos.Page{Complete: true, Items: []videos.Video{{ID: "hhc:fixture", Title: "Fixture", ObservedAt: observed}}}}}, ObservedAt: observed}, nil
	}
	var out, diag bytes.Buffer
	c := &commandContext{output: &out, diagnostics: &diag, requestedJSON: true, result: result{SchemaVersion: 1}, videoQuery: query}
	if exit := runVideos(context.Background(), []string{"search", "--query", "聚會", "--source", "hhc", "--json"}, c); exit != 0 {
		t.Fatal(exit, diag.String(), out.String())
	}
	var cliResult struct {
		Data videos.Result `json:"data"`
	}
	if json.Unmarshal(out.Bytes(), &cliResult) != nil {
		t.Fatal(out.String())
	}
	request, _ := mcp.Decode([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_videos","arguments":{"query":"聚會","source":"hhc"}}}`))
	raw, _ := json.Marshal(mcp.Handle(context.Background(), request, query, "fixture"))
	var rpc struct {
		Result struct {
			StructuredContent videos.Result `json:"structuredContent"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &rpc) != nil {
		t.Fatal(string(raw))
	}
	a, _ := json.Marshal(cliResult.Data)
	b, _ := json.Marshal(rpc.Result.StructuredContent)
	if string(a) != string(b) {
		t.Fatalf("CLI/MCP diverged %s %s", a, b)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve" {
		t.Fatal("recording touched", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("query initialized recording work")
	}
}
func TestVideoHelpAndFlagIsolation(t *testing.T) {
	for _, args := range [][]string{{"videos", "-h"}, {"videos", "search", "-h"}, {"mcp", "serve", "-h"}, {"videos", "search", "--profile", "SECRET", "--json"}, {"videos", "compare", "bad-id", "--json"}, {"videos", "get", "https://bad", "--json"}, {"videos", "search", "--timeout", "31s", "--json"}} {
		var out, diag bytes.Buffer
		exit := Run(context.Background(), args, nil, &out, &diag, "fixture")
		if strings.Contains(out.String()+diag.String(), "SECRET") {
			t.Fatal("parser echoed input")
		}
		help := args[len(args)-1] == "-h"
		if help && exit != 0 || !help && exit != 2 {
			t.Fatalf("%v exit=%d %s", args, exit, out.String())
		}
	}
	if helpRequested([]string{"--query", "-h"}) {
		t.Fatal("keyword interpreted as help")
	}
}
