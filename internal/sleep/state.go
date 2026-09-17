// state.go - sleep cycle state (.hakase/sleep-state.json, plan SL-022):
// the harvest checkpoint (monotonic), the model-identity pause, and the
// rolling night records. Writer rules follow SL-005: 0600 file in a 0700
// dir, flock during read-modify-write, atomic tmp+rename, schema validation
// on load with corrupt-file rollback (the bad file is preserved aside, the
// cycle restarts from an empty state and says so loudly).
package sleep

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"amurru/hakase/internal/util"
)

// SleepState is the persisted cross-night state.
type SleepState struct {
	// LastHarvest is the checkpoint: the next night harvests only turns at
	// or after this instant. Monotonic by save-time validation.
	LastHarvest time.Time `json:"last_harvest"`
	// LastModelKey pins the provider/model identity that produced the
	// history. A change pauses the cycle until acknowledged.
	LastModelKey string        `json:"last_model_key,omitempty"`
	Nights       []NightRecord `json:"nights,omitempty"`
}

// NightRecord is one cycle run. Outcome: staged | empty | aborted.
type NightRecord struct {
	StartedAt   time.Time          `json:"started_at"`
	Outcome     string             `json:"outcome"`
	StagingDir  string             `json:"staging_dir,omitempty"`
	Sessions    int                `json:"sessions,omitempty"`
	Tasks       int                `json:"tasks,omitempty"`
	Tokens      int                `json:"tokens,omitempty"`
	AbortReason string             `json:"abort_reason,omitempty"`
	// Groups records per-skill epoch outcomes (plan Phase 3, SL-031): the
	// slow-update trend compare reads this history; older state files
	// without groups still validate.
	Groups []NightGroupRecord `json:"groups,omitempty"`
}

// NightGroupRecord is one skill group's outcome within a night: the gate
// scores the slow-update compare tracks longitudinally.
type NightGroupRecord struct {
	SkillName       string    `json:"skill_name"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	BaselineScore   float64   `json:"baseline_score"`
	CandidateScore  float64   `json:"candidate_score"`
	Accepted        bool      `json:"accepted"`
	Consolidated    bool      `json:"consolidated"`
}

// DefaultStatePath is the project-local state file (plan: .hakase/sleep-state.json).
const DefaultStatePath = ".hakase/sleep-state.json"

// StatePruneAge bounds night-record retention (and the outputs/sleep/
// staging-dir prune): 30 days default per plan SL-022.
const StatePruneAge = 30 * 24 * time.Hour

// LoadSleepState reads and validates the state file. A missing file is an
// empty valid state; a corrupt file (unparseable or schema-violating) is
// moved aside (preserved for forensics) and an empty state returns with a
// note - the checkpoint reset is visible, never silent.
func LoadSleepState(path string) (SleepState, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SleepState{}, "", nil
		}
		return SleepState{}, "", fmt.Errorf("read sleep state: %w", err)
	}
	var st SleepState
	if err := json.Unmarshal(data, &st); err != nil {
		return rollbackCorruptState(path, err)
	}
	// Schema validation: outcomes are an enum, timestamps sane.
	for _, n := range st.Nights {
		switch n.Outcome {
		case "staged", "empty", "aborted":
		default:
			return rollbackCorruptState(path, fmt.Errorf("unknown night outcome %q", n.Outcome))
		}
		if n.StartedAt.IsZero() {
			return rollbackCorruptState(path, fmt.Errorf("night record with zero timestamp"))
		}
	}
	return st, "", nil
}

// rollbackCorruptState preserves the unreadable state file aside and resets
// to empty. The reset is returned as a note for the report, never swallowed.
func rollbackCorruptState(path string, reason error) (SleepState, string, error) {
	aside := path + ".corrupt-" + time.Now().UTC().Format("20060102-150405")
	if err := os.Rename(path, aside); err != nil {
		return SleepState{}, "", fmt.Errorf("sleep state corrupt (%v) and could not be preserved: %w", reason, err)
	}
	note := fmt.Sprintf("sleep state corrupt (%v); preserved as %s and reset", reason, aside)
	return SleepState{}, note, nil
}

// SaveSleepState persists the state under an exclusive lock with the
// monotonic-checkpoint invariant enforced against the on-disk predecessor.
func SaveSleepState(path string, st SleepState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open sleep state lock: %w", err)
	}
	defer lock.Close()
	if err := util.FlockExclusive(lock); err != nil {
		return fmt.Errorf("lock sleep state: %w", err)
	}
	defer func() { _ = util.FlockUnlock(lock) }()

	// Monotonic last_harvest: never save a checkpoint older than the one on
	// disk (a stale writer must not roll the harvest window forward again).
	if cur, err := os.ReadFile(path); err == nil {
		var prev SleepState
		if json.Unmarshal(cur, &prev) == nil && prev.LastHarvest.After(st.LastHarvest) {
			return fmt.Errorf("sleep state: refusing to move last_harvest backwards (%s -> %s)",
				prev.LastHarvest.Format(time.RFC3339), st.LastHarvest.Format(time.RFC3339))
		}
	}

	// Prune night records past the retention window.
	cutoff := time.Now().UTC().Add(-StatePruneAge)
	kept := st.Nights[:0]
	for _, n := range st.Nights {
		if !n.StartedAt.Before(cutoff) {
			kept = append(kept, n)
		}
	}
	st.Nights = kept

	blob, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, blob, 0o600)
}

// CheckModelKey enforces the model-identity pause: a different provider or
// model than the one that produced the history is a HARD pause requiring
// explicit --acknowledge-model-change (never a warning).
func CheckModelKey(st SleepState, modelKey string, acknowledgeChange bool) error {
	if st.LastModelKey == "" || modelKey == "" || st.LastModelKey == modelKey {
		return nil
	}
	if acknowledgeChange {
		return nil
	}
	return fmt.Errorf("sleep paused: model changed since the last night (%s -> %s); re-run with --acknowledge-model-change after reviewing prior proposals",
		st.LastModelKey, modelKey)
}
