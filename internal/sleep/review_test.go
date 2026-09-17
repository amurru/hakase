// review_test.go - M4 reviewed-file-flow acceptance: hand-written files are
// exempt, machine-generated files refuse real-backend use until reviewed,
// the sidecar fails closed on tamper/staleness/predated review, and the
// sidecar lands 0600.
package sleep

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTasksFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const handWrittenTasks = `{"tasks": [{"id": "t1", "intent": "q", "reference_kind": "exact", "reference": "yes"}]}`

func machineGeneratedTasks(gen time.Time) string {
	return `{"generated_at": ` + quoteTime(gen) + `, "tasks": [{"id": "t1", "intent": "q"}]}`
}

func quoteTime(ts time.Time) string {
	b, _ := json.Marshal(ts)
	return string(b)
}

func TestRequireReviewed_HandWrittenExempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	writeTasksFile(t, path, handWrittenTasks)
	if err := RequireReviewed(path); err != nil {
		t.Errorf("hand-written file must be exempt: %v", err)
	}
	// A hand-written file may still carry a sidecar certifying it.
	if err := MarkTasksReviewed(path, "operator"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := VerifyTaskReview(path); err != nil {
		t.Errorf("verify after mark: %v", err)
	}
}

func TestRequireReviewed_MachineGeneratedBlocksUntilReviewed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	gen := time.Now().UTC().Add(-2 * time.Hour)
	writeTasksFile(t, path, machineGeneratedTasks(gen))

	err := RequireReviewed(path)
	if err == nil {
		t.Fatal("machine-generated file without sidecar must be refused")
	}
	if !strings.Contains(err.Error(), "review sidecar") || !strings.Contains(err.Error(), "sleep review") {
		t.Errorf("error must point at the review command: %v", err)
	}

	// After review it passes, and the sidecar is 0600.
	if err := MarkTasksReviewed(path, "alice"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := RequireReviewed(path); err != nil {
		t.Fatalf("post-review: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "tasks.review.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("sidecar mode = %o, want 600", info.Mode().Perm())
	}

	// Any edit after review invalidates the pin.
	writeTasksFile(t, path, machineGeneratedTasks(gen)+ "\n")
	if err := RequireReviewed(path); err == nil {
		t.Error("tampered file must be refused (hash mismatch)")
	}
}

func TestVerifyTaskReview_StaleOrPredated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	gen := time.Now().UTC().Add(-30 * 24 * time.Hour) // generated 30d ago

	tests := []struct {
		name    string
		sidecar TaskReviewSidecar
	}{
		{
			name:    "reviewed before generated",
			sidecar: TaskReviewSidecar{TasksSHA256: "x", Reviewer: "a", ReviewedAt: gen.Add(-time.Hour), GeneratedAt: gen},
		},
		{
			name:    "stale review",
			sidecar: TaskReviewSidecar{TasksSHA256: "x", Reviewer: "a", ReviewedAt: gen.Add(2 * time.Hour), GeneratedAt: gen},
		},
		{
			name:    "empty reviewer",
			sidecar: TaskReviewSidecar{TasksSHA256: "x", ReviewedAt: time.Now().UTC(), GeneratedAt: gen},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := machineGeneratedTasks(gen)
			writeTasksFile(t, path, content)
			// Real hash so the targeted check (stale/predated/empty-reviewer)
			// is the one that fires, not the tamper check.
			tc.sidecar.TasksSHA256 = sha256Hex([]byte(content))
			blob, _ := json.Marshal(tc.sidecar)
			if err := os.WriteFile(path+".review.json", blob, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := VerifyTaskReview(path); err == nil {
				t.Errorf("%s must fail closed", tc.name)
			}
		})
	}
}

func TestMarkTasksReviewed_RequiresReviewer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	writeTasksFile(t, path, handWrittenTasks)
	if err := MarkTasksReviewed(path, "  "); err == nil {
		t.Error("empty reviewer must be refused")
	}
	if _, err := os.Stat(path + ".review.json"); !os.IsNotExist(err) {
		t.Error("no sidecar may be written for an empty reviewer")
	}
}
