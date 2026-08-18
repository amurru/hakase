// state.go - persistent enable/disable state for skills.
//
// The state file records the skills that are currently disabled. It lives in
// the user-level hakase home (~/.hakase/skill-state.json, or $HAKASE_HOME) so
// it applies across projects. Entries are keyed as "<type>:<name>" (e.g.
// "python:render_card", "markdown:data-cleaner") so a Python skill and a
// markdown skill that share a name are toggled independently.
package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"amurru/hakase/internal/config"
)

// Skill kinds used in the disabled-state keys.
const (
	KindPython   = "python"
	KindMarkdown = "markdown"
)

// SkillState is the persisted JSON structure in skill-state.json.
type SkillState struct {
	Disabled []string `json:"disabled,omitempty"`
}

// skillStateMu serializes in-process access to skill-state.json.
var skillStateMu sync.Mutex

// SkillStateFile returns the path to the persisted skill state file,
// creating the parent directory if missing. Uses $HAKASE_HOME or ~/.hakase.
func SkillStateFile() string {
	home := config.HakaseHome()
	if home == "" {
		home = "."
	}
	_ = os.MkdirAll(home, 0o755)
	return filepath.Join(home, "skill-state.json")
}

// SkillKey returns the canonical state key for a skill: "<kind>:<name>".
func SkillKey(kind, name string) string {
	return kind + ":" + name
}

// loadSkillStateLocked reads the state file from disk under the mutex. A
// missing file, or a file that cannot be parsed, yields an empty state rather
// than an error so a subsequent write repairs the file.
func loadSkillStateLocked() (SkillState, error) {
	var st SkillState
	data, err := os.ReadFile(SkillStateFile())
	if err != nil {
		if os.IsNotExist(err) {
			return SkillState{}, nil
		}
		return SkillState{}, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return SkillState{}, nil
	}
	return st, nil
}

// saveSkillStateLocked writes the state atomically (tmp-file + rename) with
// an exclusive flock for cross-process safety.
func saveSkillStateLocked(st SkillState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	file := SkillStateFile()
	tmp := file + ".tmp"
	lockFile := file + ".lock"

	lf, err := os.OpenFile(lockFile, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// UpdateSkillState loads, mutates, and saves the state under one lock hold.
func UpdateSkillState(mutate func(*SkillState) error) error {
	skillStateMu.Lock()
	defer skillStateMu.Unlock()

	st, err := loadSkillStateLocked()
	if err != nil {
		return err
	}
	if err := mutate(&st); err != nil {
		return err
	}
	return saveSkillStateLocked(st)
}

// SetSkillDisabled adds (disabled=true) or removes (disabled=false) a skill
// identified by kind and name. Idempotent: setting an already-disabled skill
// disabled again is a no-op, as is enabling an enabled skill.
func SetSkillDisabled(kind, name string, disabled bool) error {
	key := SkillKey(kind, name)
	return UpdateSkillState(func(st *SkillState) error {
		idx := -1
		for i, n := range st.Disabled {
			if n == key {
				idx = i
				break
			}
		}
		if disabled && idx < 0 {
			st.Disabled = append(st.Disabled, key)
		}
		if !disabled && idx >= 0 {
			st.Disabled = append(st.Disabled[:idx], st.Disabled[idx+1:]...)
		}
		return nil
	})
}

// IsSkillDisabled reports whether the named skill of the given kind is
// currently disabled. A read or parse error on the state file is treated as
// "not disabled".
func IsSkillDisabled(kind, name string) bool {
	return DisabledSkillsSet()[SkillKey(kind, name)]
}

// DisabledSkillsSet returns the set of disabled skill keys ("<kind>:<name>")
// for bulk checks. Always non-nil; a missing or corrupt state file yields an
// empty set.
func DisabledSkillsSet() map[string]bool {
	skillStateMu.Lock()
	defer skillStateMu.Unlock()

	st, err := loadSkillStateLocked()
	if err != nil {
		return map[string]bool{}
	}
	set := make(map[string]bool, len(st.Disabled))
	for _, n := range st.Disabled {
		set[n] = true
	}
	return set
}