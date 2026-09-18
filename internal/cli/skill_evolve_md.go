// skill_evolve_md.go - the `hakase skill evolve-md` CLI: one SkillOpt-style
// consolidation epoch over a markdown SKILL.md (plan Phase 1, SL-013).
//
// Flow: resolve skill by name -> load --tasks -> bootstrap model ->
// single-shot replay (or --agentic) with exact|rule|rubric judges ->
// reflect into bounded edits -> gate on the val slice with a fresh final
// replay -> stage to outputs/sleep/<ts>/ (report.md, report.json,
// diagnostics.json, proposed_SKILL.md, adopt.json) unless --dry-run ->
// optionally --adopt the staged proposal with hash+realpath verification.
//
// Exit codes mirror the skill CLI: 0 success/help, 1 runtime failure (or
// --adopt requested but nothing accepted), 2 usage error.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/skill"
	"amurru/hakase/internal/sleep"
)

func runSkillEvolveMD(args []string) int {
	fs := flag.NewFlagSet("evolve-md", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var skillName, tasksPath, reportPath, skillRoot, gateMetric string
	var editBudget int
	var mixedWeight float64
	var noRegression, greedy, dryRun, adopt, mutate, agentic bool
	fs.StringVar(&skillName, "skill", "", "markdown skill name to evolve (required)")
	fs.StringVar(&tasksPath, "tasks", "", "task file: {\"tasks\":[...]} or [...] (required)")
	fs.StringVar(&reportPath, "report", "", "write report.md here instead of staging to outputs/sleep/<ts>/")
	fs.StringVar(&skillRoot, "skill-root", "", "extra skill search root (appended to config skill dirs)")
	fs.StringVar(&gateMetric, "gate-metric", "mixed", "validation metric: hard | soft | mixed")
	fs.IntVar(&editBudget, "edit-budget", sleep.DefaultEditBudget, "max applied edits per epoch")
	fs.Float64Var(&mixedWeight, "gate-mixed-weight", sleep.DefaultMixedWeight, "soft weight for mixed metric")
	fs.BoolVar(&noRegression, "gate-no-regression", false, "block acceptance on any single val-task regression")
	fs.BoolVar(&greedy, "greedy", false, "accept edits without validation (explicit opt-out, recorded)")
	fs.BoolVar(&dryRun, "dry-run", false, "replay + report to stdout; stage and adopt nothing")
	fs.BoolVar(&adopt, "adopt", false, "adopt the staged proposal when accepted (hash-verified)")
	fs.BoolVar(&mutate, "mutate", true, "enable the reflector step (disable for evaluation-only scoring)")
	fs.BoolVar(&agentic, "agentic", false, "replay via an isolated runner with read-only tools (default: single-shot prompts)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "evolve-md takes no positional arguments\n\n")
		fs.Usage()
		return 2
	}
	if strings.TrimSpace(skillName) == "" || strings.TrimSpace(tasksPath) == "" {
		fmt.Fprintf(os.Stderr, "evolve-md requires --skill and --tasks\n\n")
		fs.Usage()
		return 2
	}
	if editBudget <= 0 {
		fmt.Fprintf(os.Stderr, "edit-budget must be positive\n")
		return 2
	}
	if adopt && reportPath != "" {
		fmt.Fprintf(os.Stderr, "--adopt needs a staging dir; drop --report or adopt the staged proposal separately\n")
		return 2
	}
	switch strings.ToLower(strings.TrimSpace(gateMetric)) {
	case "hard", "soft", "mixed":
	default:
		fmt.Fprintf(os.Stderr, "gate-metric must be hard, soft, or mixed\n")
		return 2
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "evolve-md: %v\n", err)
		return 1
	}
	var extraDirs []string
	if skillRoot != "" {
		extraDirs = append(extraDirs, skillRoot)
	}
	resolved := resolveEvolveMDSkill(cwd, extraDirs, skillName)
	if resolved == nil {
		fmt.Fprintf(os.Stderr, "evolve-md: skill %q not found\n", skillName)
		return 1
	}
	// Fail closed on disabled skills (plan SL-013/M6): CheckSkillEnabled
	// aborts on corrupt state too, unlike the lenient live-path check.
	if err := skill.CheckSkillEnabled(skill.KindMarkdown, resolved.Frontmatter.Name); err != nil {
		fmt.Fprintf(os.Stderr, "evolve-md: %v\n", err)
		return 1
	}
	liveBytes, err := os.ReadFile(resolved.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evolve-md: read live skill: %v\n", err)
		return 1
	}
	liveDoc := string(liveBytes)
	if _, _, ok := skill.SplitSkillDoc(liveDoc); !ok {
		fmt.Fprintf(os.Stderr, "evolve-md: live skill has no frontmatter\n")
		return 1
	}
	tasks, err := sleep.LoadMarkdownTasks(tasksPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evolve-md: %v\n", err)
		return 1
	}
	if len(tasks) == 0 {
		fmt.Fprintf(os.Stderr, "evolve-md: no tasks in %s\n", tasksPath)
		return 1
	}
	// M4 (SL-020): machine-generated task files (harvest/mine output) must
	// carry a valid human-review sidecar before any real-backend run — and
	// evolve-md always reaches the provider, including --dry-run.
	// Hand-written files are exempt (operator-authored).
	if err := sleep.RequireReviewed(tasksPath); err != nil {
		fmt.Fprintf(os.Stderr, "evolve-md: %v\n", err)
		return 1
	}

	// Replay and reflection both need the model, even evaluation-only runs
	// (audit B6 pattern: bootstrap or fail loudly, never degrade silently).
	if err := cronModelBootstrap(); err != nil {
		fmt.Fprintf(os.Stderr, "evolve-md requires a configured model: %v\n", err)
		return 1
	}
	call := func(ctx context.Context, prompt string) (string, error) {
		return hakaseagent.ModelPromptFn(ctx, prompt)
	}

	var run sleep.TargetRunner = sleep.SingleShotTarget(call)
	if agentic {
		ar, err := sleep.AgenticTarget(sleep.AgenticOpts{Model: currentModel, Timeout: sleep.DefaultReplayTimeout})
		if err != nil {
			fmt.Fprintf(os.Stderr, "evolve-md: agentic replay unavailable: %v\n", err)
			return 1
		}
		run = ar
	}
	rubric := sleep.DefaultRubricJudge(call)
	var reflect sleep.Reflector
	if mutate {
		reflect = evolveMDReflector(call, resolved.Frontmatter.Name, editBudget)
	}

	ctx := context.Background()
	result := sleep.Consolidate(ctx, run, rubric, tasks, liveDoc,
		sleep.ReplayOpts{Timeout: sleep.DefaultReplayTimeout},
		sleep.ConsolidateOpts{
			EditBudget: editBudget, GateMetric: gateMetric,
			MixedWeight: mixedWeight, GateNoRegression: noRegression,
			Greedy: greedy, Reflect: reflect,
		})

	fmt.Println(sleep.RenderSleepReport(skillName, result))
	fmt.Printf("Baseline: %.3f  Candidate: %.3f  Gate: %s (accepted=%v)\n",
		result.BaselineScore, result.CandidateScore, result.GateAction, result.Accepted)

	if dryRun {
		fmt.Println("dry-run: staged nothing.")
		return 0
	}
	if reportPath != "" {
		if err := os.MkdirAll(filepath.Dir(reportPath), 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "evolve-md: %v\n", err)
			return 1
		}
		_ = os.Chmod(filepath.Dir(reportPath), 0o700)
		if err := os.WriteFile(reportPath, []byte(sleep.RenderSleepReport(skillName, result)), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "evolve-md: %v\n", err)
			return 1
		}
		_ = os.Chmod(reportPath, 0o600)
		fmt.Printf("Report: %s\n", reportPath)
		return 0
	}
	stagingDir, err := sleep.StageConsolidation(filepath.Join("outputs", "sleep"), skillName, resolved.Path, result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evolve-md: stage: %v\n", err)
		return 1
	}
	fmt.Printf("Staged: %s\n", stagingDir)
	if !adopt {
		return 0
	}
	if !result.Accepted {
		fmt.Fprintf(os.Stderr, "evolve-md: --adopt requested but nothing accepted (gate: %s)\n", result.GateAction)
		return 1
	}
	adopted, backup, err := sleep.AdoptStaging(stagingDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evolve-md: adopt: %v\n", err)
		return 1
	}
	fmt.Printf("Adopted: %s (incumbent preserved as %s)\n", adopted, backup)
	return 0
}

// resolveEvolveMDSkill finds a markdown skill by frontmatter name
// (case-insensitive), mirroring resolveCronSkills.
func resolveEvolveMDSkill(cwd string, extraDirs []string, name string) *skill.MarkdownSkill {
	all := skill.DiscoverMarkdownSkills(cwd, extraDirs, func(string) {})
	for i, s := range all {
		if strings.EqualFold(s.Frontmatter.Name, name) {
			return &all[i]
		}
	}
	return nil
}

// evolveMDReflector wires the skill-package mutator prompt/parser to a
// ModelCaller with the edit budget applied.
func evolveMDReflector(call sleep.ModelCaller, skillName string, budget int) sleep.Reflector {
	return func(ctx context.Context, failures, _ []sleep.ScoredTask, skillBody string, _ int) ([]skill.TextEdit, string, error) {
		var mfails []skill.MarkdownFailure
		for _, f := range failures {
			mfails = append(mfails, skill.MarkdownFailure{Task: f.Task, Actual: f.Response, Error: f.FailReason})
		}
		// Skill name is informational in the prompt; the body carries the
		// identity. Frontmatter is frozen downstream regardless. The
		// standalone verb carries no optimizer memory or skill-aware
		// routing; those are sleep-cycle features (plan SL-031/SL-032).
		raw, err := call(ctx, skill.BuildMarkdownMutationPrompt(skillName, skillBody, mfails, budget, ""))
		if err != nil {
			return nil, "", err
		}
		edits, ok := skill.ParseMarkdownEdits(raw)
		if !ok {
			return nil, raw, nil
		}
		return edits, raw, nil
	}
}
