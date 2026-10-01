package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Keep the shipped agent examples executable against the real parser without
// opening credentials, local media, or network connections.
func TestSkillCommandExamples(t *testing.T) {
	doc, err := os.ReadFile("../../skills/hhc/references/commands.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := strings.Split(string(doc), "```json\n")
	if len(blocks) < 5 {
		t.Fatal("missing command examples")
	}
	for _, block := range blocks[1:] {
		var argv []string
		if err := json.Unmarshal([]byte(strings.SplitN(block, "```", 2)[0]), &argv); err != nil {
			t.Fatal(err)
		}
		if len(argv) < 2 || argv[0] != "hhc" {
			t.Fatal("invalid executable")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var out, diagnostics bytes.Buffer
		exit := Run(ctx, argv[1:], nil, &out, &diagnostics, "test")
		var value result
		if err := json.Unmarshal(out.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if argv[1] == "version" {
			if exit != 0 || !value.OK {
				t.Fatalf("version example: %s", out.String())
			}
		} else if exit != 130 || value.Error == nil || value.Error.Code != "cancelled" {
			t.Fatalf("example does not pass parser safely: %v: %s", argv, out.String())
		}
	}
}
