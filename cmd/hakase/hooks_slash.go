// hooks_slash.go - the /hooks slash command for the TUI: a read-only
// browser over the user hooks block and the project hooks file, with
// per-hook trust status. Trust changes stay in the CLI (`hakase hooks
// trust`), where the review UI and y/N confirm live.
package main

import (
	"fmt"
	"os"
	"strings"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/hooks"
	"amurru/hakase/internal/project"
	"amurru/hakase/internal/tui"

	tea "charm.land/bubbletea/v2"
)

func runHooksCommand(m *tui.AppModel, args string) tea.Cmd {
	if strings.TrimSpace(args) != "" {
		m.AppendLog("/hooks takes no arguments (trust changes via `hakase hooks trust`)")
		return nil
	}
	cwd, _ := os.Getwd()
	lines, err := hooksTUIReport(config.ResolveConfigPath("config.json"), project.FindRoot(cwd))
	if err != nil {
		m.AppendLog(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	for _, l := range lines {
		m.AppendLog(l)
	}
	return nil
}

// hooksTUIReport renders the read-only hooks browser: user layer plus the
// project layer for root. Pure (no AppModel) so cmd tests can drive it.
func hooksTUIReport(configPath, root string) ([]string, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("cannot load config: %v", err)
	}
	r, err := hooks.NewRunner(cfg.Hooks)
	if err != nil {
		return nil, fmt.Errorf("invalid hooks block: %v", err)
	}
	r.SetTrustStore(hooks.OpenDefaultTrustStore())

	state := "enabled"
	if !config.HooksEnabled(cfg) {
		state = "disabled (hooks.enabled:false)"
	}
	snaps := r.Snapshots()
	lines := []string{fmt.Sprintf("hooks: %s (%d user hooks)", state, len(snaps))}
	for _, s := range snaps {
		lines = append(lines, "  "+formatTUISnapshot(s))
	}
	if strings.TrimSpace(root) == "" {
		return append(lines, "project: no project root"), nil
	}
	path := hooks.ProjectHooksPath(root)
	if _, serr := os.Stat(path); serr != nil {
		return append(lines, fmt.Sprintf("project: no hooks file (%s)", path)), nil
	}
	psnaps := r.ProjectSnapshots(root)
	if psnaps == nil {
		return append(lines, fmt.Sprintf("project: ignoring broken hooks file %s", path)), nil
	}
	trusted := 0
	for _, s := range psnaps {
		if s.Trusted {
			trusted++
		}
	}
	lines = append(lines, fmt.Sprintf("project: %s (%d hooks, %d trusted)", path, len(psnaps), trusted))
	for _, s := range psnaps {
		lines = append(lines, "  "+formatTUISnapshot(s))
	}
	return lines, nil
}

func formatTUISnapshot(s hooks.Snapshot) string {
	matcher := s.Matcher
	if matcher == "" {
		matcher = "*"
	}
	name := s.Name
	if name == "" {
		name = "-"
	}
	status := ""
	if s.Layer == "project" {
		if s.Trusted {
			status = " [trusted]"
		} else {
			status = " [UNTRUSTED]"
		}
	}
	return fmt.Sprintf("%s %q %s [%s]%s", s.Event, matcher, name, strings.Join(s.Command, " "), status)
}
