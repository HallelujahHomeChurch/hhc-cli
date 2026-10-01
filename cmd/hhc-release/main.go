// hhc-release runs only in the repository release workflow, not on member PCs.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "expected package or sign")
		os.Exit(2)
	}
	f := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	version := f.String("version", "", "stable release version")
	directory := f.String("directory", "", "artifact output directory")
	bundle := f.String("bundle", "", "verified native media bundle directory")
	f.Parse(os.Args[2:])
	var err error
	switch {
	case f.NArg() != 0:
		err = fmt.Errorf("unexpected arguments")
	case os.Args[1] == "package":
		err = packageRelease(context.Background(), *directory, *bundle, *version, os.Getenv("HHC_RELEASE_PUBLIC_KEY"))
	case os.Args[1] == "sign":
		err = signRelease(*directory, *version, os.Getenv("HHC_RELEASE_SIGNING_KEY"), os.Getenv("HHC_RELEASE_PUBLIC_KEY"))
	default:
		err = fmt.Errorf("unknown release command")
	}
	if err != nil {
		// Signing configuration is never echoed, including malformed input.
		fmt.Fprintln(os.Stderr, "release operation failed; no release was published")
		os.Exit(1)
	}
}
