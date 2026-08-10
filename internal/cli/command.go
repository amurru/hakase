// Package cli provides the command dispatch framework for the hakase binary.
//
// This is the skeleton phase (plan task 3): every command is registered with a
// stub handler so the framework compiles standalone with zero dependencies on
// the root package main. Real CLI implementations migrate into this package in
// a later phase (plan task 12) by swapping the stub handlers for the real
// `runXCLI(args []string) int` implementations; the Command.Handler field is
// the seam that makes that swap a one-line change.
//
// Exit code convention (mirrors the root package): 0 = success/help,
// 1 = runtime failure, 2 = usage error. The caller decides whether to exit.
package cli

import (
	"fmt"
	"os"
	"sort"
)

// Command is a single subcommand in the hakase CLI tree.
type Command struct {
	// Name is the subcommand token, e.g. "skill" (matches os.Args[1]).
	Name string
	// Description is a one-line help string shown by the usage listing.
	Description string
	// Handler runs the command with the args after the command name and
	// returns the process exit code (0 = success, 1 = runtime, 2 = usage).
	// Handlers are stubs until the real CLI implementations migrate in.
	Handler func(args []string) int
}

// commands is the global registry, keyed by command name.
var commands = make(map[string]*Command)

// registerCommand adds a command to the registry. A duplicate name replaces
// the previous entry, which lets a later phase re-register a command with its
// real handler without changing the dispatch code.
func registerCommand(cmd Command) {
	commands[cmd.Name] = &cmd
}

// Dispatch routes the first argument to its registered command. args is the
// full argument slice after the program name (i.e. os.Args[1:]).
//
// With no subcommand the dispatch falls through to the tui placeholder stub:
// the real TUI wiring lands in a later phase. An unknown subcommand prints the
// usage listing and returns 2. The returned int is the process exit code.
func Dispatch(args []string) int {
	if len(args) == 0 {
		return runTUIPlaceholder(nil)
	}

	name := args[0]
	switch name {
	case "-h", "-help", "--help", "help":
		printUsage()
		return 0
	}

	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "hakase: unknown command %q\n\n", name)
		printUsage()
		return 2
	}
	return cmd.Handler(args[1:])
}

// printUsage writes the command listing to stderr, sorted by name for stable
// output.
func printUsage() {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Fprintln(os.Stderr, "Usage: hakase <command> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  %-10s %s\n", name, commands[name].Description)
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Run 'hakase <command>' with no subcommand for command-specific help.")
}

// notMigrated returns a stub handler that reports the command still lives in
// the root package main and has not yet been wired into this dispatcher. Exit
// code 1 = runtime failure (command unavailable).
func notMigrated(name string) func(args []string) int {
	return func(args []string) int {
		fmt.Fprintf(os.Stderr,
			"hakase: '%s' is not yet migrated into the internal/cli dispatcher (plan task 12); "+
				"use the root package main binary for now\n", name)
		return 1
	}
}

// runTUIPlaceholder is the default handler when no subcommand is given. The
// real TUI wiring replaces this in a later phase (plan task 13).
func runTUIPlaceholder(args []string) int {
	fmt.Fprintln(os.Stderr,
		"hakase: interactive TUI is not wired into internal/cli yet (plan task 13). "+
			"Use the root package main binary (`go run .`) for the TUI, or pass a subcommand.")
	return 0
}

// init registers the command skeleton: the existing commands (which will get
// their real handlers in plan task 12) plus the new web, serve and auth
// commands, and the tui placeholder default.
func init() {
	registerCommand(Command{
		Name:        "skill",
		Description: "manage markdown skills (create, list, validate, evolve)",
		Handler:     notMigrated("skill"),
	})
	registerCommand(Command{
		Name:        "task",
		Description: "manage the task board (summary, list, new, update, done, ...)",
		Handler:     notMigrated("task"),
	})
	registerCommand(Command{
		Name:        "knowledge",
		Description: "manage the knowledge base (list, read, search, lint, create, link)",
		Handler:     notMigrated("knowledge"),
	})
	registerCommand(Command{
		Name:        "session",
		Description: "manage sessions (list, resume, ...)",
		Handler:     notMigrated("session"),
	})
	registerCommand(Command{
		Name:        "rules",
		Description: "list and show active project context files (AGENTS.md)",
		Handler:     notMigrated("rules"),
	})
	registerCommand(Command{
		Name:        "env",
		Description: "print the detected runtime environment block",
		Handler:     notMigrated("env"),
	})
	registerCommand(Command{
		Name:        "cron",
		Description: "manage scheduled tasks (list, status, pause, resume, run, tick)",
		Handler:     notMigrated("cron"),
	})
	registerCommand(Command{
		Name:        "web",
		Description: "serve the web UI",
		Handler:     notMigrated("web"),
	})
	registerCommand(Command{
		Name:        "serve",
		Description: "run the API-only server",
		Handler:     notMigrated("serve"),
	})
	registerCommand(Command{
		Name:        "auth",
		Description: "manage authentication (set-password)",
		Handler:     notMigrated("auth"),
	})
	registerCommand(Command{
		Name:        "tui",
		Description: "launch the interactive terminal UI (placeholder)",
		Handler:     runTUIPlaceholder,
	})
}
