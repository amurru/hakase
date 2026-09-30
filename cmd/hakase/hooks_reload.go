// hooks_reload.go - SIGHUP hooks reload for long-running processes.
//
// External `hakase hooks ...` edits (a separate process) cannot call
// ReloadUserHooks in-process, so running servers pick them up via SIGHUP:
// re-read the hooks block from disk and reload the runner. In-process
// edits (web API, TUI) reload directly and never need the signal.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	hakaseagent "amurru/hakase/internal/agent"
)

// installHooksReloadOnHup reloads the hooks runner on SIGHUP without
// disturbing the process: the signal only refreshes hook config, it never
// shuts anything down. A broken config fails loudly to stderr and keeps
// the old set (Runner.Reload is fail-closed).
func installHooksReloadOnHup() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		for range ch {
			if err := hakaseagent.ReloadUserHooksFromDisk(); err != nil {
				fmt.Fprintf(os.Stderr, "hakase: hooks reload failed: %v (keeping old set)\n", err)
			} else {
				fmt.Fprintln(os.Stderr, "hakase: hooks reloaded")
			}
		}
	}()
}
