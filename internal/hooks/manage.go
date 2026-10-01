// manage.go implements the user-layer hook CRUD core shared by the CLI,
// web API, and TUI (spec HK-111). All ops are pure Config mutations with
// fingerprint-prefix resolution; persistence goes through WriteUserHooks
// (map-surgery atomic write), and live processes pick the result up via
// Runner.Reload (in-process) or SIGHUP (external edits).
package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"amurru/hakase/internal/util"
)

// userEvents is the fixed resolution order for prefix matching.
var userEvents = []string{EventPreToolUse, EventPostToolUse, EventSessionStart, EventUserPromptSubmit}

// groupsOf returns the group slice for event, or an error for unknown
// events (fail loud: a typo must never silently target nothing).
func groupsOf(c *Config, event string) (*[]Group, error) {
	switch event {
	case EventPreToolUse:
		return &c.PreToolUse, nil
	case EventPostToolUse:
		return &c.PostToolUse, nil
	case EventSessionStart:
		return &c.SessionStart, nil
	case EventUserPromptSubmit:
		return &c.UserPromptSubmit, nil
	default:
		return nil, fmt.Errorf("unknown hooks event %q (want PreToolUse, PostToolUse, SessionStart, UserPromptSubmit)", event)
	}
}

// userRef identifies one user-layer handler by position.
type userRef struct {
	event string
	gi    int
	hi    int
}

// resolveUserHook finds the single user-layer handler for sel, using the
// shared MatchSelector discipline (exact name, then substring, then
// fingerprint prefix) over user-layer snapshots. Zero matches or
// ambiguous selectors are errors that name the fix: never guess which
// hook the operator meant. Names are safe to match here (unlike trust):
// the user layer is own-config, so there is no forgery surface.
func resolveUserHook(c *Config, sel string) (userRef, Handler, error) {
	if strings.TrimSpace(sel) == "" {
		return userRef{}, Handler{}, fmt.Errorf("hook selector must be non-empty (name or fingerprint prefix)")
	}
	type cand struct {
		ref  userRef
		snap Snapshot
	}
	var cands []cand
	for _, event := range userEvents {
		groups, _ := groupsOf(c, event)
		for gi := range *groups {
			for hi := range (*groups)[gi].Hooks {
				h := (*groups)[gi].Hooks[hi]
				cands = append(cands, cand{
					ref:  userRef{event: event, gi: gi, hi: hi},
					snap: Snapshot{Event: event, Name: h.Name, Fingerprint: h.Fingerprint()},
				})
			}
		}
	}
	snaps := make([]Snapshot, len(cands))
	for i, cd := range cands {
		snaps[i] = cd.snap
	}
	matched := MatchSelector(snaps, sel)
	if len(matched) == 0 {
		return userRef{}, Handler{}, fmt.Errorf("no user hook matches %q", sel)
	}
	if len(matched) > 1 {
		return userRef{}, Handler{}, fmt.Errorf("%q is ambiguous (%d matches); use a longer prefix", sel, len(matched))
	}
	// Reverse lookup keys on the full triple, not the fingerprint alone:
	// two handlers can share argv+script (same fingerprint) across events
	// or names, and the first-fingerprint-wins mapping would operate on
	// the wrong hook. A triple collision means fully identical handlers,
	// which MatchSelector already reports as ambiguous above.
	want := matched[0]
	for _, cd := range cands {
		if cd.snap.Event == want.Event && cd.snap.Name == want.Name && cd.snap.Fingerprint == want.Fingerprint {
			groups, _ := groupsOf(c, cd.ref.event)
			return cd.ref, (*groups)[cd.ref.gi].Hooks[cd.ref.hi], nil
		}
	}
	return userRef{}, Handler{}, fmt.Errorf("no user hook matches %q", sel)
}

// AddUserHook appends h to event's groups (new group when no trailing
// group shares the matcher) and validates the whole block, so a bad add
// fails before anything hits disk.
func AddUserHook(c *Config, event, matcher string, h Handler) error {
	groups, err := groupsOf(c, event)
	if err != nil {
		return err
	}
	h.Type = "command"
	if len(*groups) > 0 && (*groups)[len(*groups)-1].Matcher == matcher {
		g := &(*groups)[len(*groups)-1]
		g.Hooks = append(g.Hooks, h)
	} else {
		*groups = append(*groups, Group{Matcher: matcher, Hooks: []Handler{h}})
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		// Roll back the append: callers must never see a half-added hook.
		g := &(*groups)[len(*groups)-1]
		g.Hooks = g.Hooks[:len(g.Hooks)-1]
		if len(g.Hooks) == 0 && len(*groups) > 0 && &(*groups)[len(*groups)-1] == g {
			*groups = (*groups)[:len(*groups)-1]
		}
		return fmt.Errorf("invalid hook: %v", err)
	}
	return nil
}

// RemoveUserHook deletes the handler matching prefix and returns its
// snapshot for confirm/audit text. Empty groups are pruned.
func RemoveUserHook(c *Config, prefix string) (Snapshot, error) {
	ref, h, err := resolveUserHook(c, prefix)
	if err != nil {
		return Snapshot{}, err
	}
	groups, _ := groupsOf(c, ref.event)
	g := &(*groups)[ref.gi]
	snap := Snapshot{Event: ref.event, Matcher: g.Matcher, Name: h.Name, Command: h.Command, Timeout: h.Timeout, OnFailure: h.OnFailure, Fingerprint: h.Fingerprint(), Layer: "user", Trusted: true, Enabled: h.IsEnabled()}
	g.Hooks = append(g.Hooks[:ref.hi], g.Hooks[ref.hi+1:]...)
	if len(g.Hooks) == 0 {
		*groups = append((*groups)[:ref.gi], (*groups)[ref.gi+1:]...)
	}
	return snap, nil
}

// SetUserHookEnabled flips one handler's enabled flag (HK-109). Toggling
// never touches the fingerprint, so trust is unaffected by construction.
func SetUserHookEnabled(c *Config, prefix string, enabled bool) (Snapshot, error) {
	ref, _, err := resolveUserHook(c, prefix)
	if err != nil {
		return Snapshot{}, err
	}
	groups, _ := groupsOf(c, ref.event)
	h := &(*groups)[ref.gi].Hooks[ref.hi]
	v := enabled
	h.Enabled = &v
	g := (*groups)[ref.gi]
	return Snapshot{Event: ref.event, Matcher: g.Matcher, Name: h.Name, Command: h.Command, Timeout: h.Timeout, OnFailure: h.OnFailure, Fingerprint: h.Fingerprint(), Layer: "user", Trusted: true, Enabled: enabled}, nil
}

// HookUpdate carries optional handler-field updates; nil means unchanged.
// Command nil means unchanged (empty argv is rejected at Validate, so an
// explicit clear is an error, never a wipe).
type HookUpdate struct {
	Matcher   *string
	Name      *string
	Command   []string
	Timeout   *int
	OnFailure *string
	Enabled   *bool
}

// UpdateUserHook applies upd to the handler matching prefix. The target is
// resolved BEFORE mutation (fingerprints hash the old command), then the
// whole block revalidates; a failed validation rolls back to the original
// handler.
func UpdateUserHook(c *Config, prefix string, upd HookUpdate) (Snapshot, error) {
	ref, orig, err := resolveUserHook(c, prefix)
	if err != nil {
		return Snapshot{}, err
	}
	groups, _ := groupsOf(c, ref.event)
	g := &(*groups)[ref.gi]
	origMatcher := g.Matcher
	h := &g.Hooks[ref.hi]
	if upd.Matcher != nil {
		g.Matcher = *upd.Matcher
	}
	if upd.Name != nil {
		h.Name = *upd.Name
	}
	if upd.Command != nil {
		h.Command = upd.Command
	}
	if upd.Timeout != nil {
		h.Timeout = *upd.Timeout
	}
	if upd.OnFailure != nil {
		h.OnFailure = *upd.OnFailure
	}
	if upd.Enabled != nil {
		v := *upd.Enabled
		h.Enabled = &v
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		g.Matcher = origMatcher
		g.Hooks[ref.hi] = orig
		return Snapshot{}, fmt.Errorf("invalid hook update: %v", err)
	}
	return Snapshot{Event: ref.event, Matcher: g.Matcher, Name: h.Name, Command: h.Command, Timeout: h.Timeout, OnFailure: h.OnFailure, Fingerprint: h.Fingerprint(), Layer: "user", Trusted: true, Enabled: h.IsEnabled()}, nil
}

// SetMasterEnabled flips the top-level hooks.enabled switch.
func SetMasterEnabled(c *Config, enabled bool) {
	v := enabled
	c.Enabled = &v
}

// WriteUserHooks loads configPath's raw JSON, applies mutate to its hooks
// block, validates, and writes back atomically with only the "hooks" key
// replaced — unknown top-level keys survive (a typed round-trip would drop
// them). Key order normalizes to encoding/json's sorted order (the same
// property web PUT /api/config already has). Returns the validated new
// block for the caller to Reload.
func WriteUserHooks(configPath string, mutate func(*Config) error) (Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("cannot read config %s: %v", configPath, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("existing config %s is not valid JSON: %v", configPath, err)
	}
	var block Config
	if hb, ok := raw["hooks"]; ok && hb != nil {
		hbRaw, err := json.Marshal(hb)
		if err != nil {
			return Config{}, fmt.Errorf("cannot re-encode hooks block: %v", err)
		}
		if err := json.Unmarshal(hbRaw, &block); err != nil {
			return Config{}, fmt.Errorf("invalid hooks block: %v", err)
		}
	}
	if err := mutate(&block); err != nil {
		return Config{}, err
	}
	block.ApplyDefaults()
	if err := block.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid hooks block: %v", err)
	}
	out, err := json.Marshal(block)
	if err != nil {
		return Config{}, fmt.Errorf("cannot encode hooks block: %v", err)
	}
	var blockMap map[string]any
	if err := json.Unmarshal(out, &blockMap); err != nil {
		return Config{}, fmt.Errorf("cannot re-encode hooks block: %v", err)
	}
	raw["hooks"] = blockMap
	final, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return Config{}, fmt.Errorf("cannot encode config: %v", err)
	}
	final = append(final, '\n')
	return block, writeFileLocked(configPath, final)
}

// userHooksMu serializes in-process WriteUserHooks callers (two web
// requests, TUI + web); the adjacent .lock flock serializes against
// external `hakase hooks ...` processes. Same discipline as the trust
// store.
var userHooksMu sync.Mutex

// writeFileLocked atomically replaces configPath via a unique temp file
// under an exclusive flock. The temp file inherits the existing file's
// mode (config.json holds API keys and tokens — a fixed 0644 would flip
// a 0600 file world-readable); missing files default to 0600.
func writeFileLocked(configPath string, data []byte) error {
	userHooksMu.Lock()
	defer userHooksMu.Unlock()
	dir := filepath.Dir(configPath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	lock, err := os.OpenFile(configPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("lock config: %v", err)
	}
	if err := util.FlockExclusive(lock); err != nil {
		_ = lock.Close()
		return fmt.Errorf("lock config: %v", err)
	}
	defer func() {
		_ = util.FlockUnlock(lock)
		_ = lock.Close()
	}()
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(configPath); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("write config: %v", err)
	}
	tmpName := tmp.Name()
	_ = tmp.Chmod(mode)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write config: %v", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write config: %v", err)
	}
	_ = os.Chmod(tmpName, mode)
	if err := os.Rename(tmpName, configPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write config: %v", err)
	}
	return nil
}
