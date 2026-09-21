// evolver_test.go - tests for the Phase 3b/3c skill-evolution engine:
// evaluator viability, mutator parsing, the A/B promotion gate, regression
// rollback (.bak preservation), and auto-deprecation.
package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

	orig := EvolveMutateFn
	EvolveMutateFn = func(ctx context.Context, prompt string) (string, error) {
		return "```python\n" + adderFix + "```", nil
	}
	defer func() { EvolveMutateFn = orig }()

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

	orig := EvolveMutateFn
	EvolveMutateFn = func(ctx context.Context, prompt string) (string, error) {
		// Return the SAME buggy code: gain 0, no promotion.
		return "```python\n" + adderSkill + "```", nil
	}
	defer func() { EvolveMutateFn = orig }()

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

// TestRunEvolutionPass_ParseFailure: an unparseable mutator reply is an
// unmatched no-op (plan SL-003): it never reached the gate, so it lands in
// Unmatched, not Rejected.
func TestRunEvolutionPass_ParseFailure(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "adder", adderSkill)
	writeEvalSet(t, dir, "adder", adderEval)

	orig := EvolveMutateFn
	EvolveMutateFn = func(ctx context.Context, prompt string) (string, error) {
		return "I refuse to produce code", nil
	}
	defer func() { EvolveMutateFn = orig }()

	report, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: true, ReportPath: ""})
	if err != nil {
		t.Fatalf("RunEvolutionPass: %v", err)
	}
	if report.TotalPromote != 0 || len(report.Rejected) != 0 {
		t.Errorf("expected 0 promote/0 reject, got promote=%d reject=%v", report.TotalPromote, report.Rejected)
	}
	if len(report.Unmatched) != 1 {
		t.Fatalf("expected 1 unmatched, got %v", report.Unmatched)
	}
	if !strings.Contains(report.Unmatched[0], "no code block") {
		t.Errorf("unmatched reason should mention parse failure: %v", report.Unmatched)
	}
}

// TestRunEvolutionPass_HoldoutLeaked (SL-003): a skill with no holdout cases
// can never promote, even when the candidate fixes every train case. The
// comparison cannot detect overfitting, so the gate reports
// reject_unverified instead of certifying.
func TestRunEvolutionPass_HoldoutLeaked(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "adder", adderSkill)
	writeEvalSet(t, dir, "adder", `{
  "cases": [
    {"name": "t1", "input": {"a": 1, "b": 2}, "expected": "3", "match": "contains", "train": true},
    {"name": "t2", "input": {"a": 5, "b": 7}, "expected": "12", "match": "contains", "train": true}
  ]
}`)

	orig := EvolveMutateFn
	EvolveMutateFn = func(ctx context.Context, prompt string) (string, error) {
		return "```python\n" + adderFix + "```", nil
	}
	defer func() { EvolveMutateFn = orig }()

	report, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: true, ReportPath: ""})
	if err != nil {
		t.Fatalf("RunEvolutionPass: %v", err)
	}
	if report.TotalPromote != 0 {
		t.Fatalf("leaked holdout must never promote, got %d", report.TotalPromote)
	}
	if len(report.Mutated) != 1 || !report.Mutated[0].HoldoutLeaked {
		t.Fatalf("mutation record must flag holdout_leaked: %+v", report.Mutated)
	}
	if len(report.Rejected) != 1 || !strings.Contains(report.Rejected[0], "reject_unverified") {
		t.Errorf("rejected should carry reject_unverified: %v", report.Rejected)
	}
	src, _ := os.ReadFile(filepath.Join(dir, "adder.py"))
	if !strings.Contains(string(src), "+ 1") {
		t.Error("incumbent must be unchanged after leaked rejection")
	}
}

// TestRunEvolutionPass_GateNoRegression (SL-003): with GateNoRegression, a
// candidate that clears the train-gain threshold but breaks a
// previously-passing holdout case is rejected, even though the aggregate
// holdout score does not drop (lateral move the mean-only gate accepts).
func TestRunEvolutionPass_GateNoRegression(t *testing.T) {
	dir := t.TempDir()
	// Incumbent fails t1 + h1, passes t2 + h2. Candidate fixes t1 + h1 but
	// breaks h2: train 0.5 -> 1.0 (gain ok), holdout 0.5 -> 0.5 (mean ok,
	// but h2 regressed).
	writePySkill(t, dir, "swap", `def f(x):
    if x == "t1" or x == "h1":
        return "wrong"
    return "right"
`)
	writeEvalSet(t, dir, "swap", `{
  "cases": [
    {"name": "t1", "input": "t1", "expected": "right", "match": "exact", "train": true},
    {"name": "t2", "input": "other", "expected": "right", "match": "exact", "train": true},
    {"name": "h1", "input": "h1", "expected": "right", "match": "exact", "train": false},
    {"name": "h2", "input": "h2", "expected": "right", "match": "exact", "train": false}
  ]
}`)
	candidate := `def f(x):
    if x == "h2":
        return "wrong"
    return "right"
`
	orig := EvolveMutateFn
	EvolveMutateFn = func(ctx context.Context, prompt string) (string, error) {
		return "```python\n" + candidate + "```", nil
	}
	defer func() { EvolveMutateFn = orig }()

	report, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: true, ReportPath: "", GateNoRegression: true})
	if err != nil {
		t.Fatalf("RunEvolutionPass: %v", err)
	}
	if report.TotalPromote != 0 {
		t.Fatalf("holdout task regression must block promotion, got %d", report.TotalPromote)
	}
	if len(report.Rejected) != 1 || !strings.Contains(report.Rejected[0], "h2") {
		t.Errorf("rejection should name the regressed holdout case: %v", report.Rejected)
	}

	// Legacy mean-only gate still accepts the same lateral move (documents
	// the behavior change; the new default for markdown skills is strict).
	EvolveMutateFn = func(ctx context.Context, prompt string) (string, error) {
		return "```python\n" + candidate + "```", nil
	}
	report2, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: true, ReportPath: ""})
	if err != nil {
		t.Fatalf("RunEvolutionPass legacy: %v", err)
	}
	if report2.TotalPromote != 1 {
		t.Errorf("legacy gate should accept the lateral move, got %d (%v)", report2.TotalPromote, report2.Rejected)
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

// TestEvaluateSkill_FailuresCarryInput (SL-002): failure records carry the
// declared eval input so the mutator sees what actually failed, and the
// rendered prompt redacts secret-shaped values before they leave the process.
func TestEvaluateSkill_FailuresCarryInput(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "adder", adderSkill)
	writeEvalSet(t, dir, "adder", adderEval)

	res := evaluateSkill(dir, "adder", "adder.py")
	if len(res.TrainFail) != 2 {
		t.Fatalf("trainFail=%d want 2", len(res.TrainFail))
	}
	// First train case declares {"a":1,"b":2}: must survive to the failure.
	inputJSON, _ := json.Marshal(res.TrainFail[0].Input)
	if !strings.Contains(string(inputJSON), `"a":1`) {
		t.Errorf("failure input lost: %s", inputJSON)
	}

	prompt := buildMutationPrompt("adder", adderSkill, res.TrainFail)
	if !strings.Contains(prompt, `"a":1`) {
		t.Errorf("mutator prompt missing real input: %s", prompt)
	}

	// Secret-shaped input is redacted in the prompt but raw in the record.
	secretFail := []EvalFailure{{
		Name:     "leak",
		Input:    map[string]interface{}{"token": "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD"},
		Expected: "ok",
		Actual:   "nope",
	}}
	secretPrompt := buildMutationPrompt("s", "def f():\n    pass\n", secretFail)
	if strings.Contains(secretPrompt, "ghp_") {
		t.Errorf("secret survived in mutator prompt: %s", secretPrompt)
	}
	if !strings.Contains(secretPrompt, "[REDACTED:") {
		t.Errorf("no redaction token in mutator prompt: %s", secretPrompt)
	}
}

// TestRunEvolutionPass_RegistryPerms (SL-005/B3): the rewritten registry
// lands 0600 (buggy adder scores 0 -> deprecated -> registry rewrite).
func TestRunEvolutionPass_RegistryPerms(t *testing.T) {
	dir := t.TempDir()
	writePySkill(t, dir, "adder", adderSkill)
	writeEvalSet(t, dir, "adder", adderEval)

	if _, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: false, ReportPath: ""}); err != nil {
		t.Fatalf("RunEvolutionPass: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "skills.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("registry mode = %o, want 600", got)
		}
	}
}

// TestMutateAndSelect_EscapesSkillsDir (SL-005/B4): a skill file resolving
// outside the library (here via a symlink swap) is never overwritten, even
// when the candidate would win on scores.
func TestMutateAndSelect_EscapesSkillsDir(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	evilPath := filepath.Join(outside, "evil.py")
	if err := os.WriteFile(evilPath, []byte(adderSkill), 0o600); err != nil {
		t.Fatal(err)
	}
	// skills/<name>.py is a symlink pointing outside the library.
	linkDir := filepath.Join(dir, "linked")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evilPath, filepath.Join(linkDir, "evil.py")); err != nil {
		t.Fatal(err)
	}
	reg := SkillRegistry{Skills: []SkillMeta{{Name: "evil", Description: "t", FileName: "linked/evil.py"}}}
	regBytes, _ := json.MarshalIndent(reg, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "skills.json"), regBytes, 0o600)
	writeEvalSet(t, dir, "evil", adderEval)

	// Sanity: incumbent (read through the link) fails, candidate fixes all.
	orig := EvolveMutateFn
	EvolveMutateFn = func(ctx context.Context, prompt string) (string, error) {
		return "```python\n" + adderFix + "```", nil
	}
	defer func() { EvolveMutateFn = orig }()

	report, err := RunEvolutionPass(EvolutionOptions{SkillsDir: dir, Mutate: true, ReportPath: ""})
	if err != nil {
		t.Fatalf("RunEvolutionPass: %v", err)
	}
	if report.TotalPromote != 0 {
		t.Fatalf("escaping promotion must never happen, got %d", report.TotalPromote)
	}
	if len(report.Rejected) != 1 || !strings.Contains(report.Rejected[0], "escapes skills dir") {
		t.Errorf("rejection must cite the escape: %v", report.Rejected)
	}
	after, _ := os.ReadFile(evilPath)
	if !strings.Contains(string(after), "+ 1") {
		t.Error("outside file must be untouched")
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
