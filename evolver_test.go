// evolver_test.go - tests for the Phase 3b/3c skill-evolution engine:
// evaluator viability, mutator parsing, the A/B promotion gate, regression
// rollback (.bak preservation), and auto-deprecation.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill scaffolds a minimal skill library: skills.json + a .py file.
func writePySkill(t *testing.T, dir, name, code string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".py"), []byte(code), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	reg := SkillRegistry{Skills: []SkillMeta{{Name: name, Description: "test", FileName: name + ".py"}}}
	data, _ := json.MarshalIndent(reg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "skills.json"), data, 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

// writeEvalSet writes skills/<name>.eval.json.
func writeEvalSet(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".eval.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write eval set: %v", err)
	}
}

const adderSkill = `def add(a, b):
    return a + b + 1
`

const adderFix = `def add(a, b):
    return a + b
`

const adderEval = `{
  "cases": [
    {"name": "t1", "input": {"a": 1, "b": 2}, "expected": "3", "match": "contains", "train": true},
    {"name": "t2", "input": {"a": 5, "b": 7}, "expected": "12", "match": "contains", "train": true},
    {"name": "h1", "input": {"a": 10, "b": 20}, "expected": "30", "match": "contains", "train": false}
  ]
}
`

func TestParseMutationReply(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"```python\ndef add(a,b):\n    return a+b\n```", "def add(a,b):\n    return a+b", true},
		{"Here is the fix:\n```py\nprint(1)\n```\nregards", "print(1)", true},
		{"```\nx = 1\n```", "x = 1", true},
		{"no code here", "", false},
		{"```python\n```", "", false}, // empty fence
	}
	for _, c := range cases {
		got, ok := parseMutationReply(c.raw)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseMutationReply(%q) = (%q, %v), want (%q, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestEvaluateSkill_Viable(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "adder", adderSkill)
	writeEvalSet(t, dir, "adder", adderEval)

	res := evaluateSkill(dir, "adder", "adder.py")
	if !res.HasEvalSet {
		t.Fatal("expected eval set")
	}
	if res.EvalSetError != "" {
		t.Fatalf("eval error: %s", res.EvalSetError)
	}
	if !res.Viable {
		t.Fatal("expected viable skill")
	}
	if res.Trainable != 2 || res.Holdout != 1 {
		t.Errorf("trainable=%d holdout=%d, want 2/1", res.Trainable, res.Holdout)
	}
	if res.TrainPassed != 0 {
		t.Errorf("buggy skill should fail train cases, passed %d", res.TrainPassed)
	}
	if len(res.TrainFail) != 2 || len(res.HoldFail) != 1 {
		t.Errorf("trainFail=%d holdFail=%d, want 2/1", len(res.TrainFail), len(res.HoldFail))
	}
}

func TestEvaluateSkill_BrokenSeed(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "broken", "def broken(:\n    syntax error\n")
	writeEvalSet(t, dir, "broken", `{"cases":[{"input":1,"expected":"x"}]}`)

	res := evaluateSkill(dir, "broken", "broken.py")
	if res.Viable {
		t.Error("broken module must not be viable")
	}
}

func TestEvaluateSkill_NoEvalSetSkipped(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "plain", "def f():\n    return 1\n")
	res := evaluateSkill(dir, "plain", "plain.py")
	if res.HasEvalSet {
		t.Error("skill without eval set must be skipped")
	}
}

// TestRunEvolutionPass_Promotion exercises the full mutate -> eval -> select
// cycle: a failing skill is mutated by the fake model into a fixed version
// that beats the incumbent by >=5% with zero holdout regressions, so it is
// promoted, the incumbent is preserved as .bak, and the registry records the
// evolution.
func TestRunEvolutionPass_Promotion(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "adder", adderSkill)
	writeEvalSet(t, dir, "adder", adderEval)

	orig := evolveMutateFn
	evolveMutateFn = func(ctx context.Context, prompt string) (string, error) {
		return "```python\n" + adderFix + "```", nil
	}
	defer func() { evolveMutateFn = orig }()

	report, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: true, ReportPath: ""})
	if err != nil {
		t.Fatalf("RunEvolutionPass: %v", err)
	}
	if report.TotalPromote != 1 {
		t.Fatalf("expected 1 promotion, got %d (mutated: %+v)", report.TotalPromote, report.Mutated)
	}
	if len(report.Promoted) != 1 || report.Promoted[0] != "adder" {
		t.Errorf("promoted = %v, want [adder]", report.Promoted)
	}

	// Incumbent file now contains the fixed code; .bak has the original.
	src, err := os.ReadFile(filepath.Join(dir, "adder.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "return a + b") || strings.Contains(string(src), "+ 1") {
		t.Errorf("promoted source wrong: %s", src)
	}
	bak, err := os.ReadFile(filepath.Join(dir, "adder.py.bak"))
	if err != nil {
		t.Fatalf("missing .bak: %v", err)
	}
	if !strings.Contains(string(bak), "+ 1") {
		t.Errorf(".bak should preserve the incumbent: %s", bak)
	}

	// Registry tracks the evolution.
	regData, _ := os.ReadFile(filepath.Join(dir, "skills.json"))
	var reg SkillRegistry
	_ = json.Unmarshal(regData, &reg)
	if len(reg.Skills) != 1 || reg.Skills[0].EvolveCount != 1 {
		t.Errorf("registry evolution tracking wrong: %+v", reg.Skills)
	}
	if reg.Skills[0].EvalScore <= 0 {
		t.Errorf("eval score not recorded: %+v", reg.Skills[0])
	}
}

// TestRunEvolutionPass_RejectedNoGain: a mutation with no real improvement
// (same code, gain 0 < 5% threshold) is rejected and the incumbent file is
// left untouched with no .bak.
func TestRunEvolutionPass_RejectedNoGain(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "adder", adderSkill)
	writeEvalSet(t, dir, "adder", adderEval)

	orig := evolveMutateFn
	evolveMutateFn = func(ctx context.Context, prompt string) (string, error) {
		// Return the SAME buggy code: gain 0, no promotion.
		return "```python\n" + adderSkill + "```", nil
	}
	defer func() { evolveMutateFn = orig }()

	report, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: true, ReportPath: ""})
	if err != nil {
		t.Fatalf("RunEvolutionPass: %v", err)
	}
	if report.TotalPromote != 0 {
		t.Errorf("expected no promotion, got %d", report.TotalPromote)
	}
	if len(report.Rejected) != 1 {
		t.Errorf("expected 1 rejection, got %v", report.Rejected)
	}

	src, _ := os.ReadFile(filepath.Join(dir, "adder.py"))
	if !strings.Contains(string(src), "+ 1") {
		t.Error("incumbent must be unchanged after rejection")
	}
	if _, err := os.Stat(filepath.Join(dir, "adder.py.bak")); err == nil {
		t.Error(".bak must not exist for a rejected mutation")
	}
}

// TestRunEvolutionPass_ParseFailure: an unparseable mutator reply is a no-op.
func TestRunEvolutionPass_ParseFailure(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "adder", adderSkill)
	writeEvalSet(t, dir, "adder", adderEval)

	orig := evolveMutateFn
	evolveMutateFn = func(ctx context.Context, prompt string) (string, error) {
		return "I refuse to produce code", nil
	}
	defer func() { evolveMutateFn = orig }()

	report, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: true, ReportPath: ""})
	if err != nil {
		t.Fatalf("RunEvolutionPass: %v", err)
	}
	if report.TotalPromote != 0 || len(report.Rejected) != 1 {
		t.Errorf("expected 1 no-op rejection, got promote=%d reject=%v", report.TotalPromote, report.Rejected)
	}
	if !strings.Contains(report.Rejected[0], "no code block") {
		t.Errorf("rejection reason should mention parse failure: %v", report.Rejected)
	}
}

// TestRunEvolutionPass_Deprecation: a skill passing <30% of its eval cases
// is auto-deprecated in the registry.
func TestRunEvolutionPass_Deprecation(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "loser", `def f(_=None):
    return "wrong"
`)
	writeEvalSet(t, dir, "loser", `{
  "cases": [
    {"name": "a", "input": null, "expected": "right", "match": "contains", "train": true},
    {"name": "b", "input": null, "expected": "right", "match": "contains", "train": true},
    {"name": "c", "input": null, "expected": "right", "match": "contains", "train": true},
    {"name": "d", "input": null, "expected": "right", "match": "contains", "train": true}
  ]
}`)

	report, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: false, ReportPath: ""})
	if err != nil {
		t.Fatalf("RunEvolutionPass: %v", err)
	}
	if len(report.Deprecated) != 1 || report.Deprecated[0] != "loser" {
		t.Errorf("deprecated = %v, want [loser]", report.Deprecated)
	}

	regData, _ := os.ReadFile(filepath.Join(dir, "skills.json"))
	var reg SkillRegistry
	_ = json.Unmarshal(regData, &reg)
	if len(reg.Skills) != 1 || !reg.Skills[0].Deprecated {
		t.Errorf("registry deprecation flag not set: %+v", reg.Skills)
	}
}

// TestRunEvolutionPass_NoEvalSets: everything skipped, no promotion.
func TestRunEvolutionPass_NoEvalSets(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "plain", "def f():\n    return 1\n")

	report, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: true, ReportPath: ""})
	if err != nil {
		t.Fatalf("RunEvolutionPass: %v", err)
	}
	if len(report.Skipped) != 1 || report.TotalPromote != 0 {
		t.Errorf("skipped=%v promote=%d", report.Skipped, report.TotalPromote)
	}
}

// TestRunEvolutionPass_ReportWritten: the report file lands in the given
// path and contains the summary.
func TestRunEvolutionPass_ReportWritten(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "plain", "def f():\n    return 1\n")
	reportPath := filepath.Join(t.TempDir(), "report.md")

	if _, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: false, ReportPath: reportPath}); err != nil {
		t.Fatalf("RunEvolutionPass: %v", err)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	if !strings.Contains(string(data), "Skill Evolution Pass Report") {
		t.Errorf("report content missing header: %s", data)
	}
}
