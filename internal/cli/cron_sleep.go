// cron_sleep.go - the native `sleep` cron task (plan SL-023): one full
// SkillOpt-Sleep night triggered by the scheduler. Creation is CLI-only
// (`hakase sleep schedule`, per SL-006 the cronjob tool cannot touch
// privileged native jobs); this file only runs the night.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/session"
	"amurru/hakase/internal/sleep"
)

// runSleepCronJob executes one night for a native:"sleep" job and records
// the outcome on the job. The headless cron entry points (cron run/tick)
// bootstrap the model before triggering; a TUI-ticker path that has not
// bootstrapped yet gets one in-place attempt, failing loudly otherwise.
func runSleepCronJob(job CronJob, log hakaseagent.LogFunc) {
	if currentModel == nil {
		if err := cronModelBootstrap(); err != nil {
			failSleepCronJob(job, log, fmt.Sprintf("model bootstrap failed: %v", err))
			return
		}
	}
	env, warnings, err := sleep.LoadSleepEnvFromOS()
	if err != nil {
		failSleepCronJob(job, log, err.Error())
		return
	}
	for _, w := range warnings {
		log(fmt.Sprintf("[cron] job %s sleep: %s", job.ID, w))
	}
	opts, err := buildSleepCycleOpts(currentConfig, env)
	if err != nil {
		failSleepCronJob(job, log, err.Error())
		return
	}
	opts.Call = func(ctx context.Context, prompt string) (string, error) {
		return hakaseagent.ModelPromptFn(ctx, prompt)
	}
	// Judge separation (plan H2): build the configured judge model. The
	// cron path has no --allow-shared-judge flag surface, so a missing
	// judge configuration logs loudly and continues shared rather than
	// aborting unattended nights.
	judgeCall, judgeKey, err := sleepJudgeCaller(currentConfig)
	if err != nil {
		log(fmt.Sprintf("[cron] job %s sleep: judge model unavailable (%v); continuing with shared judge", job.ID, err))
	} else if judgeCall == nil {
		log(fmt.Sprintf("[cron] job %s sleep: no separate judge model configured (set sleep.judge_model); judge shares the target model", job.ID))
	} else {
		opts.JudgeCall = judgeCall
		opts.JudgeModelKey = judgeKey
	}
	opts.ModelKey = sleepModelKey()
	// Night milestones surface in the cron log (SL-041): a scheduled night
	// runs unattended, so per-group progress is the only live signal.
	opts.Progress = func(line string) {
		log(fmt.Sprintf("[cron] job %s sleep: %s", job.ID, line))
	}

	res, err := sleep.RunCycle(context.Background(), opts)
	if err != nil {
		failSleepCronJob(job, log, err.Error())
		return
	}
	report := sleep.RenderNightReport(res)
	outputPath := writeSleepCronReport(job, report)
	summary := fmt.Sprintf("night complete: sessions=%d tasks=%d tokens=%d (est.)",
		res.HarvestedSessions, res.MinedTasks, res.TokensUsed)
	if res.Truncated {
		summary = "night truncated by a spend guard: " + summary
	}
	log(fmt.Sprintf("[cron] job %s sleep: %s", job.ID, summary))
	updateCronJobAfterRun(job, "completed", summary, outputPath)
	notifyCronJob("completed", job.ID, job.Name, summary, outputPath)
}

// failSleepCronJob marks the job failed with a loud reason.
func failSleepCronJob(job CronJob, log hakaseagent.LogFunc, reason string) {
	log(fmt.Sprintf("[cron] job %s sleep failed: %s", job.ID, reason))
	updateCronJobAfterRun(job, "failed", reason, "")
	notifyCronJob("failed", job.ID, job.Name, reason, "")
}

// writeSleepCronReport persists the night report under outputs/cron/
// (0700/0600 per SL-005).
func writeSleepCronReport(job CronJob, report string) string {
	dir := filepath.Join("outputs", "cron")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	_ = os.Chmod(dir, 0o700)
	path := filepath.Join(dir, fmt.Sprintf("sleep-%s-%s.md", job.ID, time.Now().UTC().Format("20060102-150405")))
	if err := os.WriteFile(path, []byte(report), 0o600); err != nil {
		return ""
	}
	_ = os.Chmod(path, 0o600)
	return path
}

// buildSleepCycleOpts maps the file config plus env overrides onto cycle
// opts. Explicit CLI flags are applied by the caller afterwards (flags >
// env > file > defaults). Redaction is always on in scheduled runs: the
// cron path never passes the unredacted opt-out.
func buildSleepCycleOpts(cfg *config.Config, env sleep.SleepEnv) (sleep.CycleOpts, error) {
	if cfg == nil {
		return sleep.CycleOpts{}, fmt.Errorf("no config loaded (bootstrap required)")
	}
	sc := cfg.Sleep
	opts := sleep.CycleOpts{
		EditBudget:        sc.EditBudget,
		GateMetric:        sc.GateMetric,
		MixedWeight:       sc.MixedWeight,
		GateNoRegression:  sc.GateNoRegression,
		PerTaskTimeout:    time.Duration(sc.PerTaskTimeoutSeconds) * time.Second,
		PerNightTimeout:   time.Duration(sc.PerNightTimeoutSeconds) * time.Second,
		MaxTokensPerNight: sc.MaxTokensPerNight,
		FanOut:            sc.FanOut,
		AutoAdopt:         sc.AutoAdopt,
		OutputDir:         filepath.Join("outputs", "sleep"),
		StatePath:         sleep.DefaultStatePath,
		RecallK:           sc.RecallK,
		KnowledgeDir:      cfg.KnowledgeDir,
		DreamRollouts:     sc.DreamRollouts,
		DreamFactor:       sc.DreamFactor,
		DreamConsolidate:  sc.EvolveMemory,
		LRScheduler:       sc.LRScheduler,
		LRFloor:           sc.LRFloor,
		LRHorizon:         sc.LRHorizon,
		SkillAware:        sc.SkillAwareReflection,
		Harvest: sleep.HarvestOpts{
			LookbackHours:    sc.LookbackHours,
			MaxSessions:      sc.MaxSessionsPerNight,
			IncludeAuditArgs: sc.IncludeAuditArgs,
			AuditLogPath:     "logs/exec-audit.jsonl",
			SessionsDir:      sessionDir(),
		},
		Mine: sleep.MineOpts{
			MaxTasksPerNight: sc.MaxTasksPerNight,
			TrainFraction:    sc.TrainFraction,
			ValFraction:      sc.ValFraction,
			TestFraction:     sc.TestFraction,
			Seed:             sc.SplitSeed,
		},
	}
	// Fraction sanity: val+test must stay below 1 so train keeps tasks
	// (plan SL-023). Zero values mean the documented defaults.
	if sc.TestFraction+sc.ValFraction >= 1 {
		return opts, fmt.Errorf("sleep config: val_fraction + test_fraction must stay below 1")
	}
	// File redact_secrets:false is refused regardless of flags (SL-004).
	if sc.RedactSecrets != nil && !*sc.RedactSecrets {
		return opts, fmt.Errorf("sleep config: redact_secrets:false is not permitted in config.json; use --allow-unredacted explicitly if you accept the risk")
	}
	env.ApplyToCycleOpts(&opts)
	return opts, nil
}

// sessionDir resolves the configured session store directory.
func sessionDir() string {
	return session.Dir
}
