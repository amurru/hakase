// review.go - the reviewed-task-file flow (plan SL-020 / audit M4). Files
// produced by harvest+mine carry machine-generated content (redacted but
// unreviewed session excerpts); before any real-backend mine/replay/judge
// consumes them, a human must review the file and sign the review sidecar.
// Hand-written task files (operator-authored, Phase 1 --tasks flow) are
// exempt: their author already saw the content.
//
// Sidecar contract (plan section 7): tasks.json stays the machine shape;
// the sidecar tasks.review.json records tasks_sha256, reviewer, reviewed_at
// and the file's generated_at. Verification fails closed on: missing
// sidecar, empty reviewer, hash mismatch (edit after review), reviewed_at
// before generated_at, and stale review (older than ReviewFreshness).
package sleep

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ReviewFreshness bounds how long a signed review stays valid.
const ReviewFreshness = 7 * 24 * time.Hour

// TaskReviewSidecar is the signed human-review record for a tasks file.
type TaskReviewSidecar struct {
	TasksSHA256 string    `json:"tasks_sha256"`
	Reviewer    string    `json:"reviewer"`
	ReviewedAt  time.Time `json:"reviewed_at"`
	// GeneratedAt is the reviewed file's own generation stamp (zero for
	// hand-written files, which the sidecar may still certify).
	GeneratedAt time.Time `json:"generated_at,omitempty"`
}

// generatedHeader is the optional machine-generation marker inside a tasks
// file. Unknown to MarkdownTaskFile parsing, it only marks provenance.
type generatedHeader struct {
	GeneratedAt time.Time `json:"generated_at"`
}

// sidecarPath maps a tasks file to its review sidecar:
// tasks.json -> tasks.review.json.
func sidecarPath(tasksFile string) string {
	if strings.HasSuffix(tasksFile, ".json") {
		return strings.TrimSuffix(tasksFile, ".json") + ".review.json"
	}
	return tasksFile + ".review.json"
}

// MachineGeneratedAt reports when a tasks file was produced by the sleep
// harvester/miner (non-zero generated_at) and zero for hand-written files.
func MachineGeneratedAt(tasksFile string) (time.Time, error) {
	data, err := os.ReadFile(tasksFile)
	if err != nil {
		return time.Time{}, err
	}
	var header generatedHeader
	if err := json.Unmarshal(data, &header); err != nil || header.GeneratedAt.IsZero() {
		return time.Time{}, nil
	}
	return header.GeneratedAt, nil
}

// MarkTasksReviewed records a human review of tasksFile. The current file
// hash is pinned at review time; any later edit invalidates it.
func MarkTasksReviewed(tasksFile, reviewer string) error {
	if strings.TrimSpace(reviewer) == "" {
		return fmt.Errorf("reviewer name required")
	}
	data, err := os.ReadFile(tasksFile)
	if err != nil {
		return fmt.Errorf("read tasks file: %w", err)
	}
	var generated time.Time
	var header generatedHeader
	if err := json.Unmarshal(data, &header); err == nil {
		generated = header.GeneratedAt
	}
	sidecar := TaskReviewSidecar{
		TasksSHA256: sha256Hex(data),
		Reviewer:    strings.TrimSpace(reviewer),
		ReviewedAt:  time.Now().UTC(),
		GeneratedAt: generated,
	}
	blob, err := json.MarshalIndent(sidecar, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(sidecarPath(tasksFile), blob, 0o600)
}

// VerifyTaskReview checks the review sidecar against the current file.
// Every failure names the fix.
func VerifyTaskReview(tasksFile string) error {
	data, err := os.ReadFile(tasksFile)
	if err != nil {
		return fmt.Errorf("read tasks file: %w", err)
	}
	var sidecar TaskReviewSidecar
	sidecarBytes, err := os.ReadFile(sidecarPath(tasksFile))
	if err != nil {
		return fmt.Errorf("tasks file %s has no review sidecar (%s): review it first with `hakase sleep review --tasks %s --reviewer <name>`",
			tasksFile, sidecarPath(tasksFile), tasksFile)
	}
	if err := json.Unmarshal(sidecarBytes, &sidecar); err != nil {
		return fmt.Errorf("parse %s: %w", sidecarPath(tasksFile), err)
	}
	if strings.TrimSpace(sidecar.Reviewer) == "" {
		return fmt.Errorf("review sidecar %s has no reviewer", sidecarPath(tasksFile))
	}
	if sidecar.ReviewedAt.IsZero() {
		return fmt.Errorf("review sidecar %s has no reviewed_at", sidecarPath(tasksFile))
	}
	if sha256Hex(data) != sidecar.TasksSHA256 {
		return fmt.Errorf("tasks file %s changed since review (hash mismatch): re-review", tasksFile)
	}
	var header generatedHeader
	_ = json.Unmarshal(data, &header)
	if !header.GeneratedAt.IsZero() {
		if sidecar.ReviewedAt.Before(header.GeneratedAt) {
			return fmt.Errorf("review sidecar %s predates the file it certifies: re-review", sidecarPath(tasksFile))
		}
		if sidecar.ReviewedAt.Sub(header.GeneratedAt) > ReviewFreshness {
			return fmt.Errorf("review sidecar %s is stale (reviewed more than %v after generation): re-review",
				sidecarPath(tasksFile), ReviewFreshness)
		}
	}
	// Freshness measures the AGE OF THE REVIEW against now, for hand-written
	// and machine-generated files alike (CodeRabbit): the generation-to-review
	// delay above only bounds backdating, it does not keep an old review
	// authoritative forever.
	if time.Since(sidecar.ReviewedAt) > ReviewFreshness {
		return fmt.Errorf("review sidecar %s is stale (review older than %v): re-review",
			sidecarPath(tasksFile), ReviewFreshness)
	}
	return nil
}

// RequireReviewed enforces M4 before any real-backend mine/replay/judge:
// machine-generated (harvest/mine-produced) task files must carry a valid
// review sidecar; hand-written files are exempt because their author is
// the human in the loop. dry-run/mock callers are exempt upstream (no
// provider call and no stage/adopt there).
func RequireReviewed(tasksFile string) error {
	generated, err := MachineGeneratedAt(tasksFile)
	if err != nil {
		return err
	}
	if generated.IsZero() {
		return nil
	}
	return VerifyTaskReview(tasksFile)
}
