// skill_provenance.go - save_skill code-source allowlist (S16).
//
// save_skill must only persist code that already passed the python gate:
// the exact code bytes must have a prior python_interpreter execution
// record (success or gate-approved) in this Hakase session, otherwise the
// save is refused. The store is in-memory only (no disk), keyed by Hakase
// session ID (no cross-session reuse), bounded (<=256 hashes/session,
// oldest evicted), and hash-only (never raw code).
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/skill"
)

// maxSkillProvenance bounds recorded code hashes per session. Eviction is
// oldest-first (fail-closed direction: an evicted hash re-asks via a fresh
// python run, never allows).
const maxSkillProvenance = 256

// skillProvenanceSet is one session's ordered hash set (insertion order
// doubles as the LRU eviction order: records are append-only, never
// re-ranked on read, so there is no sliding window).
type skillProvenanceSet struct {
	order []string
	seen  map[string]struct{}
}

var skillProvenance = struct {
	sync.Mutex
	m map[string]*skillProvenanceSet
}{m: make(map[string]*skillProvenanceSet)}

// normalizeSkillCode canonicalizes code before hashing so the record made
// at python-gate time matches the save-time hash for byte-identical code
// despite trailing-newline/line-ending drift.
func normalizeSkillCode(code string) string {
	s := strings.ReplaceAll(code, "\r\n", "\n")
	return strings.TrimSpace(s)
}

// skillCodeHash hashes normalized code (hex sha256). The hash - never raw
// code - is what the provenance store and audit-adjacent paths carry.
func skillCodeHash(code string) string {
	sum := sha256.Sum256([]byte(normalizeSkillCode(code)))
	return hex.EncodeToString(sum[:])
}

// saveSkillNameRe is the strict skill-name shape (S16): lowercase,
// digits and underscores only, bounded length. Path separators never
// match; __init__ matches the shape but is rejected explicitly below.
var saveSkillNameRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// skillJointHash binds the proven code to the exact description the model
// supplied (R32): the same code saved under a different description is a
// different joint, so a laundered description never rides code
// provenance. The hash - never raw code or raw description - is what
// approvals and the registry carry (hash-only audit discipline).
func skillJointHash(code, description string) string {
	sum := sha256.Sum256([]byte(normalizeSkillCode(code) + "\x00" + strings.TrimSpace(description)))
	return hex.EncodeToString(sum[:])
}

// skillExists reports whether name already has a registry entry (R32
// update path) OR an on-disk skill file. A missing or corrupt registry
// reads as absent for the registry leg, but an existing
// skills/<name>.py still counts as existing: otherwise deleting (or
// never writing) skills.json would downgrade an overwrite into the
// create path and skip the fresh update approval (M4 fail-closed).
// The save itself still fails closed on corrupt registries (explicit
// error, never a silent overwrite), and a missing file + missing entry
// is the create path.
func skillExists(name string) bool {
	if data, err := os.ReadFile(filepath.Join(skillsDirAbs(), "skills.json")); err == nil {
		var reg skill.SkillRegistry
		if err := json.Unmarshal(data, &reg); err == nil {
			for _, s := range reg.Skills {
				if s.Name == name {
					return true
				}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(skillsDirAbs(), name+".py")); err == nil {
		return true
	}
	return false
}

// wireSaveSkillGate routes skills-library write denials (write_file/patch
// in the sandbox package) through the unified save_skill provenance
// gate (R32). Called once from SetupRunner; extracted so tests can pin
// the wiring without booting a runner.
func wireSaveSkillGate() {
	sandbox.SaveSkillGateFunc = func(ctx context.Context, name, code, description string) error {
		return checkSaveSkillAllowed(ctx, name, code, description)
	}
}

// checkSaveSkillAllowed is the save_skill gate (S16, ADR condition 4,
// R32 joint binding + fresh update approval): the existing flow re-check
// runs first (deny-wins over provenance), then the strict name
// validation, then the code-source provenance check, then - for
// overwrites of an existing skill - a fresh human approval binding the
// exact code+description joint. A provenance refusal happens before any
// file or registry mutation (ADR condition 9).
func checkSaveSkillAllowed(ctx context.Context, name, code, description string) error {
	if blocked, eff, freason := checkPreFlowVerdict(ctx, "save_skill", map[string]any{"name": name}); blocked && eff == permissions.EffectDeny {
		return fmt.Errorf("save_skill blocked by flow guard: %s", freason)
	}
	if name == "__init__" || !saveSkillNameRe.MatchString(name) {
		return fmt.Errorf("save_skill: invalid skill name %q (must match ^[a-z0-9_]{1,64}$ and must not be __init__)", name)
	}
	sessionID := flowSessionID(ctx)
	if !checkSkillCodeProvenance(sessionID, skillCodeHash(code)) {
		return fmt.Errorf("save_skill requires prior python_interpreter execution of the exact code in this session (no provenance record)")
	}
	// R32 fresh update approval: overwriting an existing skill re-prompts
	// the human every time (a stale grant never satisfies this - the
	// per-call nonce makes each command unique - and approval.mode=allow
	// keeps its operator-chosen semantics via ApproveExec). The prompt
	// carries the joint hash (code+description binding, hash-only), so
	// the human approves this exact pair, never the code alone.
	//
	// M5 reroute: write_file/patch evaluate this gate only to keep the
	// flow/provenance legs from drifting - they never persist skills
	// themselves. A reroute evaluation (sandbox.IsRerouteNoPrompt) must
	// not raise a human approval and then deny anyway: it fails closed
	// here without prompting, naming save_skill as the owning tool.
	if skillExists(name) {
		if sandbox.IsRerouteNoPrompt(ctx) {
			return fmt.Errorf("write to the Python skill library is confined: persist tested code via save_skill (fresh approval required per update)")
		}
		joint := skillJointHash(code, description)
		approved, aerr := ApproveExec(ctx, ApprovalRequest{
			Tool:      "save_skill",
			Command:   fmt.Sprintf("save_skill update %s (joint:%s nonce:%d)", name, permissions.ShortHash(joint), time.Now().UnixNano()),
			Risk:      "medium",
			Reason:    "overwrite an existing saved skill (fresh approval per update)",
			Source:    "direct",
			ExpiresAt: time.Now().Add(ApprovalExpiry()),
			SessionID: sessionID,
		})
		if aerr != nil || !approved {
			return fmt.Errorf("save_skill update of %q not approved by user (fresh approval required per update)", name)
		}
	}
	return nil
}

// recordSkillProvenance records one executed (or explicitly approved)
// python payload for the session. Empty session IDs fail closed (M6):
// an empty (session-less) caller records nothing, so unrelated headless
// runs never share one bucket and provenance never authorizes without a
// bound session. Empty hashes are likewise ignored.
func recordSkillProvenance(sessionID, codeHash string) {
	if codeHash == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	skillProvenance.Lock()
	defer skillProvenance.Unlock()
	s, ok := skillProvenance.m[sessionID]
	if !ok {
		s = &skillProvenanceSet{seen: make(map[string]struct{})}
		skillProvenance.m[sessionID] = s
	}
	if _, dup := s.seen[codeHash]; dup {
		return
	}
	s.order = append(s.order, codeHash)
	s.seen[codeHash] = struct{}{}
	for len(s.order) > maxSkillProvenance {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.seen, oldest)
	}
}

// checkSkillCodeProvenance reports whether the session executed (or
// explicitly approved) the exact code hash via python_interpreter. Empty
// session IDs fail closed (M6): a session-less caller never carries
// provenance, even when a hash matches the shared empty bucket (there is
// no shared empty bucket - record drops empty sessions above).
func checkSkillCodeProvenance(sessionID, codeHash string) bool {
	if codeHash == "" || strings.TrimSpace(sessionID) == "" {
		return false
	}
	skillProvenance.Lock()
	defer skillProvenance.Unlock()
	s, ok := skillProvenance.m[sessionID]
	if !ok {
		return false
	}
	_, ok = s.seen[codeHash]
	return ok
}
