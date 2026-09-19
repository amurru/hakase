// cron_sleep_test.go - SL-023 acceptance: env overrides parse with the
// redaction keys hard-rejected, the config maps onto cycle opts with the
// val+test<1 and redact_secrets:false guards, `sleep schedule` creates a
// CLI-only native job, and the tool path stays denied for native sleep.
package cli

import (
	"strings"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/sleep"
)

func TestLoadSleepEnv_RejectsRedactionKeys(t *testing.T) {
	for _, key := range []string{
		"HAKASE_SLEEP_REDACT_SECRETS=false",
		"HAKASE_SLEEP_ALLOW_UNREDACTED=true",
	} {
		if _, _, err := sleep.LoadSleepEnv([]string{key}); err == nil {
			t.Errorf("%s must hard-error (SL-004: flag-only)", key)
		}
	}
}

func TestLoadSleepEnv_AllowedKeysAndWarnings(t *testing.T) {
	env, warnings, err := sleep.LoadSleepEnv([]string{
		"HAKASE_SLEEP_MODEL=gemini/flash",
		"HAKASE_SLEEP_MAX_TOKENS=500000",
		"HAKASE_SLEEP_LOOKBACK_HOURS=24",
		"HAKASE_SLEEP_MAX_TASKS=10",
		"HAKASE_SLEEP_LLM_MINE=true",
		"HAKASE_SLEEP_FAN_OUT=1",
		"HAKASE_SLEEP_AUTO_ADOPT=yes",
		"HAKASE_SLEEP_INCLUDE_AUDIT_ARGS=on",
		"HAKASE_SLEEP_JUDGE_MODEL=nano",
		"HAKASE_SLEEP_BOGUS=1",
	})
	if err != nil {
		t.Fatalf("valid keys must load: %v", err)
	}
	if env.Model != "gemini/flash" || env.MaxTokens != 500000 || env.LookbackHours != 24 ||
		env.MaxTasks != 10 || !env.LLMMine || !env.FanOut || !env.AutoAdopt ||
		!env.IncludeAuditArgs || env.JudgeModel != "nano" {
		t.Errorf("overrides parsed wrong: %+v", env)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "HAKASE_SLEEP_BOGUS") {
		t.Errorf("unknown key must warn: %v", warnings)
	}
}

func TestBuildSleepCycleOpts(t *testing.T) {
	cfg := &config.Config{}
	opts, err := buildSleepCycleOpts(cfg, sleep.SleepEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.EditBudget != 0 || opts.StatePath != sleep.DefaultStatePath {
		t.Errorf("defaults not mapped: %+v", opts)
	}

	cfg = &config.Config{Sleep: config.SleepConfig{
		EditBudget:        2,
		MaxTokensPerNight: 999,
		ValFraction:       0.2,
		TestFraction:      0.2,
		GateNoRegression:  true,
	}}
	opts, err = buildSleepCycleOpts(cfg, sleep.SleepEnv{MaxTokens: 555})
	if err != nil {
		t.Fatal(err)
	}
	if opts.EditBudget != 2 || opts.MaxTokensPerNight != 555 || !opts.GateNoRegression {
		t.Errorf("config+env mapping wrong: %+v", opts)
	}

	// val+test >= 1 refuses.
	bad := &config.Config{Sleep: config.SleepConfig{ValFraction: 0.5, TestFraction: 0.5}}
	if _, err := buildSleepCycleOpts(bad, sleep.SleepEnv{}); err == nil {
		t.Error("val+test>=1 must be refused")
	}

	// redact_secrets:false is file-refused regardless of flags.
	falsely := false
	red := &config.Config{Sleep: config.SleepConfig{RedactSecrets: &falsely}}
	if _, err := buildSleepCycleOpts(red, sleep.SleepEnv{}); err == nil {
		t.Error("redact_secrets:false in config must be refused")
	}
}

func TestSleepSchedule_CreatesNativeJobCLIOnly(t *testing.T) {
	cronTestEnv(t)
	if code := RunSleepCLI([]string{"schedule", "--at", "not-a-schedule"}); code != 2 {
		t.Errorf("bad schedule exit = %d, want 2", code)
	}
	if code := RunSleepCLI([]string{"schedule", "--at", "0 3 * * *", "--name", "nightly"}); code != 0 {
		t.Errorf("schedule exit = %d, want 0", code)
	}
	reg, err := loadCronRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(reg.Jobs))
	}
	j := reg.Jobs[0]
	if j.Native != "sleep" || j.Schedule != "0 3 * * *" || !j.Enabled {
		t.Errorf("scheduled job wrong: %+v", j)
	}

	// SL-006: the tool path must still refuse to create native sleep.
	out, err := handleCronCreate(CronjobInput{
		Schedule: "0 4 * * *",
		Native:   "sleep",
	}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if out.Success {
		t.Error("tool creation of native sleep must be denied")
	}
}

func TestRunNativeCronJob_SleepFailsLoudlyWithoutBootstrap(t *testing.T) {
	cronTestEnv(t)
	currentModel = nil
	currentConfig = nil
	job := newTestCronJob("sleepjob", "nightly", "0 3 * * *")
	job.Native = "sleep"
	reg := CronRegistry{Jobs: []CronJob{job}}
	if err := saveCronRegistry(reg); err != nil {
		t.Fatal(err)
	}

	runSleepCronJob(job, func(string) {})
	after, err := loadCronRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if after.Jobs[0].LastStatus != "failed" {
		t.Errorf("last_status = %q, want failed (loud bootstrap failure)", after.Jobs[0].LastStatus)
	}
}
