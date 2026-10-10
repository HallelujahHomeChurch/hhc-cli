package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandGroupsRejectOtherGroupsFlagsWithoutEchoing(t *testing.T) {
	for _, args := range [][]string{
		{"auth", "login", "--title", "private-value", "--json"},
		{"version", "--profile", "private-value", "--json"},
		{"update", "--scope", "private-value", "--json"},
		{"install", "--cover", "private-value", "--json"},
		{"recordings", "get", "00000000-0000-4000-8000-000000009907", "--check", "--json"},
		{"recordings", "upload", "private-value", "--client-id", "private-client", "--json"},
	} {
		t.Run(strings.Join(args[:2], "-"), func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			status := Run(context.Background(), args, nil, &output, &diagnostics, "test")
			var got result
			if status != 2 || json.Unmarshal(output.Bytes(), &got) != nil || got.Error == nil || got.Error.Code != "invalid_input" {
				t.Fatalf("status %d: %s %s", status, &output, &diagnostics)
			}
			if strings.Contains(output.String()+diagnostics.String(), "private-") {
				t.Fatal("argument leaked")
			}
		})
	}
}

func TestRegisteredCommandGroupsAppearInHelp(t *testing.T) {
	var output bytes.Buffer
	if Run(context.Background(), nil, nil, &output, &bytes.Buffer{}, "test") != 0 {
		t.Fatal("help failed")
	}
	if !strings.Contains(output.String(), "家教會軟體命令列工具") {
		t.Fatal("root is still recording-specific")
	}
	seen := map[string]bool{}
	for _, group := range commandGroups {
		if seen[group.name] || group.run == nil || !strings.Contains(output.String(), group.name) || commandHelp[group.name] == "" {
			t.Fatalf("incomplete command registration: %s", group.name)
		}
		seen[group.name] = true
	}
}
