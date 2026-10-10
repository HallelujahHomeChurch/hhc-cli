package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"golang.org/x/term"
)

func readSecretLine(reader io.Reader) ([]byte, error) {
	line, err := bufio.NewReaderSize(reader, 4098).ReadSlice('\n')
	if err != nil && err != io.EOF {
		clear(line)
		return nil, auth.ErrInvalidAuthInput
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) == 0 || len(line) > 4096 || !utf8.Valid(line) || bytes.ContainsAny(line, "\r\n\x00") {
		clear(line)
		return nil, auth.ErrInvalidAuthInput
	}
	return line, nil
}

func readSecret(ctx context.Context, input *os.File, diagnostics io.Writer, fromStdin bool) ([]byte, error) {
	if input == nil {
		return nil, auth.ErrInvalidAuthInput
	}
	fd := int(input.Fd())
	if fromStdin && term.IsTerminal(fd) {
		return nil, auth.ErrInvalidAuthInput
	}
	if !fromStdin {
		state, err := term.GetState(fd)
		if err != nil {
			return nil, auth.ErrInvalidAuthInput
		}
		defer term.Restore(fd, state)
		fmt.Fprint(diagnostics, "Service credential (hidden): ")
		defer fmt.Fprintln(diagnostics)
	}
	type secretResult struct {
		value []byte
		err   error
	}
	completed := make(chan secretResult)
	go func() {
		var value []byte
		var err error
		if fromStdin {
			value, err = readSecretLine(input)
		} else {
			value, err = term.ReadPassword(fd)
		}
		select {
		case completed <- secretResult{value, err}:
		case <-ctx.Done():
			clear(value)
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-completed:
		if result.err != nil {
			clear(result.value)
			return nil, auth.ErrInvalidAuthInput
		}
		return result.value, nil
	}
}
