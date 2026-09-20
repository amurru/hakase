// Package cli provides the command dispatch framework for the hakase binary.
//
// Every subcommand is registered with a handler; Dispatch routes os.Args[1]
// to the matching handler. Commands whose bootstrap would create an import
// cycle (web/serve/TUI need packages that import this package back, e.g.
// internal/web/handlers imports internal/cli) are registered here with a
// fallback handler and re-registered with their real implementations by
// package main at startup via RegisterCommand. The fallback therefore only
// runs when the dispatcher is used without the main binary wiring (e.g. in
// tests), never as a "not migrated" stub.
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
	Handler func(args []string) int
}

// commands is the global registry, keyed by command name.
var commands = make(map[string]*Command)

// registerCommand adds a command to the registry. A duplicate name replaces
// the previous entry, which lets package main re-register a command with its
// real handler without changing the dispatch code.
func registerCommand(cmd Command) {
	commands[cmd.Name] = &cmd
}

// RegisterCommand adds or replaces a command in the dispatcher. Package main
// uses it at startup to wire the real web/serve/TUI handlers, which cannot
// live in this package without creating an import cycle (their dependencies
// import this package back).
func RegisterCommand(name, description string, handler func(args []string) int) {
	registerCommand(Command{Name: name, Description: description, Handler: handler})
}

// Dispatch routes the first argument to its registered command. args is the
// full argument slice after the program name (i.e. os.Args[1:]).
//
// With no subcommand the dispatch falls through to the TUI handler (wired by
// package main; the in-package fallback explains that wiring is missing). An
// unknown subcommand prints the usage listing and returns 2. The returned int
// is the process exit code.
func Dispatch(args []string) int {
	if len(args) == 0 {
		// Bare invocation falls through to the TUI: the registered "tui"
		// command (the real interactive TUI once package main is wired, the
		// placeholder below when the dispatcher is used without that wiring,
		// e.g. in tests).
		return commands["tui"].Handler(nil)
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

// unregisteredExternal returns a fallback handler for commands whose real
// implementation is wired by package main via RegisterCommand (web, serve,
// tui). It only runs when the dispatcher is used without that wiring, e.g.
// in tests that call Dispatch directly. Exit code 1 = runtime failure
// (command unavailable in this context).
func unregisteredExternal(name string) func(args []string) int {
	return func(args []string) int {
		fmt.Fprintf(os.Stderr,
			"hakase: '%s' is wired by the main binary (package main registers the real handler "+
				"at startup via cli.RegisterCommand); it is not available in this context\n", name)
		return 1
	}
}

// runTUIPlaceholder is the fallback handler when no subcommand is given and
// package main has not wired the real TUI handler. The real binary launches
// the interactive TUI here.
func runTUIPlaceholder(args []string) int {
	fmt.Fprintln(os.Stderr,
		"hakase: interactive TUI is wired by the main binary (package main registers the real "+
			"handler at startup); no subcommand was given and no TUI handler is registered.")
	return 0
}

// init registers the command tree: the in-package commands with their real
// handlers, plus web, serve and tui with fallback handlers that package main
// replaces at startup with the real implementations.
func init() {
	registerCommand(Command{
		Name:        "skill",
		Description: "manage markdown skills (create, list, validate, evolve)",
		Handler:     RunSkillCLI,
	})
	registerCommand(Command{
		Name:        "task",
		Description: "manage the task board (summary, list, new, update, done, ...)",
		Handler:     RunTaskCLI,
	})
	registerCommand(Command{
		Name:        "knowledge",
		Description: "manage the knowledge base (list, read, search, lint, create, link)",
		Handler:     RunKnowledgeCLI,
	})
	registerCommand(Command{
		Name:        "session",
		Description: "manage sessions (list, resume, ...)",
		Handler:     RunSessionCLI,
	})
	registerCommand(Command{
		Name:        "rules",
		Description: "list and show active project context files (AGENTS.md)",
		Handler:     RunRulesCLI,
	})
	registerCommand(Command{
		Name:        "env",
		Description: "print the detected runtime environment block",
		Handler:     RunEnvCLI,
	})
	registerCommand(Command{
		Name:        "cron",
		Description: "manage scheduled tasks (list, status, pause, resume, run, tick)",
		Handler:     RunCronCLI,
	})
	registerCommand(Command{
		Name:        "projects",
		Description: "manage registered remote projects (list, register, sync, delete)",
		Handler:     RunProjectCLI,
	})
	registerCommand(Command{
		Name:        "channels",
		Description: "manage communication channels (status, pair-code, revoke)",
		Handler:     RunChannelsCLI,
	})
	registerCommand(Command{
		Name:        "sleep",
		Description: "offline skill self-improvement loop (harvest, review, run, adopt)",
		Handler:     RunSleepCLI,
	})
	registerCommand(Command{
		Name:        "mcp",
		Description: "expose hakase skills over MCP (serve)",
		Handler:     RunMCPCLI,
	})
	registerCommand(Command{
		Name:        "web",
		Description: "serve the web UI",
		Handler:     unregisteredExternal("web"),
	})
	registerCommand(Command{
		Name:        "serve",
		Description: "run the API-only server",
		Handler:     unregisteredExternal("serve"),
	})
	registerCommand(Command{
		Name:        "auth",
		Description: "manage authentication (set-password)",
		Handler:     RunAuthCLI,
	})
	registerCommand(Command{
		Name:        "tui",
		Description: "launch the interactive terminal UI",
		Handler:     runTUIPlaceholder,
	})
}
