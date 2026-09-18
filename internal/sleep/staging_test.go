// staging_test.go - SL-013 acceptance: stage round-trip and adopt guards.
package sleep

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/skill"
)

const stagingSkillDoc = `---
name: demo
description: Demo skill.
---

# Demo

Hand-written guidance.
`

// writeLiveSkill creates dir/demo/SKILL.md (dir name must match frontmatter)
// and returns the SKILL.md path.
func writeLiveSkill(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte(stagingSkillDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func acceptedResult() ConsolidationResult {
	newSkill, _, _ := skill.ApplyEdits(stagingSkillDoc, []skill.TextEdit{
		{Op: "add", Content: "Learned rule.", Rationale: "why"},
	})
	return ConsolidationResult{
		Accepted: true, GateAction: "accept_new_best",
		BaselineScore: 0.5, CandidateScore: 1.0, NewSkill: newSkill,
		Applied: []skill.TextEdit{{Op: "add", Content: "Learned rule.", Rationale: "why"}},
		Deltas:  []skill.ScoreDelta{{TaskID: "v1", BaselineScore: 0, CandidateScore: 1}},
	}
}

func TestStageAndAdopt_RoundTrip(t *testing.T) {
	isolateSkillState(t)
	root := t.TempDir()
	live := writeLiveSkill(t, root)

	dir, err := StageConsolidation(filepath.Join(root, "out"), "demo", live, acceptedResult())
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	for _, f := range []string{"report.md", "report.json", "diagnostics.json", "proposed_SKILL.md", "adopt.json"} {
		info, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", f, got)
		}
	}
	report, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	for _, want := range []string{"accept_new_best", "v1", "Learned rule."} {
		if !strings.Contains(string(report), want) {
			t.Errorf("report missing %q", want)
		}
	}

	adopted, backup, err := AdoptStaging(dir)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if adopted != live {
		t.Errorf("adopted = %s, want %s", adopted, live)
	}
	after, _ := os.ReadFile(live)
	if !strings.Contains(string(after), "Learned rule.") {
		t.Error("live file missing the rule after adopt")
	}
	if _, err := os.Stat(live + ".bak"); err != nil {
		t.Errorf("missing .bak: %v", err)
	}
	// Versioned backup (plan SL-040): <path>.bak.<ts> with the incumbent.
	if backup == "" || !strings.HasPrefix(backup, live+".bak.") {
		t.Fatalf("backup = %q, want a versioned %s.bak.<ts>", backup, live)
	}
	if info, err := os.Stat(backup); err != nil {
		t.Errorf("missing versioned backup: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %o, want 600", info.Mode().Perm())
	}
	if bak, _ := os.ReadFile(backup); !strings.Contains(string(bak), "Hand-written guidance.") {
		t.Error("backup must hold the incumbent content")
	}
}

func TestAdopt_VersionedBackupsPrunedTo3(t *testing.T) {
	isolateSkillState(t)
	root := t.TempDir()
	live := writeLiveSkill(t, root)
	// Seed 5 stale versioned backups with chronological names.
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Minute).Format("20060102-150405")
		if err := os.WriteFile(fmt.Sprintf("%s.bak.%s", live, ts), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := StageConsolidation(filepath.Join(root, "out"), "demo", live, acceptedResult())
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, _, err := AdoptStaging(dir); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	matches, _ := filepath.Glob(live + ".bak.*")
	if len(matches) != 3 {
		t.Errorf("versioned backups = %d, want pruned to 3", len(matches))
	}
	sort.Strings(matches)
	// The newest stale one plus tonight's must survive; the oldest go.
	newestStale := fmt.Sprintf("%s.bak.%s", live, base.Add(4*time.Minute).Format("20060102-150405"))
	if len(matches) > 0 && matches[len(matches)-1] < newestStale {
		t.Errorf("newest backup = %s, want >= %s", matches[len(matches)-1], newestStale)
	}
}

func TestAdopt_RejectsUnaccepted(t *testing.T) {
	root := t.TempDir()
	live := writeLiveSkill(t, root)
	res := acceptedResult()
	res.Accepted = false
	res.GateAction = "reject"
	dir, err := StageConsolidation(filepath.Join(root, "out"), "demo", live, res)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "proposed_SKILL.md")); !os.IsNotExist(err) {
		t.Error("rejected night must not stage a proposal")
	}
	if _, _, err := AdoptStaging(dir); err == nil {
		t.Error("adopt of unaccepted staging must fail")
	}
}

func TestAdopt_HashMismatch(t *testing.T) {
	root := t.TempDir()
	live := writeLiveSkill(t, root)
	dir, err := StageConsolidation(filepath.Join(root, "out"), "demo", live, acceptedResult())
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	// Concurrent hand-edit after staging.
	f, _ := os.ReadFile(live)
	_ = os.WriteFile(live, append(f, []byte("\nHand edit.\n")...), 0o600)
	if _, _, err := AdoptStaging(dir); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("must fail on hash mismatch: %v", err)
	}
	after, _ := os.ReadFile(live)
	if strings.Contains(string(after), "Learned rule.") {
		t.Error("live file must be untouched after failed adopt")
	}
}

func TestAdopt_SymlinkSwap(t *testing.T) {
	root := t.TempDir()
	live := writeLiveSkill(t, root)
	targetA := filepath.Join(root, "a.md")
	_ = os.WriteFile(targetA, []byte(stagingSkillDoc), 0o600)
	link := filepath.Join(root, "link.md")
	if err := os.Symlink(targetA, link); err != nil {
		t.Fatal(err)
	}
	// Stage against the symlink path (pins targetA's realpath).
	dir, err := StageConsolidation(filepath.Join(root, "out"), "demo", link, acceptedResult())
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	// Swap the symlink to a same-content file elsewhere.
	targetB := filepath.Join(root, "b.md")
	_ = os.WriteFile(targetB, []byte(stagingSkillDoc), 0o600)
	_ = os.Remove(link)
	if err := os.Symlink(targetB, link); err != nil {
		t.Fatal(err)
	}
	_ = live // live skill file itself is untouched by this scenario
	if _, _, err := AdoptStaging(dir); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("must fail on symlink swap: %v", err)
	}
}

func TestAdopt_FrontmatterTamper(t *testing.T) {
	root := t.TempDir()
	live := writeLiveSkill(t, root)
	dir, err := StageConsolidation(filepath.Join(root, "out"), "demo", live, acceptedResult())
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	// Tamper the staged proposal's frontmatter.
	prop, _ := os.ReadFile(filepath.Join(dir, "proposed_SKILL.md"))
	tampered := strings.Replace(string(prop), "description: Demo skill.", "description: Hijacked.", 1)
	_ = os.WriteFile(filepath.Join(dir, "proposed_SKILL.md"), []byte(tampered), 0o600)
	if _, _, err := AdoptStaging(dir); err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("must fail on frontmatter tamper: %v", err)
	}
}

func TestLoadMarkdownTasks_Shapes(t *testing.T) {
	dir := t.TempDir()
	obj := filepath.Join(dir, "obj.json")
	_ = os.WriteFile(obj, []byte(`{"tasks": [{"id": "a", "intent": "x"}]}`), 0o600)
	arr := filepath.Join(dir, "arr.json")
	_ = os.WriteFile(arr, []byte(`[{"id": "b", "intent": "y"}]`), 0o600)
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"nope": true}`), 0o600)

	tasks, err := LoadMarkdownTasks(obj)
	if err != nil || len(tasks) != 1 || tasks[0].ID != "a" {
		t.Errorf("object shape: %+v %v", tasks, err)
	}
	tasks, err = LoadMarkdownTasks(arr)
	if err != nil || len(tasks) != 1 || tasks[0].ID != "b" {
		t.Errorf("array shape: %+v %v", tasks, err)
	}
	if _, err := LoadMarkdownTasks(bad); err == nil {
		t.Error("bad shape must error")
	}
	if _, err := LoadMarkdownTasks(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file must error")
	}
}

// isolateSkillState redirects HAKASE_HOME so disabled-skill checks never
// touch the real user home.
func isolateSkillState(t *testing.T) {
	t.Helper()
	t.Setenv("HAKASE_HOME", t.TempDir())
}

func TestAdopt_BlockedWhenDisabled(t *testing.T) {
	isolateSkillState(t)
	root := t.TempDir()
	live := writeLiveSkill(t, root)
	dir, err := StageConsolidation(root+"/out", "demo", live, acceptedResult())
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := skill.SetSkillDisabled(skill.KindMarkdown, "demo", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AdoptStaging(dir); err == nil {
		t.Error("adopt of a disabled skill must fail")
	}
}

func TestAdoptMeta_RoundTrip(t *testing.T) {
	// adopt.json pins survive a JSON round-trip with all fields.
	root := t.TempDir()
	live := writeLiveSkill(t, root)
	dir, err := StageConsolidation(filepath.Join(root, "out"), "demo", live, acceptedResult())
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "adopt.json"))
	var meta AdoptMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("adopt.json: %v", err)
	}
	if meta.SkillName != "demo" || !meta.Accepted || meta.LiveSHA256 == "" || meta.LiveRealpath == "" {
		t.Errorf("adopt pins incomplete: %+v", meta)
	}
}
