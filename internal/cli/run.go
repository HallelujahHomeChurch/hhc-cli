package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/update"
)

// commandContext owns one invocation; command groups own their flags and services.
type commandContext struct {
	input               *os.File
	output, diagnostics io.Writer
	version             string
	jsonMode, noInput   bool
	requestedJSON       bool
	result              result
	progress            *progressDisplay
}

type commandGroup struct {
	name, summary string
	subcommands   bool
	run           func(context.Context, []string, *commandContext) int
}

var commandGroups = []commandGroup{
	{"auth", "帳號登入與身分管理", true, runAuth},
	{"recordings", "錄影準備、上傳與發布", true, runRecordings},
	{"install", "建立受管理安裝", false, runInstall},
	{"update", "更新工具", false, runUpdate},
	{"version", "查看版本", false, runVersion},
}

// Run never echoes parser errors, credentials or remote response bodies.
func Run(ctx context.Context, args []string, input *os.File, output, diagnostics io.Writer, version string) int {
	if showHelp(args, output) {
		return 0
	}
	c := &commandContext{input: input, output: output, diagnostics: diagnostics, version: version, jsonMode: slices.Contains(args, "--json") || slices.Contains(args, "--json=true"), result: result{SchemaVersion: 1}}
	c.requestedJSON = c.jsonMode
	defer func() {
		if c.progress != nil {
			c.progress.Close()
		}
	}()
	for _, group := range commandGroups {
		if args[0] == group.name {
			return group.run(ctx, args[1:], c)
		}
	}
	return c.finish(auth.ErrInvalidAuthInput)
}

func (c *commandContext) flags() *flag.FlagSet {
	fs := flag.NewFlagSet("hhc", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&c.jsonMode, "json", false, "")
	fs.BoolVar(&c.noInput, "no-input", false, "")
	return fs
}

func (c *commandContext) parse(fs *flag.FlagSet, args []string) error {
	requestedJSON := c.requestedJSON
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		c.jsonMode = c.jsonMode || requestedJSON
		return auth.ErrInvalidAuthInput
	}
	return nil
}

func lockInstallation() (func(), error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	lock, err := update.LockCommand(executable)
	if err != nil {
		return nil, err
	}
	return func() {
		if lock != nil {
			lock.Close()
		}
	}, nil
}

func profileDirectory() (string, error) {
	directory, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		directory, err = os.UserCacheDir()
	}
	if err != nil || !filepath.IsAbs(directory) {
		return "", auth.ErrCredentialStoreUnavailable
	}
	return directory, nil
}

func (c *commandContext) finish(err error) int {
	r := &c.result
	progress, jsonMode, output, diagnostics, version := c.progress, c.jsonMode, c.output, c.diagnostics, c.version
	if progress != nil {
		progress.Close()
	}
	exit := 0
	if err != nil {
		code, message, status, retryable := classify(err)
		r.Error = &commandError{Code: code, Message: message, Retryable: retryable, ResumeOperationID: r.OperationID}
		exit = status
	}
	r.OK = err == nil
	if jsonMode {
		if json.NewEncoder(output).Encode(r) != nil {
			return 1
		}
	} else if err != nil {
		fmt.Fprintln(diagnostics, r.Error.Message)
	} else if r.Command == "version" {
		fmt.Fprintln(output, "hhc", version, runtime.GOOS+"/"+runtime.GOARCH)
	} else {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		if encoder.Encode(r) != nil {
			return 1
		}
	}
	return exit
}
