// skillsconfine.go - write_file/patch skills confinement (S28).
//
// The executable Python skill library is save_skill's domain: only code
// that passed the python gate (provenance record, S16) may persist there.
// A direct write_file/patch into it would persist unproven model-typed
// code, bypassing the provenance gate, so the file-ops tools refuse it
// deny-first (before MkdirAll, before any write) with the save_skill
// alternative named.
//
// Confined (absolute post-resolve path):
//
//   - <any>/skills/<name>.py, <any>/skills/__init__.py,
//     <any>/skills/skills.json
//
// Non-code files under a skills dir (READMEs, fixtures) stay writable:
// the confinement targets executable persistence, not documentation.
// Markdown discovery trees (.agents/skills, .claude/skills, ...) are not
// executable Python and stay writable via write_file; the system_exec
// bypass for blocked writes is closed separately in the orchestrator
// instruction (deny-first: never route around a write_file denial via
// another tool - ask the user instead).
package sandbox

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// rerouteNoPromptKey marks a save_skill gate evaluation issued from the
// write_file/patch reroute (M5): the reroute exists only to keep the
// flow/provenance legs from drifting from the static suffix check - it
// never persists skills itself. The agent gate fails such evaluations
// closed without raising a human approval (an approval that always
// precedes a denial wastes the human round-trip and can hang
// non-interactive runs).
type rerouteNoPromptKey struct{}

// RerouteNoPromptCtx wraps ctx so the unified save_skill gate evaluates
// without prompting (M5 non-interactive reroute mode).
func RerouteNoPromptCtx(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, rerouteNoPromptKey{}, true)
}

// IsRerouteNoPrompt reports whether ctx is a write_file/patch reroute
// evaluation (no approval prompt allowed).
func IsRerouteNoPrompt(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(rerouteNoPromptKey{}).(bool)
	return v
}

// denySkillsWrite refuses tool writes into the Python skill library.
// absPath is the resolved absolute path (post-taskResolve). Nil when the
// path is outside the confined set (caller proceeds).
//
// Confined when any path element (case-insensitive) is named "skills" and
// the final name is executable persistence: *.py/*.pyc/*.pyd/*.pyw,
// *.pth (executed at interpreter startup), native modules (*.so/*.dll,
// importable once the skills package loads), skills.json, or
// __init__.py/__init__.pyc/__init__.pyd. Nested skill trees
// (skills/subdir/evil.py) are confined the same as top-level files;
// non-code files (READMEs, fixtures) and markdown discovery trees stay
// writable.
func denySkillsWrite(absPath string) error {
	clean := filepath.Clean(absPath)
	lowerClean := strings.ToLower(clean)
	// Any element named "skills" confines (nested trees included).
	hasSkillsElem := false
	for _, el := range strings.Split(lowerClean, string(filepath.Separator)) {
		if el == "skills" {
			hasSkillsElem = true
			break
		}
	}
	if !hasSkillsElem {
		return nil
	}
	base := strings.ToLower(filepath.Base(clean))
	// R31: startup-executed and natively-importable suffixes confine
	// strictly alongside .py: .pyw (Windows Python launcher executes),
	// .pth (site.py executes import lines at interpreter startup), and
	// .so/.dll (extension modules importable once the package loads).
	if strings.HasSuffix(base, ".py") || strings.HasSuffix(base, ".pyc") || strings.HasSuffix(base, ".pyd") ||
		strings.HasSuffix(base, ".pyw") || strings.HasSuffix(base, ".pth") ||
		strings.HasSuffix(base, ".so") || strings.HasSuffix(base, ".dll") {
		return fmt.Errorf("write to the Python skill library is confined: persist tested code via save_skill (provenance-gated), not write_file/patch")
	}
	if base == "skills.json" || base == "__init__.py" || base == "__init__.pyc" || base == "__init__.pyd" {
		return fmt.Errorf("write to the Python skill library is confined: persist tested code via save_skill (provenance-gated), not write_file/patch")
	}
	return nil
}
