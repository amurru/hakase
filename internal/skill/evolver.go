// evolver.go - darwinian-evolver-style self-evolution of the Python skill
// library (plan Phase 3b/3c).
//
// Reimplements the tripartite evolution contract (organism / evaluator /
// mutator / selection) in Go over skills/skills.json, following the upstream
// imbue-ai/darwinian_evolver contract WITHOUT importing or wrapping it (the
// upstream is AGPL-3.0; this is an original implementation). The loop is
// cron-driven and headless: a pass evaluates every skill with an eval set,
// mutates failing skills via the configured model, promotes only mutations
// that beat the incumbent, and writes an auditable report to outputs/cron/
// for human review. No live self-modification.
package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// EvolveMutateFn is the model-backed mutation callback. It is set by the root
// package at startup (defaulting to the agent's ModelPromptFn). When nil,
// RunEvolutionPass skips the mutator step even when Mutate=true.
var EvolveMutateFn func(ctx context.Context, prompt string) (string, error)

// Evolution gate constants (plan Phase 3c).
const (
	// promoteMinGain is the minimum RELATIVE improvement a mutation must show
	// over the incumbent trainable score to be promoted (>= 5%).
	promoteMinGain = 0.05
	// deprecateHitRate is the eval hit rate below which a skill is
	// auto-deprecated (it fails more than 70% of its own cases).
	deprecateHitRate = 0.30
	// evolveEvalTimeout bounds one skill evaluation run.
	evolveEvalTimeout = 90 * time.Second
	// evolveMutationTimeout bounds one mutator LLM call.
	evolveMutationTimeout = 60 * time.Second
	// evolveMaxFailuresInPrompt caps how many failure cases the mutator sees.
	evolveMaxFailuresInPrompt = 5
)

// EvalFailure is one failed eval case: the input, the expected output, and
// what the skill actually produced. Trainable failures are shown to the
// mutator; holdout failures are only used to detect overfitting.
type EvalFailure struct {
	Name     string      `json:"name"`
	Input    interface{} `json:"input"`
	Expected string      `json:"expected"`
	Actual   string      `json:"actual"`
	Error    string      `json:"error,omitempty"`
}

// SkillEvalResult is the evaluation outcome of one skill.
type SkillEvalResult struct {
	Name         string
	FilePath     string
	HasEvalSet   bool
	TotalCases   int
	Trainable    int
	Holdout      int
	TrainPassed  int
	HoldPassed   int
	Viable       bool
	TrainFail    []EvalFailure // visible to the mutator
	HoldFail     []EvalFailure // held out from the mutator
	EvalSetError string        // parse/load error, when any
}

// TrainScore returns the trainable-case score in [0,1]. No trainable cases
// counts as 1.0 (nothing to fail).
func (r SkillEvalResult) TrainScore() float64 {
	if r.Trainable == 0 {
		return 1.0
	}
	return float64(r.TrainPassed) / float64(r.Trainable)
}

// HoldScore returns the holdout-case score in [0,1].
func (r SkillEvalResult) HoldScore() float64 {
	if r.Holdout == 0 {
		return 1.0
	}
	return float64(r.HoldPassed) / float64(r.Holdout)
}

// HitRate returns the fraction of all eval cases that passed.
func (r SkillEvalResult) HitRate() float64 {
	if r.TotalCases == 0 {
		return 1.0
	}
	return float64(r.TrainPassed+r.HoldPassed) / float64(r.TotalCases)
}

// MutationRecord is the outcome of one mutation attempt.
type MutationRecord struct {
	Skill        string
	Incumbent    float64
	Candidate    float64
	HoldIncumbent float64
	HoldCandidate float64
	Promoted     bool
	Reason       string
}

// EvolutionReport is the audit record of one evolution pass. It is written
// to outputs/cron/ for human review (mirrors hermes "human review, never
// direct commit").
type EvolutionReport struct {
	Timestamp    time.Time
	SkillsDir    string
	SkillsSeen   []string
	Skipped      []string // no eval set / not viable
	Mutated      []MutationRecord
	Promoted     []string
	Rejected     []string
	Deprecated   []string
	TotalPromote int
	Summary      string
}

// EvolutionOptions tunes a single pass.
type EvolutionOptions struct {
	// SkillsDir is the Python skill library directory (default "./skills").
	SkillsDir string
	// Mutate enables the mutator step. Without it the pass is
	// evaluation-only (score + deprecate + report).
	Mutate bool
	// ReportPath is where the markdown report is written (default
	// "outputs/cron/evolve-<timestamp>.md"). Empty disables report writing.
	ReportPath string
}

// skillPythonBin returns the absolute interpreter path used to run skill
// evaluations: the project .venv python when present, else the system
// python3. Unlike getVenvPython it has NO side effects (no venv creation).
// An absolute path is required because the evaluation runs with cmd.Dir set
// to the skill library, and exec resolves relative binary paths against
// cmd.Dir.
func skillPythonBin() string {
	abs, err := filepath.Abs(".")
	if err != nil {
		abs = "."
	}
	for _, p := range []string{
		filepath.Join(abs, ".venv", "bin", "python3"),
		filepath.Join(abs, ".venv", "Scripts", "python.exe"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if py, err := exec.LookPath("python3"); err == nil {
		return py
	}
	return "python3"
}

// evalRunnerScript is the stdlib-only python program that loads one skill
// module, runs each eval case against its entry point, captures the actual
// output (stdout + return value), compares it to the expected value, and
// writes a JSON list of per-case results. It is executed by
// skillPythonBin(); the process exit code does not encode pass/fail - the
// JSON output does.
const evalRunnerScript = `import contextlib, importlib.util, io, json, os, re, sys

def load_module(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

def find_entry(mod):
    for attr in ("main", "run", "generate", "solve"):
        if hasattr(mod, attr) and callable(getattr(mod, attr)):
            return getattr(mod, attr)
    for name in dir(mod):
        if name.startswith("_"):
            continue
        obj = getattr(mod, name)
        if callable(obj) and getattr(obj, "__module__", None) == mod.__name__:
            return obj
    return None

def invoke(entry, case_input):
    if isinstance(case_input, dict):
        return entry(**case_input)
    return entry(case_input)

def main():
    skill_path, eval_path, out_path = sys.argv[1], sys.argv[2], sys.argv[3]
    results = []
    try:
        # importlib rejects module names containing dots (it treats them as
        # package paths), so sanitize the derived name - especially
        # important for candidate files like "<name>.py.evolve-candidate".
        mod_name = os.path.splitext(os.path.basename(skill_path))[0]
        mod_name = re.sub(r"[^A-Za-z0-9_]", "_", mod_name)
        mod = load_module(skill_path, mod_name)
        entry = find_entry(mod)
    except Exception as e:
        results.append({"name": "", "train": True, "expected": "",
                        "match": "contains", "passed": False, "actual": "",
                        "error": "module load failed: %s: %s" % (type(e).__name__, e)})
        json.dump(results, open(out_path, "w"))
        return
    cases = json.load(open(eval_path)).get("cases", [])
    for c in cases:
        rec = {"name": c.get("name", ""), "train": bool(c.get("train")),
               "expected": str(c.get("expected", "")),
               "match": c.get("match", "contains"),
               "passed": False, "actual": "", "error": ""}
        if entry is None:
            rec["error"] = "no callable entry point"
            results.append(rec)
            continue
        buf = io.StringIO()
        try:
            with contextlib.redirect_stdout(buf):
                ret = invoke(entry, c.get("input"))
            actual = buf.getvalue().strip()
            if ret is not None and str(ret) != "None":
                actual = (actual + "\n" + str(ret)).strip()
            rec["actual"] = actual
        except Exception as e:
            rec["error"] = "%s: %s" % (type(e).__name__, e)
            results.append(rec)
            continue
        exp, mode = rec["expected"], rec["match"]
        if mode == "exact":
            rec["passed"] = (actual == exp)
        elif mode == "regex":
            try:
                rec["passed"] = re.search(exp, actual) is not None
            except re.error:
                rec["passed"] = False
        else:  # contains (default)
            rec["passed"] = exp != "" and exp in actual
        results.append(rec)
    json.dump(results, open(out_path, "w"))

main()
`

// evaluateSkill runs one skill's eval set and returns the outcome. A skill
// without an eval file yields HasEvalSet=false and is skipped by the
// evolver (plan risk table: skills without eval cases are skipped).
func evaluateSkill(skillsDir, name, fileName string) SkillEvalResult {
	return evaluateSkillAt(skillsDir, name, filepath.Join(skillsDir, fileName))
}

// evaluateSkillAt evaluates the skill source at an explicit file path
// against the eval set looked up by name in skillsDir. Used both for
// incumbents (skillsDir/<name>.py) and for mutation candidates, which may
// live outside the skills dir (temp files must not be path-mangled back
// into skillsDir).
func evaluateSkillAt(skillsDir, name, filePath string) SkillEvalResult {
	res := SkillEvalResult{
		Name:     name,
		FilePath: filePath,
		Viable:   false,
	}
	evalPath := filepath.Join(skillsDir, name+".eval.json")
	if _, err := os.Stat(evalPath); err != nil {
		return res // no eval set -> skipped
	}
	if _, err := LoadSkillEvalSet(evalPath); err != nil {
		res.HasEvalSet = true
		res.EvalSetError = err.Error()
		return res
	}
	res.HasEvalSet = true

	runnerPath := filepath.Join(os.TempDir(), fmt.Sprintf("hakase-evolve-runner-%d.py", time.Now().UnixNano()))
	defer os.Remove(runnerPath)
	if err := os.WriteFile(runnerPath, []byte(evalRunnerScript), 0o600); err != nil {
		res.EvalSetError = err.Error()
		return res
	}
	outPath := filepath.Join(os.TempDir(), fmt.Sprintf("hakase-evolve-out-%d.json", time.Now().UnixNano()))
	defer os.Remove(outPath)

	ctx, cancel := context.WithTimeout(context.Background(), evolveEvalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, skillPythonBin(), runnerPath, filePath, evalPath, outPath)
	cmd.Dir = skillsDir
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			res.EvalSetError = "eval timed out"
		} else {
			res.EvalSetError = fmt.Sprintf("runner failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return res
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		res.EvalSetError = "cannot read runner output"
		return res
	}
	var cases []struct {
		Name     string      `json:"name"`
		Train    bool        `json:"train"`
		Expected string      `json:"expected"`
		Match    string      `json:"match"`
		Passed   bool        `json:"passed"`
		Actual   string      `json:"actual"`
		Error    string      `json:"error"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		res.EvalSetError = "cannot parse runner output"
		return res
	}
	res.TotalCases = len(cases)
	if res.TotalCases == 0 {
		res.EvalSetError = "eval set has no cases"
		return res
	}
	ran := 0
	for _, c := range cases {
		if c.Train {
			res.Trainable++
			if c.Passed {
				res.TrainPassed++
			} else {
				res.TrainFail = append(res.TrainFail, EvalFailure{Name: c.Name, Input: nil, Expected: c.Expected, Actual: c.Actual, Error: c.Error})
			}
		} else {
			res.Holdout++
			if c.Passed {
				res.HoldPassed++
			} else {
				res.HoldFail = append(res.HoldFail, EvalFailure{Name: c.Name, Input: nil, Expected: c.Expected, Actual: c.Actual, Error: c.Error})
			}
		}
		if c.Error == "" {
			ran++
		}
	}
	// A skill is viable when the module loaded and at least one case
	// actually executed (a total runner failure implies a broken organism;
	// never evolve from a broken seed).
	res.Viable = ran > 0
	return res
}

// buildMutationPrompt renders the mutator prompt: current source plus a
// sample of trainable failures (input/expected/actual) and a request for a
// fixed implementation inside one python code fence.
func buildMutationPrompt(name, source string, failures []EvalFailure) string {
	var b strings.Builder
	b.WriteString("You are improving a saved Python skill in an agent skill library.\n\n")
	b.WriteString(fmt.Sprintf("Skill name: %s\n\nCurrent implementation:\n```python\n%s\n```\n\n", name, source))
	if len(failures) > 0 {
		b.WriteString("It fails these evaluation cases:\n")
		for i, f := range failures {
			b.WriteString(fmt.Sprintf("\nCase %d: %s\n", i+1, f.Name))
			inputJSON, _ := json.Marshal(f.Input)
			b.WriteString(fmt.Sprintf("  input:    %s\n", inputJSON))
			b.WriteString(fmt.Sprintf("  expected: %s\n", f.Expected))
			b.WriteString(fmt.Sprintf("  actual:   %s\n", f.Actual))
			if f.Error != "" {
				b.WriteString(fmt.Sprintf("  error:    %s\n", f.Error))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("Propose a fixed implementation. Keep the same function name and signature as the entry point. ")
	b.WriteString("Output ONLY a single ```python code block containing the complete new implementation, no prose.")
	return b.String()
}

// parseMutationReply extracts the python code from a mutator reply: the code
// between the first fenced block (```python / ```py / ```) and its closing
// fence. Operates line-based so "```py" cannot match inside "```python".
// Returns ok=false (no-op mutation) when no fenced block is present.
func parseMutationReply(raw string) (string, bool) {
	lines := strings.Split(raw, "\n")
	start := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "```") {
			continue
		}
		lang := strings.TrimPrefix(trimmed, "```")
		lang = strings.TrimSpace(lang)
		if lang == "python" || lang == "py" || lang == "" {
			start = i + 1
			break
		}
		// A fence with a non-python language label: keep scanning, but if
		// nothing better shows up we do not use it.
	}
	if start < 0 {
		return "", false
	}
	for j := start; j < len(lines); j++ {
		if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
			code := strings.TrimSpace(strings.Join(lines[start:j], "\n"))
			if code != "" {
				return code, true
			}
			return "", false
		}
	}
	return "", false
}

// RunEvolutionPass executes one full mutate -> eval -> select cycle over the
// skill library and writes the auditable report. It never removes or breaks
// existing skills: promoted mutations preserve the incumbent as
// <name>.py.bak, and any mutation that fails evaluation or the A/B gate is
// discarded.
func RunEvolutionPass(opts EvolutionOptions) (*EvolutionReport, error) {
	skillsDir := opts.SkillsDir
	if skillsDir == "" {
		skillsDir = "./skills"
	}
	report := &EvolutionReport{
		Timestamp: time.Now().UTC(),
		SkillsDir: skillsDir,
	}

	registryPath := filepath.Join(skillsDir, "skills.json")
	data, err := os.ReadFile(registryPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", registryPath, err)
	}
	var registry SkillRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", registryPath, err)
	}
	if len(registry.Skills) == 0 {
		report.Summary = "no skills in registry"
		writeEvolutionReport(report, opts.ReportPath)
		return report, nil
	}

	registryChanged := false
	for i := range registry.Skills {
		meta := &registry.Skills[i]
		report.SkillsSeen = append(report.SkillsSeen, meta.Name)

		res := evaluateSkill(skillsDir, meta.Name, meta.FileName)
		if !res.HasEvalSet {
			report.Skipped = append(report.Skipped, fmt.Sprintf("%s (no eval set)", meta.Name))
			continue
		}
		if res.EvalSetError != "" {
			report.Skipped = append(report.Skipped, fmt.Sprintf("%s (eval error: %s)", meta.Name, res.EvalSetError))
			continue
		}
		if !res.Viable {
			report.Skipped = append(report.Skipped, fmt.Sprintf("%s (not viable - broken seed)", meta.Name))
			continue
		}

		meta.EvalScore = roundScore(res.HitRate())
		if res.HitRate() < deprecateHitRate && !meta.Deprecated {
			meta.Deprecated = true
			registryChanged = true
			report.Deprecated = append(report.Deprecated, meta.Name)
		}

		if opts.Mutate && len(res.TrainFail) > 0 && EvolveMutateFn != nil {
			rec := MutationRecord{
				Skill:         meta.Name,
				Incumbent:     res.TrainScore(),
				HoldIncumbent: res.HoldScore(),
			}
			promoted, reason := mutateAndSelect(skillsDir, meta, res)
			rec.Promoted = promoted
			rec.Reason = reason
			if promoted {
				rec.Candidate = roundScore(1.0) // recomputed below via re-eval
				meta.EvolveCount++
				meta.LastEvolvedAt = time.Now().UTC().Format(time.RFC3339)
				registryChanged = true
				report.Promoted = append(report.Promoted, meta.Name)
				report.TotalPromote++
				// Re-evaluate the promoted skill to record its real score.
				newRes := evaluateSkill(skillsDir, meta.Name, meta.FileName)
				rec.Candidate = roundScore(newRes.TrainScore())
				rec.HoldCandidate = roundScore(newRes.HoldScore())
				meta.EvalScore = roundScore(newRes.HitRate())
			} else {
				report.Rejected = append(report.Rejected, fmt.Sprintf("%s (%s)", meta.Name, reason))
			}
			report.Mutated = append(report.Mutated, rec)
		}
	}

	if registryChanged {
		if regBytes, err := json.MarshalIndent(registry, "", "  "); err == nil {
			_ = os.WriteFile(registryPath, regBytes, 0o644)
		}
	}

	report.Summary = fmt.Sprintf("evaluated %d skill(s); promoted %d, rejected %d, deprecated %d, skipped %d",
		len(report.SkillsSeen), report.TotalPromote, len(report.Rejected), len(report.Deprecated), len(report.Skipped))
	if opts.Mutate {
		report.Summary += "; mutations enabled"
	} else {
		report.Summary += "; evaluation-only"
	}

	writeEvolutionReport(report, opts.ReportPath)
	return report, nil
}

// mutateAndSelect runs the mutator on a failing skill and, if the A/B gate
// passes, promotes the candidate. Returns (promoted, reason).
func mutateAndSelect(skillsDir string, meta *SkillMeta, incumbent SkillEvalResult) (bool, string) {
	source, err := os.ReadFile(incumbent.FilePath)
	if err != nil {
		return false, fmt.Sprintf("cannot read source: %v", err)
	}

	failures := incumbent.TrainFail
	if len(failures) > evolveMaxFailuresInPrompt {
		failures = failures[:evolveMaxFailuresInPrompt]
	}
	prompt := buildMutationPrompt(meta.Name, string(source), failures)

	ctx, cancel := context.WithTimeout(context.Background(), evolveMutationTimeout)
	defer cancel()
	raw, err := EvolveMutateFn(ctx, prompt)
	if err != nil {
		return false, fmt.Sprintf("mutator call failed: %v", err)
	}
	code, ok := parseMutationReply(raw)
	if !ok {
		return false, "mutator reply had no code block (no-op)"
	}

	// Write the candidate to a temp file with a .py extension (importlib
	// only builds specs for recognized source extensions; a suffix like
	// ".evolve-candidate" would yield a nil spec).
	candidatePath := filepath.Join(os.TempDir(), fmt.Sprintf("hakase-evolve-candidate-%d-%s.py", time.Now().UnixNano(), meta.Name))
	if err := os.WriteFile(candidatePath, []byte(code), 0o644); err != nil {
		return false, fmt.Sprintf("cannot write candidate: %v", err)
	}
	defer os.Remove(candidatePath)

	// Evaluate the candidate under a temp name so the incumbent file is
	// untouched during scoring.
	candidateRes := SkillEvalResult{
		Name:     meta.Name + "-candidate",
		FilePath: candidatePath,
	}
	{
		// Reuse the real evaluator by pointing it at the candidate file.
		tmpEval := evaluateSkillFile(skillsDir, meta.Name, candidatePath)
		candidateRes = tmpEval
	}
	if !candidateRes.HasEvalSet || candidateRes.EvalSetError != "" || !candidateRes.Viable {
		return false, fmt.Sprintf("candidate not viable (hasEvalSet=%v, err=%q, viable=%v, trainPassed=%d/%d)",
			candidateRes.HasEvalSet, candidateRes.EvalSetError, candidateRes.Viable, candidateRes.TrainPassed, candidateRes.Trainable)
	}

	// A/B gate (plan Phase 3c): promote only when the candidate beats the
	// incumbent by >=5% on trainable cases AND shows zero regressions on
	// holdout cases.
	gain := candidateRes.TrainScore() - incumbent.TrainScore()
	if gain < promoteMinGain {
		return false, fmt.Sprintf("candidate gain %.2f < %.2f threshold", gain, promoteMinGain)
	}
	if candidateRes.HoldScore() < incumbent.HoldScore()-1e-9 {
		return false, fmt.Sprintf("candidate holdout regression: %.2f -> %.2f", incumbent.HoldScore(), candidateRes.HoldScore())
	}

	// Promote: preserve the incumbent as .bak, install the candidate.
	bakPath := incumbent.FilePath + ".bak"
	_ = os.WriteFile(bakPath, source, 0o644)
	if err := os.WriteFile(incumbent.FilePath, []byte(code), 0o644); err != nil {
		return false, fmt.Sprintf("cannot install candidate: %v", err)
	}
	return true, fmt.Sprintf("gain %.2f, no holdout regressions", gain)
}

// evaluateSkillFile evaluates a skill source file directly (used for
// candidate scoring before promotion). The eval set is looked up by skill
// name in skillsDir; the candidate file may live outside it.
func evaluateSkillFile(skillsDir, name, filePath string) SkillEvalResult {
	return evaluateSkillAt(skillsDir, name, filePath)
}

// roundScore rounds a 0-1 score to 4 decimals for report readability.
func roundScore(s float64) float64 {
	return float64(int64(s*10000+0.5)) / 10000.0
}

// writeEvolutionReport renders the pass report as markdown and writes it to
// ReportPath (default outputs/cron/evolve-<ts>.md). Empty ReportPath
// disables writing.
func writeEvolutionReport(report *EvolutionReport, path string) {
	if path == "" {
		return
	}
	if !strings.HasSuffix(path, ".md") {
		path += ".md"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Skill Evolution Pass Report\n\n"))
	b.WriteString(fmt.Sprintf("- **Time:** %s\n", report.Timestamp.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- **Skills dir:** %s\n", report.SkillsDir))
	b.WriteString(fmt.Sprintf("- **Summary:** %s\n\n", report.Summary))

	if len(report.Promoted) > 0 {
		b.WriteString("## Promoted mutations\n\n")
		for _, m := range report.Mutated {
			if m.Promoted {
				b.WriteString(fmt.Sprintf("- `%s`: train %.2f -> %.2f (holdout %.2f -> %.2f) - %s\n",
					m.Skill, m.Incumbent, m.Candidate, m.HoldIncumbent, m.HoldCandidate, m.Reason))
			}
		}
		b.WriteString("\nIncumbent sources preserved as `<name>.py.bak`.\n\n")
	}
	if len(report.Rejected) > 0 {
		b.WriteString("## Rejected mutations\n\n")
		for _, r := range report.Rejected {
			b.WriteString(fmt.Sprintf("- %s\n", r))
		}
		b.WriteString("\n")
	}
	if len(report.Deprecated) > 0 {
		b.WriteString("## Auto-deprecated skills (eval hit rate < 30%)\n\n")
		for _, d := range report.Deprecated {
			b.WriteString(fmt.Sprintf("- `%s`\n", d))
		}
		b.WriteString("\n")
	}
	if len(report.Skipped) > 0 {
		b.WriteString("## Skipped skills\n\n")
		for _, s := range report.Skipped {
			b.WriteString(fmt.Sprintf("- %s\n", s))
		}
		b.WriteString("\n")
	}
	if len(report.SkillsSeen) == 0 {
		b.WriteString("No skills evaluated.\n")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		_ = os.WriteFile(path, []byte(b.String()), 0o644)
	}
}
