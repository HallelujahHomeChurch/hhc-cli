package auth

import (
	"context"
	"os/exec"
)

func openLoginBrowser(ctx context.Context, target string) error {
	return exec.CommandContext(ctx, "/usr/bin/open", target).Run()
}
