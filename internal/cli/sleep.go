// sleep.go - the `hakase sleep` CLI (plan Phase 2): the SkillOpt-Sleep
// offline self-improvement loop. Phase 2 verbs:
//
//	harvest  walk sessions into a redacted digest file (SL-020)
//	review   sign the review sidecar for a tasks/digest file (M4 flow)
//
// run/dry-run/adopt/status/schedule arrive with the cycle (SL-022) and
// cron/config wiring (SL-023). Exit codes mirror the skill CLI: 0 success,
// 1 runtime failure, 2 usage error.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/session"
	"amurru/hakase/internal/sleep"
)

// RunSleepCLI dispatches the `hakase sleep` subcommand tree.
func RunSleepCLI(args []string) int {
	if len(args) == 0 {
		sleepCLIUsage()
		return 2
	}
	switch args[0] {
	case "harvest":
		return runSleepHarvest(args[1:])
	case "review":
		return runSleepReview(args[1:])
	case "run":
		return runSleepRun(args[1:], false)
	case "dry-run":
		return runSleepRun(args[1:], true)
	case "adopt":
		return runSleepAdopt(args[1:])
	case "status":
		return runSleepStatus(args[1:])
	case "schedule":
		return runSleepSchedule(args[1:])
	case "evalkit":
		return runSleepEvalkit(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown sleep subcommand %q\n\n", args[0])
		sleepCLIUsage()
		return 2
	}
}

func sleepCLIUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase sleep <subcommand>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  harvest  walk sessions into a redacted digest file for mining")
	fmt.Fprintln(os.Stderr, "  review   sign the human-review sidecar for a tasks/digest file")
	fmt.Fprintln(os.Stderr, "  run      one full night: harvest+mine (or --tasks), consolidate, stage")
	fmt.Fprintln(os.Stderr, "  dry-run  read-only counts: harvest+mine, no model call, no writes")
	fmt.Fprintln(os.Stderr, "  adopt    install a staged proposal (hash+realpath verified)")
	fmt.Fprintln(os.Stderr, "  status   show the sleep checkpoint, model identity, recent nights")
	fmt.Fprintln(os.Stderr, "  schedule create a CLI-only native cron job that runs one night per schedule")
	fmt.Fprintln(os.Stderr, "  evalkit  paired A/B of two skill docs over a task manifest (McNemar + bootstrap CI)")
}

// runSleepHarvest walks the session store and writes the digest file.
func runSleepHarvest(args []string) int {
	fs := flag.NewFlagSet("harvest", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var out, sessionsDir, auditLog, project, checkpoint string
	var includeArchived, includeAuditArgs, includeAuditOutputs bool
	var lookbackHours, maxSessions int
	var redactSecrets, allowUnredacted bool
	fs.StringVar(&out, "out", "", "output digest file (default outputs/sleep/harvest-<ts>.json)")
	fs.StringVar(&sessionsDir, "sessions-dir", session.Dir, "session store directory")
	fs.StringVar(&auditLog, "audit-log", "logs/exec-audit.jsonl", "exec audit log to join (tool names); empty disables")
	fs.StringVar(&project, "project", "", "only sessions bound to this project id")
	fs.BoolVar(&includeArchived, "archived", false, "include archived sessions")
	fs.IntVar(&lookbackHours, "lookback-hours", 0, "first-run window in hours (default 72; ignored with --checkpoint)")
	fs.StringVar(&checkpoint, "checkpoint", "", "harvest only turns at/after this RFC3339 instant (last_harvest)")
	fs.BoolVar(&includeAuditArgs, "include-audit-args", false, "join redacted audit command lines (default: tool names only)")
	fs.BoolVar(&includeAuditOutputs, "include-audit-outputs", false, "reserved: the audit log records no outputs today")
	fs.IntVar(&maxSessions, "max-sessions", sleep.DefaultMaxSessions, "cap on sessions harvested (newest first)")
	fs.BoolVar(&redactSecrets, "redact-secrets", true, "redact secret-shaped strings before writing (SL-004)")
	fs.BoolVar(&allowUnredacted, "allow-unredacted", false, "explicit opt-out required when --redact-secrets=false")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "harvest takes no positional arguments\n\n")
		fs.Usage()
		return 2
	}
	unredacted, warning, err := sleep.ResolveRedaction(redactSecrets, allowUnredacted)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep harvest: %v\n", err)
		return 1
	}
	if warning != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}
	var checkpointTs time.Time
	if checkpoint != "" {
		checkpointTs, err = time.Parse(time.RFC3339, checkpoint)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sleep harvest: --checkpoint must be RFC3339: %v\n", err)
			return 2
		}
	}
	file, err := sleep.Harvest(sleep.HarvestOpts{
		SessionsDir:         sessionsDir,
		AuditLogPath:        auditLog,
		ProjectID:           project,
		IncludeArchived:     includeArchived,
		LookbackHours:       lookbackHours,
		Checkpoint:          checkpointTs,
		IncludeAuditArgs:    includeAuditArgs,
		IncludeAuditOutputs: includeAuditOutputs,
		Unredacted:          unredacted,
		MaxSessions:         maxSessions,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep harvest: %v\n", err)
		return 1
	}
	if out == "" {
		out = "outputs/sleep/harvest-" + file.GeneratedAt.Format("20060102-150405") + ".json"
	}
	if err := sleep.WriteHarvestFile(out, file); err != nil {
		fmt.Fprintf(os.Stderr, "sleep harvest: write: %v\n", err)
		return 1
	}
	turns := 0
	for _, s := range file.Sessions {
		turns += s.TurnCount
	}
	fmt.Printf("Harvested %d sessions (%d turns) since %s -> %s\n",
		len(file.Sessions), turns, file.Cutoff.Format(time.RFC3339), out)
	fmt.Println("Review the digests before any real-backend use; mined task files require `hakase sleep review`.")
	return 0
}

// runSleepAdopt installs one staged proposal (hash+realpath verified, .bak
// kept). This is the explicit human gate for hand-written skills.
func runSleepAdopt(args []string) int {
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var dir string
	fs.StringVar(&dir, "dir", "", "staging directory holding adopt.json (required)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if dir == "" || fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "adopt requires --dir <staging dir>\n\n")
		fs.Usage()
		return 2
	}
	installed, backup, err := sleep.AdoptStaging(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep adopt: %v\n", err)
		return 1
	}
	fmt.Printf("Adopted: %s (incumbent preserved as %s)\n", installed, backup)
	return 0
}

// runSleepStatus prints the checkpoint, model identity and recent nights.
func runSleepStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var statePath string
	fs.StringVar(&statePath, "state", sleep.DefaultStatePath, "sleep state file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	st, note, err := sleep.LoadSleepState(statePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep status: %v\n", err)
		return 1
	}
	if note != "" {
		fmt.Println("note:", note)
	}
	if st.LastHarvest.IsZero() {
		fmt.Println("No sleep state yet; the first night will harvest the default lookback window.")
	} else {
		fmt.Printf("Last harvest checkpoint: %s\n", st.LastHarvest.Format(time.RFC3339))
	}
	if st.LastModelKey != "" {
		fmt.Printf("Last model: %s\n", st.LastModelKey)
	}
	nights := st.Nights
	if len(nights) > 10 {
		nights = nights[len(nights)-10:]
	}
	for _, n := range nights {
		line := fmt.Sprintf("- %s  %s  sessions=%d tasks=%d tokens=%d",
			n.StartedAt.Format("2006-01-02 15:04"), n.Outcome, n.Sessions, n.Tasks, n.Tokens)
		if n.AbortReason != "" {
			line += "  (" + n.AbortReason + ")"
		}
		if n.StagingDir != "" {
			line += "  " + n.StagingDir
		}
		fmt.Println(line)
	}
	return 0
}

// runSleepRun executes one night (full run) or read-only counts (dry-run).
func runSleepRun(args []string, dryRun bool) int {
	verb := "run"
	if dryRun {
		verb = "dry-run"
	}
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var tasksFile, sessionsDir, auditLog, project, checkpoint, skillName, skillRoot string
	var statePath, outputDir string
	var includeArchived, includeAuditArgs bool
	var lookbackHours, maxSessions, maxTasks int
	var trainFrac, valFrac, testFrac float64
	var seed int64
	var redactSecrets, allowUnredacted bool
	var editBudget int
	var gateMetric string
	var mixedWeight float64
	var noRegression, greedy, fanOut, autoAdopt, agentic bool
	var perTaskTimeout, perNightTimeout int
	var maxTokens int
	var ackModelChange bool
	var lrScheduler, knowledgeDir string
	var lrFloor, lrHorizon, recallK int
	var skillAware, allowSharedJudge, dreamRollouts, progress bool
	var dreamFactor float64
	fs.StringVar(&tasksFile, "tasks", "", "seed task file (reviewed; default: harvest+mine)")
	fs.StringVar(&sessionsDir, "sessions-dir", session.Dir, "session store directory")
	fs.StringVar(&auditLog, "audit-log", "logs/exec-audit.jsonl", "exec audit log to join; empty disables")
	fs.StringVar(&project, "project", "", "only sessions bound to this project id")
	fs.BoolVar(&includeArchived, "archived", false, "include archived sessions")
	fs.IntVar(&lookbackHours, "lookback-hours", 0, "first-run window in hours (default 72; ignored with --checkpoint)")
	fs.StringVar(&checkpoint, "checkpoint", "", "harvest only turns at/after this RFC3339 instant")
	fs.BoolVar(&includeAuditArgs, "include-audit-args", false, "join redacted audit command lines")
	fs.IntVar(&maxSessions, "max-sessions", sleep.DefaultMaxSessions, "cap on sessions harvested")
	fs.IntVar(&maxTasks, "max-tasks", sleep.DefaultMaxTasksPerNight, "cap on tasks mined per night")
	fs.Float64Var(&trainFrac, "train-frac", 0, "train fraction (default 0.6)")
	fs.Float64Var(&valFrac, "val-frac", 0, "val fraction (default 0.2)")
	fs.Float64Var(&testFrac, "test-frac", 0, "test fraction (default 0 = legacy)")
	fs.Int64Var(&seed, "seed", sleep.DefaultSplitSeed, "split shuffle seed")
	fs.BoolVar(&redactSecrets, "redact-secrets", true, "redact secret-shaped strings (SL-004)")
	fs.BoolVar(&allowUnredacted, "allow-unredacted", false, "explicit opt-out required when --redact-secrets=false")
	fs.StringVar(&skillName, "skill", "", "evolve only this skill hint (fail-closed)")
	fs.StringVar(&skillRoot, "skill-root", "", "extra skill search root (disambiguates collisions)")
	fs.BoolVar(&fanOut, "fan-out", false, "consolidate every resolvable skill group")
	fs.BoolVar(&autoAdopt, "auto-adopt", false, "install accepted proposals for managed skills only (M5)")
	fs.IntVar(&editBudget, "edit-budget", 0, "max applied edits per group (default: config or 4)")
	fs.StringVar(&gateMetric, "gate-metric", "", "validation metric: hard | soft | mixed (default: config or mixed)")
	fs.Float64Var(&mixedWeight, "gate-mixed-weight", 0, "soft weight for mixed metric (default: config or 0.5)")
	fs.BoolVar(&noRegression, "gate-no-regression", false, "block acceptance on any single val-task regression")
	fs.BoolVar(&greedy, "greedy", false, "accept edits without validation (explicit opt-out, recorded)")
	fs.IntVar(&perTaskTimeout, "per-task-timeout", 0, "per-task replay budget in seconds (default: config or 120)")
	fs.IntVar(&perNightTimeout, "per-night-timeout", 0, "whole-night budget in seconds (default: config or 3600)")
	fs.IntVar(&maxTokens, "max-tokens", 0, "estimated token budget for the night (default: config or 2000000)")
	fs.BoolVar(&agentic, "agentic", false, "replay via an isolated runner with read-only tools")
	fs.BoolVar(&ackModelChange, "acknowledge-model-change", false, "confirm the provider/model changed since the last night")
	fs.StringVar(&lrScheduler, "lr-scheduler", "", "edit-budget schedule: constant | linear | cosine (default: config or constant)")
	fs.IntVar(&lrFloor, "lr-floor", 0, "floor the schedule decays toward (default: config or 0)")
	fs.IntVar(&lrHorizon, "lr-horizon", 0, "nights over which the schedule decays (default: config or 0 = never)")
	fs.BoolVar(&skillAware, "skill-aware", false, "skill-aware reflection: EXECUTION_LAPSE reminders bypass the gate into the appendix (SL-032)")
	fs.BoolVar(&allowSharedJudge, "allow-shared-judge", false, "accept a judge that resolves to the target model (reduced independence)")
	fs.IntVar(&recallK, "recall-k", 0, "recall top-k knowledge notes into the reflector context (SL-033)")
	fs.StringVar(&knowledgeDir, "knowledge-dir", "", "knowledge base directory for recall (default: config knowledge_dir)")
	fs.BoolVar(&dreamRollouts, "dream-rollouts", false, "synthesize contrastive dream tasks (SL-033; requires --dream-factor > 0)")
	fs.Float64Var(&dreamFactor, "dream-factor", 0, "dream tasks as a fraction of train tasks")
	fs.BoolVar(&progress, "progress", false, "print one progress line per night milestone as the night runs")
	fs.StringVar(&statePath, "state", "", "sleep state file (default .hakase/sleep-state.json)")
	fs.StringVar(&outputDir, "output-dir", "", "staging output directory (default outputs/sleep)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "%s takes no positional arguments\n\n", verb)
		fs.Usage()
		return 2
	}

	unredacted, warning, err := sleep.ResolveRedaction(redactSecrets, allowUnredacted)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep %s: %v\n", verb, err)
		return 1
	}
	if warning != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}
	var checkpointTs time.Time
	if checkpoint != "" {
		checkpointTs, err = time.Parse(time.RFC3339, checkpoint)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sleep %s: --checkpoint must be RFC3339: %v\n", verb, err)
			return 2
		}
	}

	// Precedence: explicit flags > env overrides > file config > defaults.
	// Dry-run loads config without bootstrapping a model.
	var cfg *config.Config
	if currentConfig != nil {
		cfg = currentConfig
	} else {
		cfg, err = config.LoadConfig(config.ResolveConfigPath("config.json"))
		if err != nil {
			cfg = &config.Config{} // config-less dry-run uses pure defaults
		}
	}
	envOverrides, envWarnings, err := sleep.LoadSleepEnvFromOS()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep %s: %v\n", verb, err)
		return 1
	}
	for _, w := range envWarnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	opts, err := buildSleepCycleOpts(cfg, envOverrides)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep %s: %v\n", verb, err)
		return 1
	}
	opts.DryRun = dryRun
	opts.SeedTasksPath = tasksFile
	opts.Harvest.Unredacted = unredacted
	opts.Harvest.Checkpoint = checkpointTs
	opts.Harvest.ProjectID = project
	opts.Harvest.IncludeArchived = includeArchived
	opts.Greedy = greedy
	opts.SkillName = skillName
	opts.SkillRoot = skillRoot

	// Explicit flags win over env/file (fs.Visit reports only set flags).
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["sessions-dir"] {
		opts.Harvest.SessionsDir = sessionsDir
	}
	if set["audit-log"] {
		opts.Harvest.AuditLogPath = auditLog
	}
	if set["include-audit-args"] {
		opts.Harvest.IncludeAuditArgs = includeAuditArgs
	}
	if set["max-sessions"] {
		opts.Harvest.MaxSessions = maxSessions
	}
	if set["lookback-hours"] {
		opts.Harvest.LookbackHours = lookbackHours
	}
	if set["max-tasks"] {
		opts.Mine.MaxTasksPerNight = maxTasks
	}
	if set["train-frac"] {
		opts.Mine.TrainFraction = trainFrac
	}
	if set["val-frac"] {
		opts.Mine.ValFraction = valFrac
	}
	if set["test-frac"] {
		opts.Mine.TestFraction = testFrac
	}
	if set["seed"] {
		opts.Mine.Seed = seed
	}
	if set["edit-budget"] {
		opts.EditBudget = editBudget
	}
	if set["gate-metric"] {
		opts.GateMetric = gateMetric
	}
	if set["gate-mixed-weight"] {
		opts.MixedWeight = mixedWeight
	}
	if set["gate-no-regression"] {
		opts.GateNoRegression = noRegression
	}
	if set["fan-out"] {
		opts.FanOut = fanOut
	}
	if set["auto-adopt"] {
		opts.AutoAdopt = autoAdopt
	}
	if set["per-task-timeout"] {
		opts.PerTaskTimeout = time.Duration(perTaskTimeout) * time.Second
	}
	if set["per-night-timeout"] {
		opts.PerNightTimeout = time.Duration(perNightTimeout) * time.Second
	}
	if set["max-tokens"] {
		opts.MaxTokensPerNight = maxTokens
	}
	if set["state"] {
		opts.StatePath = statePath
	}
	if set["output-dir"] {
		opts.OutputDir = outputDir
	}
	if set["acknowledge-model-change"] {
		opts.AcknowledgeModelChange = ackModelChange
	}
	if set["lr-scheduler"] {
		opts.LRScheduler = lrScheduler
	}
	if set["lr-floor"] {
		opts.LRFloor = lrFloor
	}
	if set["lr-horizon"] {
		opts.LRHorizon = lrHorizon
	}
	if set["skill-aware"] {
		opts.SkillAware = skillAware
	}
	if set["allow-shared-judge"] {
		opts.AllowSharedJudge = allowSharedJudge
	}
	if set["recall-k"] {
		opts.RecallK = recallK
	}
	if set["knowledge-dir"] {
		opts.KnowledgeDir = knowledgeDir
	}
	if set["dream-rollouts"] {
		opts.DreamRollouts = dreamRollouts
	}
	if set["dream-factor"] {
		opts.DreamFactor = dreamFactor
	}
	if progress {
		opts.Progress = func(line string) {
			fmt.Fprintf(os.Stderr, "[sleep %s] %s\n", verb, line)
		}
	}

	// Dry-run stays provider-free; a real run bootstraps the model or exits
	// loudly (audit B6 pattern).
	if !dryRun {
		if err := cronModelBootstrap(); err != nil {
			fmt.Fprintf(os.Stderr, "sleep run requires a configured model: %v\n", err)
			return 1
		}
		opts.Call = func(ctx context.Context, prompt string) (string, error) {
			return hakaseagent.ModelPromptFn(ctx, prompt)
		}
		// Judge separation (plan H2): a configured judge model gets its own
		// caller; sharing the target model requires --allow-shared-judge.
		judgeCall, judgeKey, jerr := sleepJudgeCaller(cfg)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "sleep %s: judge model: %v\n", verb, jerr)
			return 1
		}
		if judgeCall == nil {
			if !allowSharedJudge {
				fmt.Fprintf(os.Stderr, "sleep %s: no separate judge model configured (set sleep.judge_model, or summary_model as the cheap secondary); judge and target would both use %s. Re-run with --allow-shared-judge to accept the reduced independence.\n",
					verb, sleepModelKey())
				return 1
			}
		} else {
			opts.JudgeCall = judgeCall
			opts.JudgeModelKey = judgeKey
		}
		if agentic {
			ar, err := sleep.AgenticTarget(sleep.AgenticOpts{Model: currentModel, Timeout: opts.PerTaskTimeout})
			if err != nil {
				fmt.Fprintf(os.Stderr, "sleep run: agentic replay unavailable: %v\n", err)
				return 1
			}
			opts.Run = ar
		}
		opts.ModelKey = sleepModelKey()
	}

	res, err := sleep.RunCycle(context.Background(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep %s: %v\n", verb, err)
		return 1
	}
	fmt.Print(sleep.RenderNightReport(res))
	if dryRun {
		return 0
	}
	if res.StagingDir != "" {
		fmt.Printf("Staged: %s (review proposals, then `hakase sleep adopt --dir <skill dir>`)\n", res.StagingDir)
	}
	return 0
}

// sleepModelKey identifies the provider/model that produced the night, for
// the state file's hard-pause rule.
func sleepModelKey() string {
	name := currentModelName
	if name == "" {
		name = "unknown"
	}
	provider := "unknown"
	if currentConfig != nil && currentConfig.Provider != "" {
		provider = currentConfig.Provider
	}
	return provider + "/" + name
}

// sleepJudgeCaller builds the separate judge model seam (plan H2): an
// explicit sleep.judge_backend/judge_model, else the cheap secondary
// (summary model) when configured and distinct from the target. Returns
// (nil, "", nil) when no distinct judge exists - the caller decides whether
// sharing the target model is acceptable.
func sleepJudgeCaller(cfg *config.Config) (sleep.ModelCaller, string, error) {
	if cfg == nil {
		return nil, "", nil
	}
	target := cfg.EffectiveModelName()
	backend := cfg.Sleep.JudgeBackend
	judgeName := cfg.Sleep.JudgeModel
	if judgeName == "" && backend == "" {
		if hctx.SummarizeModel != nil && cfg.SummaryModel != "" && cfg.SummaryModel != target {
			return hakaseagent.PromptModelCaller(hctx.SummarizeModel), "summary/" + cfg.SummaryModel, nil
		}
		return nil, "", nil
	}
	cp := *cfg
	if backend != "" {
		cp.Provider = backend
	}
	if judgeName != "" {
		cp.ModelName = judgeName
	}
	resolved := cp.EffectiveModelName()
	if cp.Provider == cfg.Provider && resolved == target {
		return nil, "", nil // explicitly the same stack as the target
	}
	provider, err := hakaseagent.ProviderFactory(&cp)
	if err != nil {
		return nil, "", err
	}
	if err := provider.ValidateConfig(&cp); err != nil {
		return nil, "", fmt.Errorf("validate judge provider: %w", err)
	}
	m, err := provider.CreateModel(context.Background(), resolved, cp.APIKey)
	if err != nil {
		return nil, "", err
	}
	return hakaseagent.PromptModelCaller(m), cp.Provider + "/" + resolved, nil
}

// runSleepEvalkit replays one reviewed task manifest against two skill
// documents (baseline vs candidate) and reports the paired comparison:
// McNemar's exact test plus a seeded bootstrap CI (plan SL-034). Both sides
// are scored by the same judge, so any judge bias applies equally.
func runSleepEvalkit(args []string) int {
	fs := flag.NewFlagSet("evalkit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var baselineFile, candidateFile, tasksFile, metric string
	var mixedWeight float64
	var bootstrap int
	var seed int64
	var perTaskTimeout int
	var agentic bool
	fs.StringVar(&baselineFile, "baseline", "", "incumbent SKILL.md path (required)")
	fs.StringVar(&candidateFile, "candidate", "", "challenger SKILL.md path (required)")
	fs.StringVar(&tasksFile, "tasks", "", "reviewed task manifest (required)")
	fs.StringVar(&metric, "metric", "hard", "per-task projection: hard | soft | mixed")
	fs.Float64Var(&mixedWeight, "mixed-weight", 0, "soft weight for mixed metric (default 0.5)")
	fs.IntVar(&bootstrap, "bootstrap", 0, "bootstrap repetitions (default 2000)")
	fs.Int64Var(&seed, "seed", sleep.DefaultSplitSeed, "bootstrap RNG seed")
	fs.IntVar(&perTaskTimeout, "per-task-timeout", 0, "per-task replay budget in seconds (default 120)")
	fs.BoolVar(&agentic, "agentic", false, "replay via an isolated runner with read-only tools")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if baselineFile == "" || candidateFile == "" || tasksFile == "" || fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "evalkit requires --baseline, --candidate and --tasks\n\n")
		fs.Usage()
		return 2
	}
	if err := sleep.RequireReviewed(tasksFile); err != nil {
		fmt.Fprintf(os.Stderr, "sleep evalkit: %v\n", err)
		return 1
	}
	baseBytes, err := os.ReadFile(baselineFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep evalkit: read baseline: %v\n", err)
		return 1
	}
	candBytes, err := os.ReadFile(candidateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep evalkit: read candidate: %v\n", err)
		return 1
	}
	tasks, err := sleep.LoadMarkdownTasks(tasksFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep evalkit: %v\n", err)
		return 1
	}
	if err := cronModelBootstrap(); err != nil {
		fmt.Fprintf(os.Stderr, "sleep evalkit requires a configured model: %v\n", err)
		return 1
	}
	call := func(ctx context.Context, prompt string) (string, error) {
		return hakaseagent.ModelPromptFn(ctx, prompt)
	}
	run := sleep.SingleShotTarget(call)
	if agentic {
		ar, err := sleep.AgenticTarget(sleep.AgenticOpts{Model: currentModel, Timeout: time.Duration(perTaskTimeout) * time.Second})
		if err != nil {
			fmt.Fprintf(os.Stderr, "sleep evalkit: agentic replay unavailable: %v\n", err)
			return 1
		}
		run = ar
	}
	res, err := sleep.RunEvalkit(context.Background(), run, sleep.DefaultRubricJudge(call), sleep.EvalkitOpts{
		Tasks:         tasks,
		BaselineBody:  string(baseBytes),
		CandidateBody: string(candBytes),
		Metric:        metric,
		MixedWeight:   mixedWeight,
		Bootstrap:     bootstrap,
		Seed:          seed,
		Timeout:       time.Duration(perTaskTimeout) * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep evalkit: %v\n", err)
		return 1
	}
	fmt.Print(sleep.RenderEvalkitReport(res))
	return 0
}

// runSleepReview signs the human-review sidecar for a tasks/digest file.
func runSleepReview(args []string) int {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var tasksFile, reviewer string
	fs.StringVar(&tasksFile, "tasks", "", "tasks/digest file being reviewed (required)")
	fs.StringVar(&reviewer, "reviewer", "", "who reviewed it (required, recorded in the sidecar)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 || tasksFile == "" || reviewer == "" {
		fmt.Fprintf(os.Stderr, "review requires --tasks and --reviewer\n\n")
		fs.Usage()
		return 2
	}
	if err := sleep.MarkTasksReviewed(tasksFile, reviewer); err != nil {
		fmt.Fprintf(os.Stderr, "sleep review: %v\n", err)
		return 1
	}
	if err := sleep.VerifyTaskReview(tasksFile); err != nil {
		fmt.Fprintf(os.Stderr, "sleep review: verification failed: %v\n", err)
		return 1
	}
	fmt.Printf("Reviewed %s (reviewer: %s). Real-backend runs may now consume it until it changes.\n", tasksFile, reviewer)
	return 0
}

// runSleepSchedule creates a native:"sleep" cron job directly in the
// registry. This is the CLI-only creation path mandated by SL-006: the
// cronjob tool cannot create, alter, or trigger privileged native jobs.
// The job runs one full night per schedule via the run/tick/TUI paths.
func runSleepSchedule(args []string) int {
	fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var at, name string
	fs.StringVar(&at, "at", "", "schedule expression, e.g. \"0 3 * * *\" (required)")
	fs.StringVar(&name, "name", "sleep-nightly", "job display name")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if at == "" || fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "schedule requires --at <expression>\n\n")
		fs.Usage()
		return 2
	}
	if _, err := parseSchedule(at); err != nil {
		fmt.Fprintf(os.Stderr, "sleep schedule: %v\n", err)
		return 2
	}
	now := time.Now().UTC()
	job := CronJob{
		ID:        session.GenerateTaskID(),
		Name:      name,
		Prompt:    "",
		Schedule:  at,
		State:     CronStateScheduled,
		Enabled:   true,
		Native:    "sleep",
		CreatedAt: now,
		UpdatedAt: now,
	}
	reg, err := loadCronRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sleep schedule: %v\n", err)
		return 1
	}
	reg.Jobs = append(reg.Jobs, job)
	if err := saveCronRegistry(reg); err != nil {
		fmt.Fprintf(os.Stderr, "sleep schedule: %v\n", err)
		return 1
	}
	fmt.Printf("Scheduled native sleep job %s (%s) at %q.\n", job.ID, name, at)
	fmt.Println("Runs via `hakase cron tick`/`run` or the TUI ticker (opportunistic, in-process).")
	fmt.Println("For a true nightly guarantee, add `hakase cron tick` to your OS crontab; the TUI only fires while hakase runs.")
	return 0
}
