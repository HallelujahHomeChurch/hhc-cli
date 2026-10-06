package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("newline-%q", newline), func(t *testing.T) {
			checkSkillExamples(t, strings.ReplaceAll(strings.ReplaceAll(string(doc), "\r\n", "\n"), "\n", newline))
		})
	}
}

func TestSkillProcessingProgressReference(t *testing.T) {
	doc, err := os.ReadFile("../../skills/hhc/references/commands.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"processing_progress", "lastProgressAt", "heartbeatAt", "local_validation", "Ctrl+C"} {
		if !strings.Contains(string(doc), field) {
			t.Errorf("progress reference missing %s", field)
		}
	}
}

func checkSkillExamples(t *testing.T, doc string) {
	t.Helper()
	blocks := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "```json\n")
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
