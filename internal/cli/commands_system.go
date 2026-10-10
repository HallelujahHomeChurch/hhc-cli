package cli

import (
	"context"
	"runtime"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/bundle"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/update"
)

func runInstall(ctx context.Context, args []string, c *commandContext) int {
	c.result.Command = "install"
	fs := c.flags()
	var directory string
	fs.StringVar(&directory, "directory", "", "")
	if err := c.parse(fs, args); err != nil {
		return c.finish(err)
	}
	var err error
	c.result.Data, err = update.Bootstrap(ctx, directory, c.version)
	return c.finish(err)
}
func runUpdate(ctx context.Context, args []string, c *commandContext) int {
	c.result.Command = "update"
	fs := c.flags()
	var check bool
	fs.BoolVar(&check, "check", false, "")
	if err := c.parse(fs, args); err != nil {
		return c.finish(err)
	}
	var err error
	c.result.Data, err = update.Execute(ctx, c.version, check)
	return c.finish(err)
}
func runVersion(ctx context.Context, args []string, c *commandContext) int {
	r := &c.result
	r.Command = "version"
	fs := c.flags()
	var selfCheck bool
	fs.BoolVar(&selfCheck, "self-check", false, "")
	if err := c.parse(fs, args); err != nil {
		return c.finish(err)
	}
	unlock, err := lockInstallation()
	if err != nil {
		return c.finish(err)
	}
	defer unlock()
	bundleVersion := ""
	if selfCheck {
		tools, err := bundle.Verify()
		if err != nil {
			return c.finish(err)
		}
		bundleVersion = tools.Version
	}
	r.Data = struct {
		Version                 string `json:"version"`
		Platform                string `json:"platform"`
		JournalSchema           int    `json:"journalSchema"`
		SupportedJournalSchemas []int  `json:"supportedJournalSchemas"`
		SkillVersion            string `json:"skillVersion"`
		BundleVersion           string `json:"bundleVersion,omitempty"`
	}{c.version, runtime.GOOS + "/" + runtime.GOARCH, 1, []int{1, 2}, c.version, bundleVersion}
	return c.finish(nil)
}
