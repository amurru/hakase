// hooks.go - the `hakase hooks` management CLI.
//
// User hooks are trusted by construction (own config); project hooks
// (<root>/.hakase/hooks.json) need per-hook content-hash trust before they
// execute (docs/hooks/spec.md HK-102/HK-105). This CLI is the trust UI:
// list shows both layers with trust status, trust reviews and records,
// untrust revokes, test dry-runs one handler with a sample payload.
//
//	hakase hooks list                     - show configured hooks, fingerprints, trust status
//	hakase hooks trust [--all] [--yes] [fingerprint-prefix...]
//	                                      - review pending project hooks (bare = list pending)
//	hakase hooks untrust <prefix>         - revoke trust
//	hakase hooks test <name|prefix>       - dry-run one handler, print the verdict
//	hakase hooks add <Event> [--matcher R] [--name N] [--timeout S]
//	                 [--on-failure allow|block] -- <command...>
//	                                      - add a user hook (takes effect via SIGHUP/restart)
//	hakase hooks rm <prefix>              - remove a user hook by fingerprint prefix
//	hakase hooks enable|disable <prefix>  - flip one user hook (never affects trust)
//	hakase hooks on|off                   - master hooks.enabled switch
package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/hooks"
)

// RunHooksCLI implements the hooks subcommand.
func RunHooksCLI(args []string) int {
	if len(args) == 0 {
		hooksUsage()
		return 2
	}
	switch args[0] {
	case "list":
		if len(args) > 1 {
			fmt.Fprintf(os.Stderr, "hakase: unexpected hooks list argument %q\n\n", args[1])
			hooksUsage()
			return 2
		}
		return runHooksList()
	case "trust":
		return runHooksTrust(args[1:])
	case "untrust":
		return runHooksUntrust(args[1:])
	case "test":
		return runHooksTest(args[1:])
	case "add":
		return runHooksAdd(args[1:])
	case "rm", "remove":
		return runHooksRemove(args[1:])
	case "enable", "disable":
		return runHooksSetEnabled(args[0], args[1:])
	case "on", "off":
		return runHooksMaster(args[0])
	default:
		fmt.Fprintf(os.Stderr, "hakase: unknown hooks subcommand %q\n\n", args[0])
		hooksUsage()
		return 2
	}
}

func hooksUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase hooks <subcommand> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list                            show configured hooks, fingerprints, trust status")
	fmt.Fprintln(os.Stderr, "  trust [--all] [--yes] [prefix]  review pending project hooks and trust them")
	fmt.Fprintln(os.Stderr, "  untrust <prefix>                revoke trust for a fingerprint prefix")
	fmt.Fprintln(os.Stderr, "  test <name|prefix>              dry-run one handler with a sample payload")
	fmt.Fprintln(os.Stderr, "  add <Event> [flags] -- <cmd..>  add a user hook (--matcher/--name/--timeout/--on-failure)")
	fmt.Fprintln(os.Stderr, "  rm <prefix>                     remove a user hook by fingerprint prefix")
	fmt.Fprintln(os.Stderr, "  enable|disable <prefix>         flip one user hook (never affects trust)")
	fmt.Fprintln(os.Stderr, "  on|off                          master hooks.enabled switch")
	fmt.Fprintln(os.Stderr, "  test <name|prefix>              dry-run one handler with a sample payload")
}

// loadHooksRunner loads the user config and builds the validated runner.
// Every subcommand funnels through here so a malformed hooks block fails
// the same actionable way everywhere.
func loadHooksRunner() (*config.Config, *hooks.Runner, error) {
	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("cannot load config: %v", err)
	}
	r, err := hooks.NewRunner(cfg.Hooks)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid hooks block: %v", err)
	}
	return cfg, r, nil
}

func runHooksList() int {
	cfg, r, err := loadHooksRunner()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: %v\n", err)
		return 1
	}
	// Trust status is per-hook content-hash: without the store every
	// project hook reads untrusted (fail-closed display, same as the
	// agent path).
	r.SetTrustStore(hooks.OpenDefaultTrustStore())
	state := "enabled"
	if !config.HooksEnabled(cfg) {
		state = "disabled (hooks.enabled:false)"
	}
	snaps := r.Snapshots()
	var pre, post, sess, prmpt int
	for _, s := range snaps {
		switch s.Event {
		case hooks.EventPreToolUse:
			pre++
		case hooks.EventPostToolUse:
			post++
		case hooks.EventSessionStart:
			sess++
		case hooks.EventUserPromptSubmit:
			prmpt++
		default:
			sess++
		}
	}
	fmt.Printf("hooks: %s (%d PreToolUse, %d PostToolUse, %d SessionStart, %d UserPromptSubmit)\n", state, pre, post, sess, prmpt)
	if len(snaps) == 0 {
		fmt.Println("user: no hooks configured")
	} else {
		fmt.Println("user:")
		for _, s := range snaps {
			fmt.Printf("  %s\n", formatSnapshot(s))
		}
	}
	printProjectSection(r)
	return 0
}

func formatSnapshot(s hooks.Snapshot) string {
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
			status = " [UNTRUSTED - skipped until trusted]"
		}
	}
	if !s.Enabled {
		status += " [disabled]"
	}
	return fmt.Sprintf("%s %q %s [%s] timeout=%ds on_failure=%s %s%s",
		s.Event, matcher, name, strings.Join(s.Command, " "),
		s.Timeout, s.OnFailure, s.Fingerprint, status)
}

// printProjectSection renders the project layer for the CLI's project root.
// Best-effort diagnostics: problems warn on stderr, the exit stays 0 (list
// is a reader, and the user layer above already printed).
func printProjectSection(r *hooks.Runner) {
	root := cliProjectRoot()
	if root == "" {
		fmt.Println("project: no project root (not under a git checkout)")
		return
	}
	path := hooks.ProjectHooksPath(root)
	if _, err := os.Stat(path); err != nil {
		fmt.Printf("project: no hooks file (%s)\n", path)
		return
	}
	snaps := r.ProjectSnapshots(root)
	if snaps == nil {
		if fi, err := os.Stat(path); err == nil && fi != nil {
			fmt.Fprintf(os.Stderr, "project: ignoring broken hooks file %s (`hakase hooks test` diagnoses it)\n", path)
		} else {
			fmt.Printf("project: layer disabled (%s present but ignored)\n", path)
		}
		return
	}
	trusted := 0
	for _, s := range snaps {
		if s.Trusted {
			trusted++
		}
	}
	fmt.Printf("project: %s (%d hooks, %d trusted)\n", path, len(snaps), trusted)
	for _, s := range snaps {
		fmt.Printf("  %s\n", formatSnapshot(s))
	}
}

// projectPending returns the untrusted project handlers for root with their
// snapshots, for the trust review UI.
func projectPending(r *hooks.Runner, root string) []hooks.Snapshot {
	var out []hooks.Snapshot
	for _, s := range r.ProjectSnapshots(root) {
		if !s.Trusted {
			out = append(out, s)
		}
	}
	return out
}

func runHooksTrust(args []string) int {
	var all, yes bool
	var selectors []string
	for _, a := range args {
		switch a {
		case "--all":
			all = true
		case "--yes":
			yes = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "hakase: unknown hooks trust flag %q\n\n", a)
				hooksUsage()
				return 2
			}
			selectors = append(selectors, a)
		}
	}
	_, r, err := loadHooksRunner()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: %v\n", err)
		return 1
	}
	r.SetTrustStore(hooks.OpenDefaultTrustStore())
	if cfg, cerr := config.LoadConfig(config.ResolveConfigPath("config.json")); cerr == nil && cfg.Hooks.Project.Enabled != nil && !*cfg.Hooks.Project.Enabled {
		fmt.Fprintln(os.Stderr, "warning: project layer disabled in config (hooks.project.enabled:false); new trust entries stay inert until it is re-enabled")
	}
	root := cliProjectRoot()
	if root == "" {
		fmt.Fprintln(os.Stderr, "hakase: no project root (not under a git checkout); nothing to trust")
		return 1
	}
	path := hooks.ProjectHooksPath(root)
	if _, err := os.Stat(path); err != nil {
		fmt.Printf("no project hooks file (%s); nothing to trust\n", path)
		return 0
	}
	pending := projectPending(r, root)
	if len(pending) == 0 {
		fmt.Println("no pending project hooks; everything is trusted")
		return 0
	}
	store := hooks.OpenDefaultTrustStore()
	if store.Path() == "" {
		fmt.Fprintln(os.Stderr, "hakase: no trust store (no hakase home resolves; set HAKASE_HOME)")
		return 1
	}

	// Bare `hooks trust` is the review UI: show pending, explain, stop.
	if !all && len(selectors) == 0 {
		fmt.Printf("%d pending project hook(s) in %s:\n", len(pending), path)
		for _, s := range pending {
			printTrustCandidate(s)
		}
		fmt.Println("run `hakase hooks trust --all` or `hakase hooks trust <fingerprint-prefix>` to trust after review")
		return 0
	}

	var targets []hooks.Snapshot
	if all {
		targets = pending
	} else {
		for _, sel := range selectors {
			matches := matchSnapshots(pending, sel)
			if len(matches) == 0 {
				fmt.Fprintf(os.Stderr, "hakase: no pending hook matches %q\n", sel)
				return 1
			}
			if len(matches) > 1 {
				fmt.Fprintf(os.Stderr, "hakase: %q is ambiguous (%d matches); use a longer prefix:\n", sel, len(matches))
				for _, m := range matches {
					fmt.Fprintf(os.Stderr, "  %s\n", m.Fingerprint)
				}
				return 1
			}
			targets = append(targets, matches[0])
		}
	}
	trusted := 0
	now := time.Now().UTC().Format(time.RFC3339)
	for _, s := range targets {
		printTrustCandidate(s)
		if !yes && !confirmPrompt(fmt.Sprintf("Trust hook %s?", shortFP(s.Fingerprint))) {
			fmt.Println("skipped")
			continue
		}
		entry := hooks.TrustEntry{
			Fingerprint: s.Fingerprint,
			Name:        s.Name,
			Event:       s.Event,
			Matcher:     s.Matcher,
			Command:     s.Command,
			FirstSeen:   now,
		}
		if err := store.Trust(entry); err != nil {
			fmt.Fprintf(os.Stderr, "hakase: trust failed: %v\n", err)
			return 1
		}
		fmt.Printf("trusted %s\n", s.Fingerprint)
		trusted++
	}
	fmt.Printf("trusted %d hook(s); they fire on the next tool call, no restart needed\n", trusted)
	return 0
}

// matchSnapshots matches a selector against fingerprint prefixes (with or
// without the "sha256:" scheme).
func matchSnapshots(snaps []hooks.Snapshot, sel string) []hooks.Snapshot {
	sel = strings.ToLower(strings.TrimPrefix(strings.ToLower(sel), "sha256:"))
	var out []hooks.Snapshot
	for _, s := range snaps {
		fp := strings.ToLower(strings.TrimPrefix(s.Fingerprint, "sha256:"))
		if strings.HasPrefix(fp, sel) {
			out = append(out, s)
		}
	}
	return out
}

func shortFP(fp string) string {
	hex := strings.TrimPrefix(fp, "sha256:")
	if len(hex) > 12 {
		hex = hex[:12]
	}
	return "sha256:" + hex
}

// printTrustCandidate renders everything a trust decision needs: the full
// argv (never a redacted summary — a dialog that hides `curl evil | sh`
// is a consent bug) plus a script-body preview when the entry point is a
// local file.
func printTrustCandidate(s hooks.Snapshot) {
	fmt.Printf("- %s matcher=%q name=%q\n  argv: %s\n  timeout=%ds on_failure=%s\n  fingerprint: %s\n",
		s.Event, s.Matcher, s.Name, strings.Join(s.Command, " "), s.Timeout, s.OnFailure, s.Fingerprint)
	if preview := scriptPreview(s.Command); preview != "" {
		fmt.Printf("  script preview:\n%s\n", preview)
	}
}

// scriptPreview returns the first lines of a local hook script (2KB cap),
// or "" when argv[0] is not a readable file. Binaries are suppressed:
// previewing the interpreter of a `sh -c` wrapper as ELF garbage would
// bury the actual inline script (already visible in argv).
func scriptPreview(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	data, err := os.ReadFile(argv[0])
	if err != nil {
		return ""
	}
	if len(data) > 2048 {
		data = data[:2048]
	}
	for _, b := range data {
		if b == 0 {
			return "    | [binary file, preview suppressed]"
		}
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 15 {
		lines = append(lines[:15], "… (truncated)")
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("    | ")
		b.WriteString(l)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// confirmPrompt asks y/N on stdout; EOF/non-tty counts as "no".
func confirmPrompt(question string) bool {
	fmt.Printf("%s [y/N] ", question)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return true
	}
	return false
}

func runHooksUntrust(args []string) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "Usage: hakase hooks untrust <fingerprint-prefix>")
		return 2
	}
	store := hooks.OpenDefaultTrustStore()
	if store.Path() == "" {
		fmt.Fprintln(os.Stderr, "hakase: no trust store (no hakase home resolves; set HAKASE_HOME)")
		return 1
	}
	sel := strings.ToLower(strings.TrimPrefix(strings.ToLower(args[0]), "sha256:"))
	var full string
	for _, e := range store.List() {
		if strings.HasPrefix(strings.ToLower(strings.TrimPrefix(e.Fingerprint, "sha256:")), sel) {
			if full != "" {
				fmt.Fprintf(os.Stderr, "hakase: %q is ambiguous; use a longer prefix\n", args[0])
				return 1
			}
			full = e.Fingerprint
		}
	}
	if full == "" {
		fmt.Fprintf(os.Stderr, "hakase: no trusted hook matches %q\n", args[0])
		return 1
	}
	removed, err := store.Untrust(full)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: untrust failed: %v\n", err)
		return 1
	}
	if !removed {
		fmt.Fprintf(os.Stderr, "hakase: no trusted hook matches %q\n", args[0])
		return 1
	}
	fmt.Printf("untrusted %s (takes effect on the next tool call)\n", full)
	return 0
}

func runHooksTest(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase hooks test <hook-name|fingerprint-prefix>")
		return 2
	}
	_, r, err := loadHooksRunner()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: %v\n", err)
		return 1
	}
	r.SetTrustStore(hooks.OpenDefaultTrustStore())
	sel := args[0]
	// Candidates across both layers: user snapshots plus project snapshots
	// for the CLI's project root.
	// Candidates across both layers: user snapshots plus project snapshots
	// for the CLI's project root.
	cands := r.Snapshots()
	root := cliProjectRoot()
	if root != "" {
		cands = append(cands, r.ProjectSnapshots(root)...)
	}
	matches := matchTestSelector(cands, sel)
	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "hakase: no hook matches %q\n", sel)
		return 1
	}
	if len(matches) > 1 {
		fmt.Fprintf(os.Stderr, "hakase: %q is ambiguous (%d matches):\n", sel, len(matches))
		for _, m := range matches {
			fmt.Fprintf(os.Stderr, "  [%s] %s %q %s\n", m.Layer, m.Event, m.Name, m.Fingerprint)
		}
		return 1
	}
	m := matches[0]
	// Resolve the runnable handler (name/matcher/event back to the
	// definition) and dry-run it.
	h, event := findHookHandler(r, root, m)
	if h == nil {
		fmt.Fprintf(os.Stderr, "hakase: cannot resolve hook %q\n", sel)
		return 1
	}
	if m.Layer == "project" && !m.Trusted {
		fmt.Println("WARNING: this project hook is UNTRUSTED — dry-running executes its command. Review the preview above first.")
	}
	res := hooks.DryRun(event, *h)
	fmt.Printf("hook [%s] %s %q\n", m.Layer, event, m.Name)
	fmt.Printf("command: %s\n", strings.Join(h.Command, " "))
	fmt.Printf("exit: %d (%.1fs)\n", res.ExitCode, float64(res.DurationMs)/1000)
	switch {
	case res.TimedOut:
		fmt.Println("verdict: ERROR (timeout)")
	case res.HookErr != "":
		fmt.Printf("verdict: ERROR (%s)\n", res.HookErr)
	case res.Blocked:
		fmt.Printf("verdict: BLOCK\nreason: %s\n", res.Reason)
	default:
		fmt.Println("verdict: ALLOW")
		if res.Context != "" {
			fmt.Printf("context:\n%s\n", res.Context)
		}
	}
	return 0
}

// matchTestSelector matches by exact name, then unique name substring, then
// fingerprint prefix.
func matchTestSelector(cands []hooks.Snapshot, sel string) []hooks.Snapshot {
	var exact, sub []hooks.Snapshot
	for _, c := range cands {
		if c.Name != "" && c.Name == sel {
			exact = append(exact, c)
		} else if c.Name != "" && strings.Contains(c.Name, sel) {
			sub = append(sub, c)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	if len(sub) > 0 {
		return sub
	}
	return matchSnapshots(cands, sel)
}

// findHookHandler maps a snapshot back to its runnable definition.
func findHookHandler(r *hooks.Runner, root string, m hooks.Snapshot) (*hooks.Handler, string) {
	matchFP := func(groups []hooks.Group) *hooks.Handler {
		for _, g := range groups {
			for i := range g.Hooks {
				if g.Hooks[i].Fingerprint() == m.Fingerprint {
					h := g.Hooks[i]
					return &h
				}
			}
		}
		return nil
	}
	if m.Layer == "project" && root != "" {
		if f, err := hooks.LoadProjectFile(root); err == nil && f != nil {
			var groups []hooks.Group
			switch m.Event {
			case hooks.EventPreToolUse:
				groups = f.PreToolUse
			case hooks.EventPostToolUse:
				groups = f.PostToolUse
			case hooks.EventSessionStart:
				groups = f.SessionStart
			default:
				groups = f.UserPromptSubmit
			}
			if h := matchFP(groups); h != nil {
				return h, m.Event
			}
		}
		return nil, ""
	}
	// User layer: re-read the loaded config's groups via a fresh runner is
	// wasteful; instead match against the runner's compiled view is
	// unavailable — so resolve through the config file directly.
	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		return nil, ""
	}
	var groups []hooks.Group
	switch m.Event {
	case hooks.EventPreToolUse:
		groups = cfg.Hooks.PreToolUse
	case hooks.EventPostToolUse:
		groups = cfg.Hooks.PostToolUse
	case hooks.EventSessionStart:
		groups = cfg.Hooks.SessionStart
	default:
		groups = cfg.Hooks.UserPromptSubmit
	}
	return matchFP(groups), m.Event
}

// sighupHint reminds that a running server (web/TUI, another process) only
// picks config edits up via SIGHUP; in-process surfaces reload directly.
const sighupHint = "note: running servers pick this up on SIGHUP (`pkill -HUP hakase`); otherwise it applies on restart"

// mutateUserHooks runs mutate against the resolved config file, prints the
// SIGHUP hint on success, and maps errors to exit 1.
func mutateUserHooks(mutate func(*hooks.Config) error) int {
	path := config.ResolveConfigPath("config.json")
	if _, err := hooks.WriteUserHooks(path, mutate); err != nil {
		fmt.Fprintf(os.Stderr, "hakase: %v\n", err)
		return 1
	}
	fmt.Println(sighupHint)
	return 0
}

// runHooksAdd implements `hooks add <Event> [flags] -- <command...>`.
// Flags precede the `--` separator; everything after is the argv verbatim
// (so commands starting with `-` need no escaping).
func runHooksAdd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "hakase: usage: hooks add <Event> [--matcher R] [--name N] [--timeout S] [--on-failure allow|block] -- <command...>")
		return 2
	}
	event := args[0]
	var matcher, name, onFailure string
	timeout := 0
	rest := args[1:]
	i := 0
	for ; i < len(rest); i++ {
		a := rest[i]
		if a == "--" {
			break
		}
		val := func() string {
			if i+1 >= len(rest) {
				return ""
			}
			i++
			return rest[i]
		}
		switch a {
		case "--matcher":
			matcher = val()
		case "--name":
			name = val()
		case "--timeout":
			n, err := strconv.Atoi(val())
			if err != nil || n < 0 {
				fmt.Fprintf(os.Stderr, "hakase: bad --timeout value (want non-negative seconds)\n")
				return 2
			}
			timeout = n
		case "--on-failure":
			onFailure = val()
		default:
			fmt.Fprintf(os.Stderr, "hakase: unknown hooks add flag %q\n", a)
			return 2
		}
	}
	if i >= len(rest) || rest[i] != "--" {
		fmt.Fprintln(os.Stderr, "hakase: hooks add needs `--` before the command argv")
		return 2
	}
	argv := rest[i+1:]
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "hakase: hooks add needs a non-empty command argv after `--`")
		return 2
	}
	h := hooks.Handler{Name: name, Command: argv, Timeout: timeout, OnFailure: onFailure}
	return mutateUserHooks(func(c *hooks.Config) error {
		if err := hooks.AddUserHook(c, event, matcher, h); err != nil {
			return err
		}
		fmt.Printf("added %s hook %q [%s]\n", event, name, strings.Join(argv, " "))
		return nil
	})
}

// runHooksRemove implements `hooks rm <prefix>`.
func runHooksRemove(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "hakase: usage: hooks rm <fingerprint-prefix>")
		return 2
	}
	return mutateUserHooks(func(c *hooks.Config) error {
		snap, err := hooks.RemoveUserHook(c, args[0])
		if err != nil {
			return err
		}
		fmt.Printf("removed %s hook %q [%s]\n", snap.Event, snap.Name, strings.Join(snap.Command, " "))
		return nil
	})
}

// runHooksSetEnabled implements `hooks enable|disable <prefix>`.
func runHooksSetEnabled(sub string, args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "hakase: usage: hooks %s <fingerprint-prefix>\n", sub)
		return 2
	}
	return mutateUserHooks(func(c *hooks.Config) error {
		snap, err := hooks.SetUserHookEnabled(c, args[0], sub == "enable")
		if err != nil {
			return err
		}
		fmt.Printf("%sd %s hook %q (trust unaffected)\n", sub, snap.Event, snap.Name)
		return nil
	})
}

// runHooksMaster implements `hooks on|off` (master hooks.enabled switch).
func runHooksMaster(sub string) int {
	return mutateUserHooks(func(c *hooks.Config) error {
		hooks.SetMasterEnabled(c, sub == "on")
		fmt.Printf("hooks %s\n", sub)
		return nil
	})
}
