//go:build !darwin && !windows

package auth

import (
	"context"
)

func openLoginBrowser(context.Context, string) error { return ErrAuthUnavailable }
