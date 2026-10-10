package cli

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"golang.org/x/term"
)

func runAuth(ctx context.Context, args []string, c *commandContext) int {
	if len(args) < 1 || !slices.Contains([]string{"login", "status", "logout"}, args[0]) {
		return c.finish(auth.ErrInvalidAuthInput)
	}
	r := &c.result
	r.Command = "auth " + args[0]
	fs := c.flags()
	profile := "default"
	fs.StringVar(&profile, "profile", "default", "")
	var service, secretStdin bool
	var clientID, scope string
	if args[0] == "login" {
		fs.BoolVar(&service, "service-principal", false, "")
		fs.BoolVar(&secretStdin, "secret-stdin", false, "")
		fs.StringVar(&clientID, "client-id", "", "")
		fs.StringVar(&scope, "scope", "cms:recordings:read cms:recordings:write cms:recordings:publish", "")
	}
	if err := c.parse(fs, args[1:]); err != nil {
		return c.finish(err)
	}
	unlock, err := lockInstallation()
	if err != nil {
		return c.finish(err)
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return c.finish(err)
	}
	if !auth.ValidProfile(profile) {
		return c.finish(auth.ErrInvalidAuthInput)
	}
	r.Profile = &profile
	input, diagnostics, jsonMode, noInput := c.input, c.diagnostics, c.jsonMode, &c.noInput
	if r.Command == "auth login" && !service {
		if secretStdin || clientID != "" {
			return c.finish(auth.ErrInvalidAuthInput)
		}
		if *noInput || jsonMode || input == nil || !term.IsTerminal(int(input.Fd())) {
			return c.finish(auth.ErrAuthenticationRequired)
		}
	}
	if r.Command == "auth login" && service && (clientID == "" || (!secretStdin && (*noInput || jsonMode || input == nil || !term.IsTerminal(int(input.Fd()))))) {
		return c.finish(auth.ErrInvalidAuthInput)
	}
	directory, err := profileDirectory()
	if err != nil {
		return c.finish(err)
	}
	profiles := auth.NewProfiles(filepath.Join(directory, "HHC", "cli", "profiles"))
	var token auth.Token
	switch r.Command {
	case "auth status":
		token, err = profiles.Token(ctx, profile)
	case "auth logout":
		r.Data, err = profiles.Logout(ctx, profile)
		return c.finish(err)
	case "auth login":
		if service {
			var secret []byte
			secret, err = readSecret(ctx, input, diagnostics, secretStdin)
			if err != nil {
				return c.finish(err)
			}
			defer clear(secret)
			token, err = profiles.LoginService(ctx, profile, clientID, string(secret), strings.Fields(scope))
		} else {
			token, err = profiles.LoginHuman(ctx, profile, auth.HumanLoginOptions{Scopes: strings.Fields(scope)})
		}
	}
	if err == nil {
		principal := token.Principal()
		r.Principal = &principal
		r.Data = struct {
			Scopes          []string  `json:"scopes"`
			AccessExpiresAt time.Time `json:"accessExpiresAt"`
		}{strings.Fields(token.Scope()), token.ExpiresAt()}
	}
	return c.finish(err)
}
