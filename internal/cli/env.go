// env.go - the `hakase env` CLI: print the runtime-environment block that
// would be injected into agent instructions, without starting the agent.
//
// Mirrors the rule.go conventions: flag.ContinueOnError parsing and an int
// exit code (0 = success/help, 1 = runtime failure, 2 = usage error).
package cli

import (
	"amurru/hakase/internal/env"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/sandbox"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// runEnvCLI dispatches the `hakase env` subcommand tree.
func RunEnvCLI(args []string) int {
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "env takes no arguments\n\n")
		fs.Usage()
		return 2
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine current directory: %v\n", err)
		return 1
	}
	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		cfg = &config.Config{}
	}
	if !config.SystemEnvEnabled(cfg) {
		fmt.Println("Runtime environment block is disabled (system_env.enabled = false).")
		return 0
	}

	// Resolve the sandbox mode so the rendered block carries the bubblewrap
	// exec note exactly as the running agent would.
	sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(cfg.Sandbox)

	info := env.DetectSystemInfo(cwd, nil)
	block := env.BuildEnvironmentReminder(info, config.SystemEnvMaxChars(cfg))
	if strings.TrimSpace(block) == "" {
		fmt.Println("No runtime environment facts detected.")
		return 0
	}
	fmt.Print(strings.TrimLeft(block, "\n"))
	fmt.Println()
	return 0
}
