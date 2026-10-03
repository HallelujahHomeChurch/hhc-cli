package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelpIsConciseAndDoesNotStartOperations(t *testing.T) {
	for _, args := range [][]string{nil, {"-h"}, {"--help"}, {"help"}} {
		var out, diagnostics bytes.Buffer
		code := Run(context.Background(), args, nil, &out, &diagnostics, "test")
		if code != 0 || diagnostics.Len() != 0 || !strings.Contains(out.String(), "recordings upload") || strings.Count(out.String(), "\n") > 16 || strings.Contains(out.String(), "--client-id") {
			t.Fatalf("root help %v: %d %s %s", args, code, &out, &diagnostics)
		}
	}
	for _, command := range [][]string{
		{"auth"}, {"auth", "login"}, {"auth", "status"}, {"auth", "logout"},
		{"recordings"}, {"recordings", "upload"}, {"recordings", "prepare"},
		{"recordings", "resume"}, {"recordings", "publish"}, {"recordings", "get"},
		{"update"}, {"install"}, {"version"},
	} {
		for _, flag := range []string{"-h", "--help"} {
			args := append(append([]string(nil), command...), flag)
			var out, diagnostics bytes.Buffer
			code := Run(context.Background(), args, nil, &out, &diagnostics, "test")
			if code != 0 || diagnostics.Len() != 0 || !strings.Contains(out.String(), "hhc "+strings.Join(command, " ")) {
				t.Fatalf("command help %v: %d %s %s", args, code, &out, &diagnostics)
			}
		}
	}
}

func TestHelpDoesNotHideUnknownCommandsOrEchoArguments(t *testing.T) {
	for _, args := range [][]string{{"unknown", "-h"}, {"recordings", "unknown", "--help"}} {
		var out, diagnostics bytes.Buffer
		if Run(context.Background(), args, nil, &out, &diagnostics, "test") != 2 {
			t.Fatalf("unknown command accepted: %v", args)
		}
	}
	var out, diagnostics bytes.Buffer
	if Run(context.Background(), []string{"recordings", "upload", "private-source", "-h"}, nil, &out, &diagnostics, "test") != 0 || strings.Contains(out.String()+diagnostics.String(), "private-source") {
		t.Fatalf("help parsed or echoed source: %s %s", &out, &diagnostics)
	}
}
