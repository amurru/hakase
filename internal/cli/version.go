package cli

import (
	"flag"
	"fmt"
	"runtime"
)

// Build metadata, injected at link time by the Makefile:
//
//	go build -ldflags "-X amurru/hakase/internal/cli.Version=v0.1.0-alpha.1 \
//	    -X amurru/hakase/internal/cli.Commit=69e922d \
//	    -X amurru/hakase/internal/cli.Date=2026-08-23T00:00:00Z"
//
// Defaults describe an ad-hoc build from a working tree (plain `go build`),
// where the version falls back to "dev".
var (
	// Version is the release version, typically `git describe --tags` output
	// (e.g. "v0.1.0-alpha.1" or "v0.1.0-alpha.1-3-g69e922d").
	Version = "dev"
	// Commit is the short git SHA of the built revision.
	Commit = "unknown"
	// Date is the UTC build timestamp (RFC 3339).
	Date = "unknown"
)

// RunVersionCLI implements `hakase version`. It prints build metadata so
// testers can report exactly what they are running.
func RunVersionCLI(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	short := fs.Bool("short", false, "print only the version string")
	if err := fs.Parse(args); err != nil {
		return 2 // usage error
	}

	if *short {
		fmt.Println(Version)
		return 0
	}

	fmt.Printf("hakase %s\n", Version)
	fmt.Printf("  commit: %s\n", Commit)
	fmt.Printf("  built:  %s\n", Date)
	fmt.Printf("  go:     %s (%s/%s)\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return 0
}

// registerVersion wires the version command into the dispatcher.
func init() {
	registerCommand(Command{
		Name:        "version",
		Description: "print build version information",
		Handler:     RunVersionCLI,
	})
}
