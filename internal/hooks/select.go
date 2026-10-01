// select.go - shared hook-selection helpers for the CLI, web API, and TUI
// (spec HK-112). Name matching (exact, then substring) precedes
// fingerprint-prefix matching, so short human names keep working while
// full fingerprints stay unambiguous.
package hooks

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// MatchSelector resolves sel against snapshots: exact name first, then
// name substring, then fingerprint prefix. Empty selection matches
// nothing (never "everything" — bulk ops say --all explicitly).
func MatchSelector(snaps []Snapshot, sel string) []Snapshot {
	if strings.TrimSpace(sel) == "" {
		return nil
	}
	var exact, sub, fp []Snapshot
	norm := func(s string) string {
		return strings.ToLower(strings.TrimPrefix(strings.ToLower(s), "sha256:"))
	}
	selFP := norm(sel)
	for _, s := range snaps {
		switch {
		case s.Name != "" && s.Name == sel:
			exact = append(exact, s)
		case s.Name != "" && strings.Contains(s.Name, sel):
			sub = append(sub, s)
		case strings.HasPrefix(norm(s.Fingerprint), selFP):
			fp = append(fp, s)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	if len(sub) > 0 {
		return sub
	}
	return fp
}

// MatchFingerprints resolves each prefix against user-layer snapshots,
// failing the whole set on the first zero/ambiguous match (the trust
// API's all-or-nothing discipline: never guess).
func MatchFingerprints(snaps []Snapshot, prefixes []string) ([]Snapshot, error) {
	var out []Snapshot
	for _, p := range prefixes {
		var matches []Snapshot
		for _, s := range snaps {
			if strings.HasPrefix(s.Fingerprint, p) {
				matches = append(matches, s)
			}
		}
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("no hook matches %q", p)
		case 1:
			out = append(out, matches[0])
		default:
			return nil, fmt.Errorf("%q is ambiguous (%d matches); use a longer prefix", p, len(matches))
		}
	}
	return out, nil
}

// LookupUserHandler maps a user-layer snapshot back to its runnable
// definition (exact fingerprint within the snapshot's event groups).
func LookupUserHandler(c *Config, event, fp string) (Handler, bool) {
	groups, err := groupsOf(c, event)
	if err != nil {
		return Handler{}, false
	}
	for _, g := range *groups {
		for _, h := range g.Hooks {
			if h.Fingerprint() == fp {
				return h, true
			}
		}
	}
	return Handler{}, false
}

// ScriptPreview returns the first lines of a local hook script (2KB,
// 15-line cap), or "" when argv[0] is not a readable file. Binaries are
// suppressed: dumping ELF bytes would bury the actual command (already
// visible in argv) under garbage.
func ScriptPreview(argv []string) string {
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
	for i := range lines {
		lines[i] = "    | " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// AddArgs is the parsed form of `hooks add` (CLI and TUI share the
// syntax so muscle memory transfers).
type AddArgs struct {
	Event     string
	Matcher   string
	Name      string
	Timeout   int
	OnFailure string
	Argv      []string
}

// ParseAddArgs parses `<Event> [--matcher R] [--name N] [--timeout S]
// [--on-failure allow|block] -- <argv...>`. Flags precede the `--`
// separator; everything after is the argv verbatim.
func ParseAddArgs(args []string) (AddArgs, error) {
	var out AddArgs
	if len(args) == 0 {
		return out, fmt.Errorf("usage: add <Event> [--matcher R] [--name N] [--timeout S] [--on-failure allow|block] -- <command...>")
	}
	out.Event = args[0]
	rest := args[1:]
	i := 0
	for ; i < len(rest); i++ {
		a := rest[i]
		if a == "--" {
			break
		}
		next := func() (string, error) {
			if i+1 >= len(rest) {
				return "", fmt.Errorf("flag %s needs a value", a)
			}
			i++
			return rest[i], nil
		}
		var err error
		switch a {
		case "--matcher":
			if out.Matcher, err = next(); err != nil {
				return out, err
			}
		case "--name":
			if out.Name, err = next(); err != nil {
				return out, err
			}
		case "--timeout":
			var v string
			if v, err = next(); err != nil {
				return out, err
			}
			if out.Timeout, err = strconv.Atoi(v); err != nil || out.Timeout < 0 {
				return out, fmt.Errorf("bad --timeout value (want non-negative seconds)")
			}
		case "--on-failure":
			if out.OnFailure, err = next(); err != nil {
				return out, err
			}
		default:
			return out, fmt.Errorf("unknown add flag %q", a)
		}
	}
	if i >= len(rest) || rest[i] != "--" {
		return out, fmt.Errorf("add needs `--` before the command argv")
	}
	out.Argv = rest[i+1:]
	if len(out.Argv) == 0 {
		return out, fmt.Errorf("add needs a non-empty command argv after `--`")
	}
	return out, nil
}

// ParseUpdateArgs parses `<prefix> [--matcher R] [--name N] [--timeout S]
// [--on-failure X] [--enable|--disable] [-- <argv...>]`. Absent flags
// leave fields alone; `--` replaces the argv wholesale.
func ParseUpdateArgs(args []string) (string, HookUpdate, error) {
	var upd HookUpdate
	if len(args) == 0 {
		return "", upd, fmt.Errorf("usage: update <prefix> [--matcher R] [--name N] [--timeout S] [--on-failure X] [--enable|--disable] [-- <command...>]")
	}
	prefix := args[0]
	rest := args[1:]
	i := 0
	for ; i < len(rest); i++ {
		a := rest[i]
		if a == "--" {
			break
		}
		next := func() (string, error) {
			if i+1 >= len(rest) {
				return "", fmt.Errorf("flag %s needs a value", a)
			}
			i++
			return rest[i], nil
		}
		var v string
		var err error
		switch a {
		case "--matcher":
			if v, err = next(); err != nil {
				return "", upd, err
			}
			upd.Matcher = &v
		case "--name":
			if v, err = next(); err != nil {
				return "", upd, err
			}
			upd.Name = &v
		case "--timeout":
			if v, err = next(); err != nil {
				return "", upd, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return "", upd, fmt.Errorf("bad --timeout value (want non-negative seconds)")
			}
			upd.Timeout = &n
		case "--on-failure":
			if v, err = next(); err != nil {
				return "", upd, err
			}
			upd.OnFailure = &v
		case "--enable", "--disable":
			b := a == "--enable"
			upd.Enabled = &b
		default:
			return "", upd, fmt.Errorf("unknown update flag %q", a)
		}
	}
	if i < len(rest) {
		upd.Command = rest[i+1:]
		if len(upd.Command) == 0 {
			return "", upd, fmt.Errorf("update needs a non-empty command argv after `--`")
		}
	}
	return prefix, upd, nil
}
