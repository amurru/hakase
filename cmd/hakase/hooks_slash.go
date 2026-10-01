// hooks_slash.go - the /hooks slash command for the TUI: list, trust,
// and full user-layer CRUD over tool-lifecycle hooks. Reads render the
// user block plus the cwd project file; trust keeps the CLI's explicit-
// accept discipline (review first, `--yes` to record); user-layer edits
// reload the live runner in-process, no restart.
//
// Usage: /hooks [list] | trust <prefix> [--yes] | untrust <prefix> |
// enable|disable <prefix> | rm <prefix> [--yes] | on|off |
// add <Event> [flags] -- <cmd...> | update <prefix> [flags] |
// test <name|prefix>
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/hooks"
	"amurru/hakase/internal/project"
	"amurru/hakase/internal/tui"

	tea "charm.land/bubbletea/v2"
)

func runHooksCommand(m *tui.AppModel, args string) tea.Cmd {
	return runHooksCommandWithLog(func(s string) { m.AppendLog(s) }, args)
}

// runHooksCommandWithLog is the testable core: all output goes through
// log, so cmd tests drive dispatch without a TUI model.
func runHooksCommandWithLog(log func(string), args string) tea.Cmd {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return hooksListCmd(log)
	}
	sub := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(args, sub))
	switch sub {
	case "list", "ls", "status":
		return hooksListCmd(log)
	case "trust":
		return hooksTrustCmd(log, strings.Fields(rest))
	case "untrust":
		return hooksUntrustCmd(log, strings.Fields(rest))
	case "enable", "disable":
		return hooksSetEnabledCmd(log, sub == "enable", strings.Fields(rest))
	case "rm", "remove":
		return hooksRemoveCmd(log, strings.Fields(rest))
	case "on", "off":
		if rest != "" {
			log(fmt.Sprintf("usage: /hooks %s (no arguments)", sub))
			return nil
		}
		return hooksMasterCmd(log, sub == "on")
	case "add":
		return hooksAddCmd(log, splitArgs(rest))
	case "update", "edit":
		return hooksUpdateCmd(log, splitArgs(rest))
	case "test":
		return hooksTestCmd(log, strings.Fields(rest))
	case "help":
		log("/hooks [list] | trust <prefix> [--yes] | untrust <prefix> | enable|disable <prefix> | rm <prefix> [--yes] | on|off | add <Event> [flags] -- <cmd...> | update <prefix> [flags] | test <name|prefix>")
		return nil
	default:
		log(fmt.Sprintf("unknown /hooks subcommand %q (try /hooks help)", sub))
		return nil
	}
}

// splitArgs splits rest on whitespace (TUI input is already tokenized by
// the user; quoted argv entries are not supported — use the web UI form
// or CLI for commands with spaces).
func splitArgs(rest string) []string {
	return strings.Fields(rest)
}

// tuiHooksState loads the disk config plus a throwaway runner for reads.
// Mutations go through hooks.WriteUserHooks + in-process reload; the live
// agent runner picks trust-store edits up via its mtime cache.
type tuiHooksState struct {
	cfg     *config.Config
	runner  *hooks.Runner
	cfgPath string
	root    string
}

func loadTUIHooks() (*tuiHooksState, error) {
	cfgPath := config.ResolveConfigPath("config.json")
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("cannot load config: %v", err)
	}
	r, err := hooks.NewRunner(cfg.Hooks)
	if err != nil {
		return nil, fmt.Errorf("invalid hooks block: %v", err)
	}
	r.SetTrustStore(hooks.OpenDefaultTrustStore())
	cwd, _ := os.Getwd()
	return &tuiHooksState{cfg: cfg, runner: r, cfgPath: cfgPath, root: project.FindRoot(cwd)}, nil
}

func hooksListCmd(log func(string)) tea.Cmd {
	cwd, _ := os.Getwd()
	lines, err := hooksTUIReport(config.ResolveConfigPath("config.json"), project.FindRoot(cwd))
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	for _, l := range lines {
		log(l)
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
	if !s.Enabled {
		status += " [disabled]"
	}
	return fmt.Sprintf("%s %q %s [%s]%s", s.Event, matcher, name, strings.Join(s.Command, " "), status)
}

// pendingProjectHook resolves one untrusted project hook for the TUI's
// root by fingerprint prefix.
func pendingProjectHook(st *tuiHooksState, prefix string) (hooks.Snapshot, error) {
	if st.root == "" {
		return hooks.Snapshot{}, fmt.Errorf("no project root (TUI cwd is outside a git checkout)")
	}
	var pending []hooks.Snapshot
	for _, s := range st.runner.ProjectSnapshots(st.root) {
		if !s.Trusted {
			pending = append(pending, s)
		}
	}
	if len(pending) == 0 {
		return hooks.Snapshot{}, fmt.Errorf("no pending project hooks")
	}
	matched, err := hooks.MatchFingerprints(pending, []string{prefix})
	if err != nil {
		return hooks.Snapshot{}, err
	}
	return matched[0], nil
}

// printTrustReview logs everything a trust decision needs: full argv plus
// script preview (shared helper — a review that hides the command body is
// a consent bug).
func printTrustReview(log func(string), s hooks.Snapshot) {
	log(fmt.Sprintf("- %s matcher=%q name=%q", s.Event, s.Matcher, s.Name))
	log(fmt.Sprintf("  argv: %s", strings.Join(s.Command, " ")))
	log(fmt.Sprintf("  timeout=%ds on_failure=%s", s.Timeout, s.OnFailure))
	log(fmt.Sprintf("  fingerprint: %s", s.Fingerprint))
	if preview := hooks.ScriptPreview(s.Command); preview != "" {
		log("  script preview:")
		for _, l := range strings.Split(preview, "\n") {
			log(l)
		}
	}
}

func hooksTrustCmd(log func(string), args []string) tea.Cmd {
	var prefix string
	yes := false
	for _, a := range args {
		if a == "--yes" {
			yes = true
		} else if prefix == "" {
			prefix = a
		} else {
			log("usage: /hooks trust <fingerprint-prefix> [--yes]")
			return nil
		}
	}
	if prefix == "" {
		log("usage: /hooks trust <fingerprint-prefix> [--yes]")
		return nil
	}
	st, err := loadTUIHooks()
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	snap, err := pendingProjectHook(st, prefix)
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	if !yes {
		printTrustReview(log, snap)
		log(fmt.Sprintf("re-run with --yes to trust %s", snap.Fingerprint))
		return nil
	}
	store := hooks.OpenDefaultTrustStore()
	if store.Path() == "" {
		log("hooks: no trust store (no hakase home resolves; set HAKASE_HOME)")
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := store.Trust(hooks.TrustEntry{
		Fingerprint: snap.Fingerprint, Name: snap.Name, Event: snap.Event,
		Matcher: snap.Matcher, Command: snap.Command, FirstSeen: now,
	}); err != nil {
		log(fmt.Sprintf("hooks: trust failed: %v", err))
		return nil
	}
	// No reload call: the live runner's store handle re-reads behind its
	// mtime cache, so the grant lands on the next tool call.
	log(fmt.Sprintf("trusted %s (takes effect on the next tool call)", snap.Fingerprint))
	return nil
}

func hooksUntrustCmd(log func(string), args []string) tea.Cmd {
	if len(args) != 1 {
		log("usage: /hooks untrust <fingerprint-prefix>")
		return nil
	}
	store := hooks.OpenDefaultTrustStore()
	if store.Path() == "" {
		log("hooks: no trust store (no hakase home resolves; set HAKASE_HOME)")
		return nil
	}
	var full string
	for _, e := range store.List() {
		if strings.HasPrefix(e.Fingerprint, args[0]) {
			if full != "" {
				log(fmt.Sprintf("%q is ambiguous; use a longer prefix", args[0]))
				return nil
			}
			full = e.Fingerprint
		}
	}
	if full == "" {
		log(fmt.Sprintf("no trusted hook matches %q", args[0]))
		return nil
	}
	if _, err := store.Untrust(full); err != nil {
		log(fmt.Sprintf("hooks: untrust failed: %v", err))
		return nil
	}
	log(fmt.Sprintf("untrusted %s (takes effect on the next tool call)", full))
	return nil
}

// mutateAndReload applies a user-layer mutation to disk and reloads the
// live runner in-process (TUI shares the agent process — no signal
// needed). The refreshed user layer echoes back for the log.
func mutateAndReload(log func(string), cfgPath string, mutate func(*hooks.Config) error, done string) {
	block, err := hooks.WriteUserHooks(cfgPath, mutate)
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return
	}
	if err := hakaseagent.ReloadUserHooks(block); err != nil {
		log(fmt.Sprintf("hooks: saved, but live reload failed: %v", err))
		return
	}
	log(done)
}

func hooksSetEnabledCmd(log func(string), enable bool, args []string) tea.Cmd {
	if len(args) != 1 {
		log("usage: /hooks enable|disable <fingerprint-prefix>")
		return nil
	}
	st, err := loadTUIHooks()
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	matched, err := hooks.MatchFingerprints(st.runner.Snapshots(), args)
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	snap := matched[0]
	verb := "disabled"
	if enable {
		verb = "enabled"
	}
	mutateAndReload(log, st.cfgPath, func(c *hooks.Config) error {
		_, err := hooks.SetUserHookEnabled(c, snap.Fingerprint, enable)
		return err
	}, fmt.Sprintf("%s hook %q (trust unaffected, live now)", verb, snap.Name))
	return nil
}

func hooksRemoveCmd(log func(string), args []string) tea.Cmd {
	var prefix string
	yes := false
	for _, a := range args {
		if a == "--yes" {
			yes = true
		} else if prefix == "" {
			prefix = a
		} else {
			log("usage: /hooks rm <fingerprint-prefix> [--yes]")
			return nil
		}
	}
	if prefix == "" {
		log("usage: /hooks rm <fingerprint-prefix> [--yes]")
		return nil
	}
	st, err := loadTUIHooks()
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	matched, err := hooks.MatchFingerprints(st.runner.Snapshots(), []string{prefix})
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	snap := matched[0]
	if !yes {
		log(fmt.Sprintf("would remove %s %q [%s]", snap.Event, snap.Name, strings.Join(snap.Command, " ")))
		log("re-run with --yes to confirm removal")
		return nil
	}
	mutateAndReload(log, st.cfgPath, func(c *hooks.Config) error {
		_, err := hooks.RemoveUserHook(c, snap.Fingerprint)
		return err
	}, fmt.Sprintf("removed %s hook %q (live now)", snap.Event, snap.Name))
	return nil
}

func hooksMasterCmd(log func(string), enable bool) tea.Cmd {
	st, err := loadTUIHooks()
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	verb := "off"
	if enable {
		verb = "on"
	}
	mutateAndReload(log, st.cfgPath, func(c *hooks.Config) error {
		hooks.SetMasterEnabled(c, enable)
		return nil
	}, fmt.Sprintf("hooks %s (live now)", verb))
	return nil
}

func hooksAddCmd(log func(string), args []string) tea.Cmd {
	parsed, err := hooks.ParseAddArgs(args)
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	st, err := loadTUIHooks()
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	mutateAndReload(log, st.cfgPath, func(c *hooks.Config) error {
		return hooks.AddUserHook(c, parsed.Event, parsed.Matcher, hooks.Handler{
			Name: parsed.Name, Command: parsed.Argv, Timeout: parsed.Timeout, OnFailure: parsed.OnFailure,
		})
	}, fmt.Sprintf("added %s hook %q (live now)", parsed.Event, parsed.Name))
	return nil
}

func hooksUpdateCmd(log func(string), args []string) tea.Cmd {
	prefix, upd, err := hooks.ParseUpdateArgs(args)
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	st, err := loadTUIHooks()
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	mutateAndReload(log, st.cfgPath, func(c *hooks.Config) error {
		_, err := hooks.UpdateUserHook(c, prefix, upd)
		return err
	}, "hook updated (live now)")
	return nil
}

func hooksTestCmd(log func(string), args []string) tea.Cmd {
	if len(args) != 1 {
		log("usage: /hooks test <hook-name|fingerprint-prefix>")
		return nil
	}
	st, err := loadTUIHooks()
	if err != nil {
		log(fmt.Sprintf("hooks: %v", err))
		return nil
	}
	cands := st.runner.Snapshots()
	if st.root != "" {
		cands = append(cands, st.runner.ProjectSnapshots(st.root)...)
	}
	matched := hooks.MatchSelector(cands, args[0])
	if len(matched) == 0 {
		log(fmt.Sprintf("no hook matches %q", args[0]))
		return nil
	}
	if len(matched) > 1 {
		log(fmt.Sprintf("%q is ambiguous (%d matches):", args[0], len(matched)))
		for _, s := range matched {
			log(fmt.Sprintf("  [%s] %s %q %s", s.Layer, s.Event, s.Name, s.Fingerprint))
		}
		return nil
	}
	snap := matched[0]
	var h hooks.Handler
	if snap.Layer == "user" {
		var ok bool
		h, ok = hooks.LookupUserHandler(&st.cfg.Hooks, snap.Event, snap.Fingerprint)
		if !ok {
			log(fmt.Sprintf("cannot resolve hook %q", args[0]))
			return nil
		}
	} else {
		f, ferr := hooks.LoadProjectFile(st.root)
		if ferr != nil || f == nil {
			log(fmt.Sprintf("cannot resolve hook %q", args[0]))
			return nil
		}
		groups := map[string][]hooks.Group{
			hooks.EventPreToolUse: f.PreToolUse, hooks.EventPostToolUse: f.PostToolUse,
			hooks.EventSessionStart: f.SessionStart, hooks.EventUserPromptSubmit: f.UserPromptSubmit,
		}[snap.Event]
		found := false
		for _, g := range groups {
			for _, cand := range g.Hooks {
				if cand.Fingerprint() == snap.Fingerprint {
					h, found = cand, true
				}
			}
		}
		if !found {
			log(fmt.Sprintf("cannot resolve hook %q", args[0]))
			return nil
		}
		if !snap.Trusted {
			log("WARNING: this project hook is UNTRUSTED — dry-running executes its command.")
		}
	}
	res := hooks.DryRun(snap.Event, h)
	log(fmt.Sprintf("hook [%s] %s %q: exit %d", snap.Layer, snap.Event, snap.Name, res.ExitCode))
	switch {
	case res.TimedOut:
		log("verdict: ERROR (timeout)")
	case res.HookErr != "":
		log(fmt.Sprintf("verdict: ERROR (%s)", res.HookErr))
	case res.Blocked:
		log(fmt.Sprintf("verdict: BLOCK (%s)", res.Reason))
	default:
		log("verdict: ALLOW")
		if res.Context != "" {
			log(fmt.Sprintf("context: %s", res.Context))
		}
	}
	return nil
}
