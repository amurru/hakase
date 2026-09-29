// hooks.go - the `hakase hooks` management CLI. Read-only in v1: it dumps
// the loaded, validated user-scope hooks with their content fingerprints
// (docs/hooks/spec.md HK-005). Trust management (trust/untrust) arrives
// with project-scope hooks in Phase 2.
//
//	hakase hooks list   - show configured PreToolUse/PostToolUse hooks
package cli

import (
	"fmt"
	"os"
	"strings"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/hooks"
)

// RunHooksCLI implements the hooks subcommand.
func RunHooksCLI(args []string) int {
	if len(args) == 0 {
		hooksUsage()
		return 2
	}
	if args[0] == "list" {
		if len(args) > 1 {
			fmt.Fprintf(os.Stderr, "hakase: unexpected hooks list argument %q\n\n", args[1])
			hooksUsage()
			return 2
		}
		return runHooksList()
	}
	fmt.Fprintf(os.Stderr, "hakase: unknown hooks subcommand %q\n\n", args[0])
	hooksUsage()
	return 2
}

func hooksUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase hooks <subcommand> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list                show configured hooks and their fingerprints")
}

func runHooksList() int {
	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: cannot load config: %v\n", err)
		return 1
	}
	r, err := hooks.NewRunner(cfg.Hooks)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: invalid hooks block: %v\n", err)
		return 1
	}
	state := "enabled"
	if !config.HooksEnabled(cfg) {
		state = "disabled (hooks.enabled:false)"
	}
	snaps := r.Snapshots()
	var pre, post int
	for _, s := range snaps {
		if s.Event == hooks.EventPreToolUse {
			pre++
		} else {
			post++
		}
	}
	fmt.Printf("hooks: %s (%d PreToolUse, %d PostToolUse)\n", state, pre, post)
	if len(snaps) == 0 {
		fmt.Println("no hooks configured")
		return 0
	}
	for _, s := range snaps {
		matcher := s.Matcher
		if matcher == "" {
			matcher = "*"
		}
		name := s.Name
		if name == "" {
			name = "-"
		}
		fmt.Printf("  %s %q %s [%s] timeout=%ds on_failure=%s %s\n",
			s.Event, matcher, name, strings.Join(s.Command, " "),
			s.Timeout, s.OnFailure, s.Fingerprint)
	}
	return 0
}
