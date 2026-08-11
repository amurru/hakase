// rule.go - the `hakase rules` CLI: list and show project context files
// (AGENTS.md, with a project-scoped CLAUDE.md fallback).
//
// Subcommands parse with flag.ContinueOnError and return an int exit code
// (0 = success/help, 1 = runtime failure, 2 = usage error).
package cli

import (
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/config"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// runRulesCLI dispatches the `hakase rules` subcommand tree.
func RunRulesCLI(args []string) int {
	if len(args) == 0 {
		rulesCLIUsage()
		return 2
	}
	switch args[0] {
	case "list":
		return RunRulesList(args[1:])
	case "show":
		return RunRulesShow(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown rules subcommand %q\n\n", args[0])
		rulesCLIUsage()
		return 2
	}
}

// rulesCLIUsage prints the top-level `hakase rules` usage to stderr.
func rulesCLIUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase rules <subcommand>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list    list the project context files (AGENTS.md) that would be loaded")
	fmt.Fprintln(os.Stderr, "  show    show the content of one active context file (path or basename)")
}

// loadRulesConfig loads config.json for context-related fields. A config
// error degrades to a zero config so discovery still runs with defaults; the
// command always succeeds unless the filesystem itself fails.
func loadRulesConfig() *config.Config {
	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		return &config.Config{}
	}
	return cfg
}

// rulesScope classifies a context file for listing: project, user, or url.
func rulesScope(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return "url"
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, filepath.Join(home, ".hakase")) {
		return "user"
	}
	return "project"
}

// runRulesList prints the context files that would be loaded for the current
// directory, in render order, with scope and truncation status.
func RunRulesList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "list takes no arguments\n\n")
		fs.Usage()
		return 2
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine current directory: %v\n", err)
		return 1
	}
	cfg := loadRulesConfig()
	files := hctx.DiscoveredInstructionFiles(cwd, cfg, nil)

	if len(files) == 0 && strings.TrimSpace(cfg.Instruction) == "" {
		fmt.Println("No project context files found (no AGENTS.md above this directory and no ~/.hakase/AGENTS.md).")
		return 0
	}

	maxChars := hctx.EffectiveMaxChars(cfg)
	fmt.Println("Project context files (render order):")
	for _, f := range files {
		status := ""
		if len(f.Content) > maxChars {
			status = fmt.Sprintf(" (truncated to %d chars)", maxChars)
		}
		fmt.Printf("  %-8s %s (%d chars%s)\n", rulesScope(f.Path), f.Path, len(f.Content), status)
	}
	if inst := strings.TrimSpace(cfg.Instruction); inst != "" {
		fmt.Printf("  %-8s config.json \"instruction\" (%d chars)\n", "config", len(inst))
	}
	return 0
}

// runRulesShow prints the content of one active context file, matched by
// absolute path or basename. Content is shown with the same truncation cap
// that applies to the system prompt block.
func RunRulesShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "show takes exactly one argument (a context file path or basename)\n\n")
		fs.Usage()
		return 2
	}
	target := fs.Arg(0)

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine current directory: %v\n", err)
		return 1
	}
	cfg := loadRulesConfig()
	files := hctx.DiscoveredInstructionFiles(cwd, cfg, nil)

	var match *hctx.InstructionFile
	for i := range files {
		f := &files[i]
		if f.Path == target || filepath.Base(f.Path) == target {
			match = f
			break
		}
	}
	if match == nil {
		fmt.Fprintf(os.Stderr, "no active context file matches %q (see 'hakase rules list')\n", target)
		return 1
	}

	fmt.Printf("Instructions from: %s\n\n", match.Path)
	fmt.Print(hctx.TruncateContextFile(match.Content, hctx.EffectiveMaxChars(cfg)))
	if len(match.Content) > hctx.EffectiveMaxChars(cfg) {
		fmt.Printf("\n\n[file is %d chars; shown truncated to %d]\n", len(match.Content), hctx.EffectiveMaxChars(cfg))
	}
	fmt.Println()
	return 0
}
