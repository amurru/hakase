// cronjob_test.go - tests for the cronjob tool: schedule parsing, registry
// persistence, job lookup, and post-run state transitions.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cronTestEnv points the cron registry at a fresh temp HAKASE_HOME and resets
// the cached cronJobsFile so each test starts with an empty registry.
func cronTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HAKASE_HOME", t.TempDir())
	cronJobsFile = ""
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// newTestCronJob builds a minimal scheduled job.
func newTestCronJob(id, name, schedule string) CronJob {
	now := time.Now().UTC()
	return CronJob{
		ID:        id,
		Name:      name,
		Prompt:    "test prompt",
		Schedule:  schedule,
		State:     CronStateScheduled,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestParseScheduleRelative(t *testing.T) {
	cronTestEnv(t)

	from := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		raw    string
		delta  time.Duration
		oneShot bool
	}{
		{"30m", 30 * time.Minute, true},
		{"2h", 2 * time.Hour, true},
		{"1d", 24 * time.Hour, true},
		{"45s", 45 * time.Second, true},
	}
	for _, c := range cases {
		sched, err := parseSchedule(c.raw)
		if err != nil {
			t.Fatalf("parseSchedule(%q): %v", c.raw, err)
		}
		if sched.Kind != ScheduleOneShot {
			t.Errorf("parseSchedule(%q): kind = %v, want one-shot", c.raw, sched.Kind)
		}
		if sched.OneShot != c.oneShot {
			t.Errorf("parseSchedule(%q): OneShot = %v, want %v", c.raw, sched.OneShot, c.oneShot)
		}
		next, err := sched.next(from)
		if err != nil {
			t.Fatalf("parseSchedule(%q).next: %v", c.raw, err)
		}
		if want := from.Add(c.delta); !next.Equal(want) {
			t.Errorf("parseSchedule(%q).next = %v, want %v", c.raw, next, want)
		}
	}
}

func TestParseScheduleInterval(t *testing.T) {
	cronTestEnv(t)

	from := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		raw   string
		delta time.Duration
	}{
		{"every 30m", 30 * time.Minute},
		{"every 2h", 2 * time.Hour},
		{"every 1d", 24 * time.Hour},
		{"every 30 minutes", 30 * time.Minute},
		{"every 2 hours", 2 * time.Hour},
	}
	for _, c := range cases {
		sched, err := parseSchedule(c.raw)
		if err != nil {
			t.Fatalf("parseSchedule(%q): %v", c.raw, err)
		}
		if sched.Kind != ScheduleInterval {
			t.Errorf("parseSchedule(%q): kind = %v, want interval", c.raw, sched.Kind)
		}
		if sched.OneShot {
			t.Errorf("parseSchedule(%q): OneShot = true, want false", c.raw)
		}
		next, err := sched.next(from)
		if err != nil {
			t.Fatalf("parseSchedule(%q).next: %v", c.raw, err)
		}
		if want := from.Add(c.delta); !next.Equal(want) {
			t.Errorf("parseSchedule(%q).next = %v, want %v", c.raw, next, want)
		}
	}
}

func TestParseScheduleCron(t *testing.T) {
	cronTestEnv(t)

	from := time.Date(2026, 8, 6, 8, 30, 0, 0, time.UTC) // a Thursday

	sched, err := parseSchedule("0 9 * * *")
	if err != nil {
		t.Fatalf("parseSchedule: %v", err)
	}
	if sched.Kind != ScheduleCron {
		t.Errorf("kind = %v, want cron", sched.Kind)
	}
	next, err := sched.next(from)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if want := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next = %v, want %v (same-day 09:00)", next, want)
	}

	// A daily 09:00 cron must advance to the NEXT day when already past 09:00.
	fromLate := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	nextLate, err := sched.next(fromLate)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if want := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC); !nextLate.Equal(want) {
		t.Errorf("next = %v, want %v (tomorrow 09:00)", nextLate, want)
	}
}

func TestParseScheduleISO(t *testing.T) {
	cronTestEnv(t)

	from := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	sched, err := parseSchedule("2026-08-07T09:00:00")
	if err != nil {
		t.Fatalf("parseSchedule: %v", err)
	}
	if !sched.OneShot {
		t.Errorf("OneShot = false, want true for ISO timestamp")
	}
	next, err := sched.next(from)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if want := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}

	// RFC3339 with offset.
	schedZ, err := parseSchedule("2026-08-07T09:00:00Z")
	if err != nil {
		t.Fatalf("parseSchedule RFC3339: %v", err)
	}
	nextZ, _ := schedZ.next(from)
	if want := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC); !nextZ.Equal(want) {
		t.Errorf("next = %v, want %v", nextZ, want)
	}
}

func TestParseScheduleErrors(t *testing.T) {
	cronTestEnv(t)

	cases := []string{
		"",            // empty
		"banana",      // garbage
		"0 9 * * * *", // 6-field cron must be rejected
		"every",       // bare keyword
		"every banana",
		"0m", // zero duration
	}
	for _, raw := range cases {
		if _, err := parseSchedule(raw); err == nil {
			t.Errorf("parseSchedule(%q): expected error, got nil", raw)
		}
	}
}

func TestCronRegistrySaveLoad(t *testing.T) {
	cronTestEnv(t)

	reg := CronRegistry{Jobs: []CronJob{
		newTestCronJob("job1", "alpha", "0 9 * * *"),
		newTestCronJob("job2", "beta", "every 2h"),
	}}

	if err := saveCronRegistry(reg); err != nil {
		t.Fatalf("saveCronRegistry: %v", err)
	}

	loaded, err := loadCronRegistry()
	if err != nil {
		t.Fatalf("loadCronRegistry: %v", err)
	}
	if len(loaded.Jobs) != 2 {
		t.Fatalf("loaded %d jobs, want 2", len(loaded.Jobs))
	}
	if loaded.Jobs[0].ID != "job1" || loaded.Jobs[1].ID != "job2" {
		t.Errorf("job order wrong after round-trip: %+v", loaded.Jobs)
	}

	// The file must live under the HAKASE_HOME dir.
	if _, err := os.Stat(filepath.Join(os.Getenv("HAKASE_HOME"), "cronjobs.json")); err != nil {
		t.Errorf("registry file not at HAKASE_HOME/cronjobs.json: %v", err)
	}
}

func TestCronRegistryMissingFile(t *testing.T) {
	cronTestEnv(t)

	reg, err := loadCronRegistry()
	if err != nil {
		t.Fatalf("loadCronRegistry on missing file: %v", err)
	}
	if len(reg.Jobs) != 0 {
		t.Errorf("expected empty registry, got %d jobs", len(reg.Jobs))
	}
}

func TestGetCronJob(t *testing.T) {
	cronTestEnv(t)

	reg := CronRegistry{Jobs: []CronJob{
		newTestCronJob("job1", "Daily digest", "0 9 * * *"),
		newTestCronJob("job2", "weekly", "0 9 * * 1"),
	}}

	// By exact ID.
	job, err := getCronJob(reg, "job1")
	if err != nil || job.ID != "job1" {
		t.Fatalf("getCronJob by ID: job=%v err=%v", job, err)
	}

	// By case-insensitive name.
	job, err = getCronJob(reg, "DAILY DIGEST")
	if err != nil || job.ID != "job1" {
		t.Fatalf("getCronJob by name: job=%v err=%v", job, err)
	}

	// Not found.
	if _, err := getCronJob(reg, "nope"); err == nil {
		t.Errorf("getCronJob(unknown): expected error, got nil")
	}
}

func TestGetCronJobAmbiguousName(t *testing.T) {
	cronTestEnv(t)

	reg := CronRegistry{Jobs: []CronJob{
		newTestCronJob("job1", "digest", "0 9 * * *"),
		newTestCronJob("job2", "Digest", "0 8 * * *"),
	}}

	if _, err := getCronJob(reg, "digest"); err == nil {
		t.Errorf("getCronJob(ambiguous): expected error, got nil")
	} else if !strings.Contains(err.Error(), "multiple jobs match name") {
		t.Errorf("getCronJob(ambiguous) error = %q, want mention of multiple matches", err.Error())
	}
}

func TestUpdateCronJobAfterRunRecurring(t *testing.T) {
	cronTestEnv(t)

	job := newTestCronJob("job1", "alpha", "every 2h")
	if err := saveCronRegistry(CronRegistry{Jobs: []CronJob{job}}); err != nil {
		t.Fatalf("saveCronRegistry: %v", err)
	}

	updateCronJobAfterRun(job, "ok", "summary", "outputs/cron/job1.md")

	reg, err := loadCronRegistry()
	if err != nil {
		t.Fatalf("loadCronRegistry: %v", err)
	}
	got := reg.Jobs[0]
	if got.State != CronStateScheduled {
		t.Errorf("state = %v, want scheduled (recurring job keeps running)", got.State)
	}
	if !got.Enabled {
		t.Errorf("job disabled after recurring run, want enabled")
	}
	if got.RunCount != 1 {
		t.Errorf("RunCount = %d, want 1", got.RunCount)
	}
	if got.NextRunAt == nil {
		t.Errorf("NextRunAt = nil, want a future time")
	}
	if got.LastStatus != "ok" {
		t.Errorf("LastStatus = %q, want ok", got.LastStatus)
	}
}

func TestUpdateCronJobAfterRunOneShot(t *testing.T) {
	cronTestEnv(t)

	job := newTestCronJob("job1", "alpha", "30m")
	if err := saveCronRegistry(CronRegistry{Jobs: []CronJob{job}}); err != nil {
		t.Fatalf("saveCronRegistry: %v", err)
	}

	updateCronJobAfterRun(job, "ok", "summary", "outputs/cron/job1.md")

	reg, err := loadCronRegistry()
	if err != nil {
		t.Fatalf("loadCronRegistry: %v", err)
	}
	got := reg.Jobs[0]
	if got.State != CronStateCompleted {
		t.Errorf("state = %v, want completed (one-shot fires once)", got.State)
	}
	if got.NextRunAt != nil {
		t.Errorf("NextRunAt = %v, want nil for completed job", got.NextRunAt)
	}
}

func TestDueCronJobs(t *testing.T) {
	cronTestEnv(t)

	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	reg := CronRegistry{Jobs: []CronJob{
		// Due: scheduled + enabled + past next-run.
		{ID: "due1", Name: "due", Schedule: "0 9 * * *", State: CronStateScheduled, Enabled: true, NextRunAt: &past},
		// Not due: next run in the future.
		{ID: "future1", Name: "future", Schedule: "every 2h", State: CronStateScheduled, Enabled: true, NextRunAt: &future},
		// Not due: paused.
		{ID: "paused1", Name: "paused", Schedule: "every 2h", State: CronStatePaused, Enabled: false, NextRunAt: &past},
		// Not due: completed.
		{ID: "done1", Name: "done", Schedule: "30m", State: CronStateCompleted, Enabled: false, NextRunAt: nil},
		// Not due: nil next run.
		{ID: "nil1", Name: "nil", Schedule: "every 2h", State: CronStateScheduled, Enabled: true, NextRunAt: nil},
	}}

	due := dueCronJobs(now, reg)
	if len(due) != 1 {
		t.Fatalf("dueCronJobs returned %d jobs, want 1", len(due))
	}
	if due[0].ID != "due1" {
		t.Errorf("due job = %s, want due1", due[0].ID)
	}
}

// TestHandleCronCreateNativeEvolve verifies the native "evolve" job contract
// (plan Phase 3b cron wiring): creation without an LLM prompt, and rejection
// of unknown native types. The registry is sandboxed to a temp HAKASE_HOME.
func TestHandleCronCreateNativeEvolve(t *testing.T) {
	t.Setenv("HAKASE_HOME", t.TempDir())

	// Native evolve job: no prompt required.
	out, err := handleCronCreate(CronjobInput{
		Action:   "create",
		Name:     "nightly evolution",
		Schedule: "every 24h",
		Native:   "evolve",
	}, func(string) {})
	if err != nil {
		t.Fatalf("handleCronCreate: %v", err)
	}
	if !out.Success {
		t.Fatalf("native evolve create failed: %s", out.Message)
	}
	if out.Job == nil || out.Job.Native != "evolve" {
		t.Fatalf("job native field not set: %+v", out.Job)
	}

	// Unknown native type must be rejected.
	out2, err := handleCronCreate(CronjobInput{
		Action:   "create",
		Name:     "bogus",
		Schedule: "every 24h",
		Native:   "bogus",
	}, func(string) {})
	if err != nil {
		t.Fatalf("handleCronCreate(bogus): %v", err)
	}
	if out2.Success {
		t.Fatalf("unknown native type should be rejected: %+v", out2)
	}

	// Plain job without a prompt must still be rejected.
	out3, err := handleCronCreate(CronjobInput{
		Action:   "create",
		Schedule: "every 24h",
	}, func(string) {})
	if err != nil {
		t.Fatalf("handleCronCreate(no prompt): %v", err)
	}
	if out3.Success {
		t.Fatalf("prompt-less plain job should be rejected: %+v", out3)
	}
}
