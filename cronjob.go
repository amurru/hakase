// cronjob.go - scheduled task subsystem for hakase.
//
// Implements the cronjob toolset modeled after the Hermes Agent
// (nousresearch/hermes-agent) cronjob toolset: a headless scheduler that
// persists a registry of timed jobs, parses four schedule formats (relative
// delay, ISO timestamp, every-interval, 5-field cron), runs jobs in isolated
// sub-agent runners, and writes output artifacts to outputs/cron/.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	cron "github.com/robfig/cron/v3"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// ---------------------------------------------------------------------------
// 1. Schedule model + parser
// ---------------------------------------------------------------------------

// ScheduleKind discriminates the parsed schedule flavour.
type ScheduleKind int

const (
	ScheduleInvalid  ScheduleKind = iota
	ScheduleOneShot                // relative delay or ISO timestamp
	ScheduleInterval               // recurring "every ..."
	ScheduleCron                   // 5-field cron expression
)

// Schedule is a parsed schedule expression that can compute the next fire time.
type Schedule struct {
	Kind    ScheduleKind // flavour
	Raw     string       // original user string
	Display string       // human-readable description ("every 2h", "0 9 * * *", "once in 30m")
	OneShot bool         // true for relative delays and ISO timestamps
	next    func(from time.Time) (time.Time, error) // next fire strictly after from
}

var (
	// relativeRe matches a bare duration: "30m", "2h", "1d", "45s".
	relativeRe = regexp.MustCompile(`^(\d+)\s*(s|m|h|d)$`)
	// intervalRe matches "every 2h", "every 30 minutes", "every 1 day", etc.
	intervalRe = regexp.MustCompile(`^every\s+(\d+)\s*(s|m|h|d|seconds?|minutes?|hours?|days?)$`)
	// isoRe matches approximate ISO prefix to distinguish timestamps from cron.
	isoRe = regexp.MustCompile(`^20\d\d-\d\d-\d\d`)
)

// durationForUnit converts a unit abbreviation or spelled-out name to the
// matching time.Duration multiplier. Returns -1 on unknown.
func durationForUnit(unit string) time.Duration {
	switch strings.ToLower(unit) {
	case "s", "second", "seconds":
		return time.Second
	case "m", "minute", "minutes":
		return time.Minute
	case "h", "hour", "hours":
		return time.Hour
	case "d", "day", "days":
		return 24 * time.Hour
	default:
		return -1
	}
}

// unitLabelFull returns the full unit label for display (e.g. "h" -> "hours").
func unitLabelFull(unit string) string {
	switch strings.ToLower(unit) {
	case "s":
		return "seconds"
	case "m":
		return "minutes"
	case "h":
		return "hours"
	case "d":
		return "days"
	default:
		return unit
	}
}

// parsableScheduleFormats is a static description of accepted formats.
const parsableScheduleFormats = "Accepted formats: '30m' / '2h' / '1d' (relative one-shot), " +
	"'every 30m' / 'every 2 hours' (recurring), " +
	"'0 9 * * *' (5-field cron), " +
	"'2026-06-01T09:00:00' (ISO timestamp)."

// parseSchedule parses a raw schedule string into a Schedule with a next-time
// closure. Errors include the accepted-format summary.
func parseSchedule(raw string) (Schedule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Schedule{}, fmt.Errorf("empty schedule. %s", parsableScheduleFormats)
	}

	// 1. Interval first, before relative, so "every 30m" is caught.
	if m := intervalRe.FindStringSubmatch(raw); m != nil {
		d := durationForUnit(m[2])
		if d < 0 {
			return Schedule{}, fmt.Errorf("unknown unit %q. %s", m[2], parsableScheduleFormats)
		}
		count := atoi(m[1])
		if count <= 0 {
			return Schedule{}, fmt.Errorf("interval count must be positive. %s", parsableScheduleFormats)
		}
		dur := time.Duration(count) * d
		return Schedule{
			Kind:    ScheduleInterval,
			Raw:     raw,
			Display: fmt.Sprintf("every %d %s", count, unitLabelFull(m[2])),
			next: func(from time.Time) (time.Time, error) {
				return from.Add(dur), nil
			},
		}, nil
	}

	// 2. Relative one-shot: "30m", "2h", "1d", "45s".
	if m := relativeRe.FindStringSubmatch(raw); m != nil {
		d := durationForUnit(m[2])
		if d < 0 {
			return Schedule{}, fmt.Errorf("unknown unit %q. %s", m[2], parsableScheduleFormats)
		}
		count := atoi(m[1])
		if count <= 0 {
			return Schedule{}, fmt.Errorf("duration must be positive. %s", parsableScheduleFormats)
		}
		dur := time.Duration(count) * d
		return Schedule{
			Kind:    ScheduleOneShot,
			Raw:     raw,
			Display: fmt.Sprintf("once in %s", dur.String()),
			OneShot: true,
			next: func(from time.Time) (time.Time, error) {
				return from.Add(dur), nil
			},
		}, nil
	}

	// 3. ISO timestamp: "2026-06-01T09:00:00", "2026-06-01T09:00:00Z", with offset, etc.
	if isoRe.MatchString(raw) {
		formats := []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04"}
		var ts time.Time
		var err error
		for _, f := range formats {
			ts, err = time.Parse(f, raw)
			if err == nil {
				break
			}
		}
		if err != nil {
			return Schedule{}, fmt.Errorf("cannot parse ISO timestamp %q. %s", raw, parsableScheduleFormats)
		}
		ts = ts.UTC()
		return Schedule{
			Kind:    ScheduleOneShot,
			Raw:     raw,
			Display: fmt.Sprintf("at %s UTC", ts.Format(time.RFC3339)),
			OneShot: true,
			next: func(from time.Time) (time.Time, error) {
				return ts, nil
			},
		}, nil
	}

	// 4. Cron: exactly five whitespace-separated fields. 6-field (with seconds)
	// is explicitly rejected with a clear message.
	fields := strings.Fields(raw)
	if len(fields) == 6 {
		return Schedule{}, fmt.Errorf("6-field cron (with seconds) is not supported; use 5-field cron instead. %s", parsableScheduleFormats)
	}
	if len(fields) == 5 {
		sched, err := cron.ParseStandard(raw)
		if err != nil {
			return Schedule{}, fmt.Errorf("invalid 5-field cron expression %q: %v. %s", raw, err, parsableScheduleFormats)
		}
		return Schedule{
			Kind:    ScheduleCron,
			Raw:     raw,
			Display: raw,
			next: func(from time.Time) (time.Time, error) {
				return sched.Next(from), nil
			},
		}, nil
	}

	return Schedule{}, fmt.Errorf("unrecognized schedule format %q. %s", raw, parsableScheduleFormats)
}

// atoi is a panic-free strconv.Atoi for the regex captures (already validated).
func atoi(s string) int {
	var n int
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// ---------------------------------------------------------------------------
// 2. Job model + registry
// ---------------------------------------------------------------------------

// CronJobState labels the lifecycle phase of a scheduled job.
type CronJobState string

const (
	CronStateScheduled CronJobState = "scheduled"
	CronStatePaused    CronJobState = "paused"
	CronStateRunning   CronJobState = "running"
	CronStateCompleted CronJobState = "completed"
)

// CronJob is a persisted scheduled task entry.
type CronJob struct {
	ID         string       `json:"id"`
	Name       string       `json:"name,omitempty"`
	Prompt     string       `json:"prompt"`
	Schedule   string       `json:"schedule"`
	Skills     []string     `json:"skills,omitempty"`
	Repeat     int          `json:"repeat,omitempty"`
	State      CronJobState `json:"state"`
	Enabled    bool         `json:"enabled"`
	NextRunAt  *time.Time   `json:"next_run_at,omitempty"`
	LastRunAt  *time.Time   `json:"last_run_at,omitempty"`
	LastStatus string       `json:"last_status,omitempty"`
	RunCount   int          `json:"run_count"`
	OutputPath string       `json:"output_path,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

// CronRegistry is the top-level persistence envelope.
type CronRegistry struct {
	Jobs []CronJob `json:"jobs"`
}

// cronJobsFile is resolved once via resolveCronJobsFile().
var cronJobsFile string

// cronRegistryMu serialises in-process access to the cron jobs file.
var cronRegistryMu sync.Mutex

// resolveCronJobsFile returns the path to the persisted cron-jobs registry,
// creating the parent directory if missing. Uses $HAKASE_HOME or ~/.hakase.
func resolveCronJobsFile() string {
	if cronJobsFile != "" {
		return cronJobsFile
	}
	home := hakaseHome()
	if home == "" {
		home = "."
	}
	_ = os.MkdirAll(home, 0755)
	cronJobsFile = filepath.Join(home, "cronjobs.json")
	return cronJobsFile
}

// loadCronRegistryLocked reads the registry from disk under the mutex.
func loadCronRegistryLocked() (CronRegistry, error) {
	var reg CronRegistry
	data, err := os.ReadFile(resolveCronJobsFile())
	if err != nil {
		if os.IsNotExist(err) {
			return CronRegistry{Jobs: []CronJob{}}, nil
		}
		return CronRegistry{}, err
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		return CronRegistry{}, err
	}
	return reg, nil
}

// loadCronRegistry is the public locked loader.
func loadCronRegistry() (CronRegistry, error) {
	cronRegistryMu.Lock()
	defer cronRegistryMu.Unlock()
	return loadCronRegistryLocked()
}

// saveCronRegistryLocked writes the registry to disk atomically with a
// tmp-file + rename, protected by an exclusive flock for cross-process safety.
func saveCronRegistryLocked(reg CronRegistry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	file := resolveCronJobsFile()
	tmp := file + ".tmp"
	lockFile := file + ".lock"

	// Acquire exclusive flock for cross-process safety.
	lf, err := os.OpenFile(lockFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// saveCronRegistry is the public locked saver.
func saveCronRegistry(reg CronRegistry) error {
	cronRegistryMu.Lock()
	defer cronRegistryMu.Unlock()
	return saveCronRegistryLocked(reg)
}

// getCronJob looks up a job by exact ID first, then by case-insensitive name.
// Returns an error when the name matches more than one job (ambiguous).
func getCronJob(reg CronRegistry, idOrName string) (*CronJob, error) {
	if idOrName == "" {
		return nil, fmt.Errorf("job identifier is required")
	}
	// Exact ID match first.
	for i := range reg.Jobs {
		if reg.Jobs[i].ID == idOrName {
			return &reg.Jobs[i], nil
		}
	}
	// Case-insensitive name match.
	var matches []*CronJob
	lower := strings.ToLower(idOrName)
	for i := range reg.Jobs {
		if strings.ToLower(reg.Jobs[i].Name) == lower {
			matches = append(matches, &reg.Jobs[i])
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no job found with id or name %q", idOrName)
	case 1:
		return matches[0], nil
	default:
		var ids []string
		for _, m := range matches {
			ids = append(ids, fmt.Sprintf("%s (%s)", m.ID, m.Name))
		}
		return nil, fmt.Errorf("multiple jobs match name %q: %s", idOrName, strings.Join(ids, ", "))
	}
}





// ---------------------------------------------------------------------------
// 3. Tool input / output types
// ---------------------------------------------------------------------------

// CronjobInput is the model-facing argument schema for the cronjob tool.
type CronjobInput struct {
	Action   string   `json:"action" doc:"One of: create, list, update, pause, resume, run, remove"`
	JobID    string   `json:"job_id,omitempty" doc:"Job ID or name (required for update/pause/resume/run/remove)"`
	Name     string   `json:"name,omitempty" doc:"Human-friendly job name (optional)"`
	Prompt   string   `json:"prompt,omitempty" doc:"Self-contained task prompt; required for create, optional for update"`
	Schedule string   `json:"schedule,omitempty" doc:"Schedule: '30m', 'every 2h', '0 9 * * *', or ISO timestamp; required for create"`
	Skills   []string `json:"skills,omitempty" doc:"Optional markdown skill names whose content is injected before the prompt"`
	Repeat   int      `json:"repeat,omitempty" doc:"Total number of runs; 0 = unlimited (default)"`
}

// CronjobOutput is the model-facing result schema for the cronjob tool.
type CronjobOutput struct {
	Success bool      `json:"success" doc:"Whether the operation succeeded"`
	Message string    `json:"message,omitempty" doc:"Human-readable status"`
	Job     *CronJob  `json:"job,omitempty" doc:"The affected job (create/update/pause/resume/run/remove)"`
	Jobs    []CronJob `json:"jobs,omitempty" doc:"All jobs (list action only)"`
}

// ---------------------------------------------------------------------------
// 4. The cronjob tool
// ---------------------------------------------------------------------------

// cronJobNotify is set by main and streams cron job lifecycle events to the
// TUI. Status values: "scheduled" (created), "started", "completed", "failed",
// "silent", "triggered".
var cronJobNotify func(status, jobID, name, summary, outputPath string)

// notifyCronJob emits a lifecycle event to the TUI listener and debug log.
func notifyCronJob(status, jobID, name, summary, outputPath string) {
	debugEvent("cronjob", "status", status, "job_id", jobID, "name", name)
	if cronJobNotify != nil {
		cronJobNotify(status, jobID, name, summary, outputPath)
	}
}

// createCronjobTool builds and returns the cronjob function tool registered
// via newDocTool so the doc:"..." struct tags are reflected into the JSON
// schema the model sees.
func createCronjobTool(log LogFunc) (tool.Tool, error) {
	return newDocTool(functiontool.Config{
		Name: "cronjob",
		Description: "Manage scheduled one-shot and recurring tasks. " +
			"Actions: create (schedule a new job), list (all jobs), " +
			"update (modify fields), pause / resume, run (trigger immediately), " +
			"remove (delete). Schedule formats: '30m' (one-shot delay), " +
			"'every 2h' (recurring interval), '0 9 * * *' (5-field cron), " +
			"or '2026-06-01T09:00:00' (ISO timestamp).",
	}, func(ctx agent.Context, input CronjobInput) (CronjobOutput, error) {
		action := strings.TrimSpace(strings.ToLower(input.Action))
		switch action {
		case "create":
			return handleCronCreate(input, log)
		case "list":
			return handleCronList()
		case "update":
			return handleCronUpdate(input, log)
		case "pause":
			return handleCronPause(input)
		case "resume":
			return handleCronResume(input, log)
		case "remove":
			return handleCronRemove(input)
		case "run":
			return handleCronRun(input, log)
		default:
			return CronjobOutput{
				Success: false,
				Message: fmt.Sprintf("Unknown action %q. Valid actions: create, list, update, pause, resume, run, remove.", input.Action),
			}, nil
		}
	})
}

func handleCronCreate(input CronjobInput, log LogFunc) (CronjobOutput, error) {
	if input.Schedule == "" {
		return CronjobOutput{Success: false, Message: "schedule is required for create"}, nil
	}
	if input.Prompt == "" {
		return CronjobOutput{Success: false, Message: "prompt is required for create"}, nil
	}

	sched, err := parseSchedule(input.Schedule)
	if err != nil {
		return CronjobOutput{Success: false, Message: err.Error()}, nil
	}

	if len(input.Skills) > 0 {
		_, missing, err := resolveCronSkills(input.Skills, log)
		if err != nil {
			return CronjobOutput{Success: false, Message: err.Error()}, nil
		}
		if len(missing) > 0 {
			var names []string
			for n := range missing {
				names = append(names, n)
			}
			sort.Strings(names)
			return CronjobOutput{
				Success: false,
				Message: fmt.Sprintf("skill(s) not found: %s", strings.Join(names, ", ")),
			}, nil
		}
	}

	now := time.Now().UTC()
	next, err := sched.next(now)
	if err != nil {
		return CronjobOutput{Success: false, Message: fmt.Sprintf("cannot compute next run: %v", err)}, nil
	}
	if sched.OneShot && !next.After(now) {
		return CronjobOutput{Success: false, Message: "schedule is in the past"}, nil
	}

	job := CronJob{
		ID:        GenerateTaskID(),
		Name:      input.Name,
		Prompt:    input.Prompt,
		Schedule:  input.Schedule,
		Skills:    input.Skills,
		Repeat:    input.Repeat,
		State:     CronStateScheduled,
		Enabled:   true,
		NextRunAt: &next,
		CreatedAt: now,
		UpdatedAt: now,
	}

	reg, err := loadCronRegistry()
	if err != nil {
		return CronjobOutput{}, err
	}
	reg.Jobs = append(reg.Jobs, job)
	if err := saveCronRegistry(reg); err != nil {
		return CronjobOutput{}, err
	}

	notifyCronJob("scheduled", job.ID, job.Name, job.Prompt, "")
	return CronjobOutput{Success: true, Message: fmt.Sprintf("Job created: %s", job.ID), Job: &job}, nil
}

func handleCronList() (CronjobOutput, error) {
	reg, err := loadCronRegistry()
	if err != nil {
		return CronjobOutput{}, err
	}
	jobs := reg.Jobs
	sort.Slice(jobs, func(i, j int) bool {
		ni, nj := jobs[i].NextRunAt, jobs[j].NextRunAt
		if ni == nil && nj == nil {
			return false
		}
		if ni == nil {
			return false
		}
		if nj == nil {
			return true
		}
		return ni.Before(*nj)
	})
	return CronjobOutput{Success: true, Message: fmt.Sprintf("%d job(s)", len(jobs)), Jobs: jobs}, nil
}

func handleCronUpdate(input CronjobInput, log LogFunc) (CronjobOutput, error) {
	reg, err := loadCronRegistry()
	if err != nil {
		return CronjobOutput{}, err
	}
	job, err := getCronJob(reg, input.JobID)
	if err != nil {
		return CronjobOutput{Success: false, Message: err.Error()}, nil
	}

	var changed bool
	if input.Schedule != "" {
		sched, err := parseSchedule(input.Schedule)
		if err != nil {
			return CronjobOutput{Success: false, Message: err.Error()}, nil
		}
		next, err := sched.next(time.Now().UTC())
		if err != nil {
			return CronjobOutput{Success: false, Message: fmt.Sprintf("cannot compute next run: %v", err)}, nil
		}
		job.Schedule = input.Schedule
		job.NextRunAt = &next
		changed = true
	}
	if input.Prompt != "" {
		job.Prompt = input.Prompt
		changed = true
	}
	if input.Name != "" {
		job.Name = input.Name
		changed = true
	}
	if len(input.Skills) > 0 {
		_, missing, err := resolveCronSkills(input.Skills, log)
		if err != nil {
			return CronjobOutput{Success: false, Message: err.Error()}, nil
		}
		if len(missing) > 0 {
			var names []string
			for n := range missing {
				names = append(names, n)
			}
			sort.Strings(names)
			return CronjobOutput{
				Success: false,
				Message: fmt.Sprintf("skill(s) not found: %s", strings.Join(names, ", ")),
			}, nil
		}
		job.Skills = input.Skills
		changed = true
	}
	if input.Repeat > 0 {
		job.Repeat = input.Repeat
		changed = true
	}
	if !changed {
		return CronjobOutput{Success: true, Message: "no fields to update", Job: job}, nil
	}

	job.UpdatedAt = time.Now().UTC()
	if err := saveCronRegistry(reg); err != nil {
		return CronjobOutput{}, err
	}
	return CronjobOutput{Success: true, Message: fmt.Sprintf("Job updated: %s", job.ID), Job: job}, nil
}

func handleCronPause(input CronjobInput) (CronjobOutput, error) {
	reg, err := loadCronRegistry()
	if err != nil {
		return CronjobOutput{}, err
	}
	job, err := getCronJob(reg, input.JobID)
	if err != nil {
		return CronjobOutput{Success: false, Message: err.Error()}, nil
	}
	job.State = CronStatePaused
	job.Enabled = false
	job.NextRunAt = nil
	job.UpdatedAt = time.Now().UTC()
	if err := saveCronRegistry(reg); err != nil {
		return CronjobOutput{}, err
	}
	notifyCronJob("triggered", job.ID, job.Name, "paused", "")
	return CronjobOutput{Success: true, Message: fmt.Sprintf("Job paused: %s", job.ID), Job: job}, nil
}

func handleCronResume(input CronjobInput, log LogFunc) (CronjobOutput, error) {
	reg, err := loadCronRegistry()
	if err != nil {
		return CronjobOutput{}, err
	}
	job, err := getCronJob(reg, input.JobID)
	if err != nil {
		return CronjobOutput{Success: false, Message: err.Error()}, nil
	}
	if job.State == CronStateCompleted {
		return CronjobOutput{Success: false, Message: "cannot resume a completed job"}, nil
	}
	sched, err := parseSchedule(job.Schedule)
	if err != nil {
		return CronjobOutput{Success: false, Message: fmt.Sprintf("cannot re-parse stored schedule: %v", err)}, nil
	}
	next, err := sched.next(time.Now().UTC())
	if err != nil {
		return CronjobOutput{Success: false, Message: fmt.Sprintf("cannot compute next run: %v", err)}, nil
	}
	job.State = CronStateScheduled
	job.Enabled = true
	job.NextRunAt = &next
	job.UpdatedAt = time.Now().UTC()
	if err := saveCronRegistry(reg); err != nil {
		return CronjobOutput{}, err
	}
	notifyCronJob("triggered", job.ID, job.Name, "resumed", "")
	return CronjobOutput{Success: true, Message: fmt.Sprintf("Job resumed: %s", job.ID), Job: job}, nil
}

func handleCronRemove(input CronjobInput) (CronjobOutput, error) {
	reg, err := loadCronRegistry()
	if err != nil {
		return CronjobOutput{}, err
	}
	job, err := getCronJob(reg, input.JobID)
	if err != nil {
		return CronjobOutput{Success: false, Message: err.Error()}, nil
	}
	removed := *job
	reg.Jobs = removeFromSlice(reg.Jobs, job)
	if err := saveCronRegistry(reg); err != nil {
		return CronjobOutput{}, err
	}
	return CronjobOutput{Success: true, Message: fmt.Sprintf("Job removed: %s", job.ID), Job: &removed}, nil
}

func handleCronRun(input CronjobInput, log LogFunc) (CronjobOutput, error) {
	reg, err := loadCronRegistry()
	if err != nil {
		return CronjobOutput{}, err
	}
	job, err := getCronJob(reg, input.JobID)
	if err != nil {
		return CronjobOutput{Success: false, Message: err.Error()}, nil
	}
	// Fire in background; do not alter the schedule.
	jobCopy := *job
	go runCronJob(jobCopy, log)
	notifyCronJob("triggered", job.ID, job.Name, "manually triggered", "")
	return CronjobOutput{Success: true, Message: fmt.Sprintf("Job triggered: %s", job.ID), Job: &jobCopy}, nil
}

// removeFromSlice deletes the first occurrence of target (matched by pointer)
// from s and returns the new slice.
func removeFromSlice(jobs []CronJob, target *CronJob) []CronJob {
	for i := range jobs {
		if &jobs[i] == target {
			return append(jobs[:i], jobs[i+1:]...)
		}
	}
	return jobs
}

// ---------------------------------------------------------------------------
// 5. Scheduler + executor
// ---------------------------------------------------------------------------

// cronRunning tracks in-flight job IDs to prevent double-firing.
var cronRunningMu sync.Mutex
var cronRunning = make(map[string]bool)

// startCronScheduler launches a background goroutine that ticks every 30s and
// fires any due jobs in their own goroutine. Safe to call once.
func startCronScheduler(log LogFunc) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			cronTick(log)
		}
	}()
}

func cronTick(log LogFunc) {
	reg, err := loadCronRegistry()
	if err != nil {
		debugWarn("cron_tick", "error", err)
		return
	}
	now := time.Now().UTC()
	for _, job := range reg.Jobs {
		if job.State != CronStateScheduled || !job.Enabled || job.NextRunAt == nil {
			continue
		}
		if job.NextRunAt.After(now) {
			continue
		}
		// Guard against double-fire.
		cronRunningMu.Lock()
		if cronRunning[job.ID] {
			cronRunningMu.Unlock()
			continue
		}
		cronRunning[job.ID] = true
		cronRunningMu.Unlock()

		// Mark running so the next tick sees it in-flight.
		markJobRunning(job.ID)
		jobCopy := job
		go runCronJob(jobCopy, log)
	}
}

// markJobRunning sets a job's state to running and persists.
func markJobRunning(id string) {
	reg, err := loadCronRegistry()
	if err != nil {
		return
	}
	for i := range reg.Jobs {
		if reg.Jobs[i].ID == id {
			reg.Jobs[i].State = CronStateRunning
			_ = saveCronRegistry(reg)
			return
		}
	}
}

// runCronJob executes a single scheduled job in a headless sub-agent runner,
// mirroring delegateTaskHandler's structure. It manages the in-flight map,
// builds the task prompt (with skill injection), constructs a restricted
// sub-agent with an isolated session, runs it with a watchdog + loop guard,
// writes an output artifact, and updates the registry.
func runCronJob(job CronJob, log LogFunc) {
	cronRunningMu.Lock()
	cronRunning[job.ID] = true
	cronRunningMu.Unlock()
	defer func() {
		cronRunningMu.Lock()
		delete(cronRunning, job.ID)
		cronRunningMu.Unlock()
	}()

	notifyCronJob("started", job.ID, job.Name, job.Prompt, "")

	if currentModel == nil {
		notifyCronJob("failed", job.ID, job.Name, "currentModel is nil; bootstrap required", "")
		log(fmt.Sprintf("[cron] job %s failed: currentModel is nil", job.ID))
		return
	}

	// Build the full prompt with optional skill injection.
	finalPrompt := buildCronPrompt(job, log)

	// Build sub-agent tools.
	parentEnv := os.Environ()
	subAgentLogFunc := func(msg string) { /* cron executor does not stream to TUI */ }
	subAgentTools, subAgentToolsets := buildSubAgentTools("default", parentEnv, subAgentLogFunc)
	subAgentTools = filterBlockedTools(subAgentTools)

	genCfg := buildGenerationConfig("")

	subAgent, err := llmagent.New(llmagent.Config{
		Name:                  fmt.Sprintf("cron_%s", job.ID),
		Description:           "Scheduled cron job agent",
		Instruction:           buildCronInstruction(job.Name),
		Model:                 currentModel,
		Tools:                 subAgentTools,
		Toolsets:              subAgentToolsets,
		GenerateContentConfig: genCfg,
		BeforeModelCallbacks:  []llmagent.BeforeModelCallback{visionInjectionCallback},
	})
	if err != nil {
		log(fmt.Sprintf("[cron] job %s failed to create sub-agent: %v", job.ID, err))
		updateCronJobAfterRun(job, "failed", "", "")
		notifyCronJob("failed", job.ID, job.Name, fmt.Sprintf("agent creation: %v", err), "")
		return
	}

	msg := genai.NewContentFromText(finalPrompt, genai.RoleUser)

	var summary strings.Builder

	subRunner, err := runner.New(runner.Config{
		AppName:           fmt.Sprintf("hakase_cron_%s", job.ID),
		Agent:             subAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		log(fmt.Sprintf("[cron] job %s failed to create runner: %v", job.ID, err))
		updateCronJobAfterRun(job, "failed", "", "")
		notifyCronJob("failed", job.ID, job.Name, fmt.Sprintf("runner creation: %v", err), "")
		return
	}

	// Watchdog: idle timeout and hard ceiling, same as delegateTaskHandler.
	var runCtx context.Context
	var cancel context.CancelFunc
	watchdogActive := delegateTimeout > 0
	var lastActivityMu sync.Mutex
	lastActivity := time.Now()
	touchActivity := func() {
		lastActivityMu.Lock()
		lastActivity = time.Now()
		lastActivityMu.Unlock()
	}
	done := make(chan struct{})
	if watchdogActive {
		runCtx, cancel = context.WithCancel(context.Background())
		defer cancel()
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			ceiling := time.NewTimer(3 * delegateTimeout)
			defer ceiling.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					lastActivityMu.Lock()
					idle := time.Since(lastActivity)
					lastActivityMu.Unlock()
					if idle > delegateTimeout {
						cancel()
						return
					}
				case <-ceiling.C:
					cancel()
					return
				}
			}
		}()
	} else {
		runCtx = context.Background()
	}

	guard := guardDefaults(currentGuard)
	guardCtx, guardCancel := context.WithCancel(runCtx)
	defer guardCancel()

	var finalErr error
	attempt := 0
	taskID := job.ID

	for {
		repaired := false
		for ev, runErr := range subRunner.Run(guardCtx, "cron_scheduler", taskID, msg, agent.RunConfig{}) {
			if runErr != nil {
				if isToolCallJSONErr(runErr) && attempt < maxToolCallRepairAttempts {
					debugWarn("tool_call_repair", "cron_job", job.ID, "attempt", attempt+1, "error", runErr)
					msg = toolCallRepairMessage(runErr, attempt)
					attempt++
					repaired = true
					break
				}
				finalErr = runErr
				break
			}
			if ev == nil {
				continue
			}
			touchActivity()
			if ev.Content != nil {
				for _, part := range ev.Content.Parts {
					if part.Text != "" {
						summary.WriteString(part.Text)
						if !part.Thought {
							if reason := guard.feed(part.FunctionCall != nil, part.Text); reason != "" {
								guardCancel()
								debugWarn("cron_guard_abort", "job_id", job.ID, "reason", reason)
								finalErr = fmt.Errorf("cron job %s aborted: %s", job.ID, reason)
								break
							}
						}
					}
				}
			}
		}
		if !repaired {
			break
		}
	}
	close(done)

	summaryText := strings.TrimSpace(summary.String())

	// Check for [SILENT] marker.
	silent := strings.Contains(summaryText, "[SILENT]")
	if silent {
		summaryText = strings.ReplaceAll(summaryText, "[SILENT]", "")
		summaryText = strings.TrimSpace(summaryText)
	}

	// Write output artifact.
	outputPath := writeCronOutput(job, summaryText, silent)

	status := "ok"
	if watchdogActive && runCtx.Err() != nil {
		status = "failed"
		finalErr = fmt.Errorf("cron job %s timed out", job.ID)
	} else if finalErr != nil {
		status = "failed"
	} else if silent {
		status = "silent"
	}

	updateCronJobAfterRun(job, status, summaryText, outputPath)

	if silent {
		notifyCronJob("silent", job.ID, job.Name, "", outputPath)
	} else if status == "failed" {
		notifyCronJob("failed", job.ID, job.Name, truncate(summaryText, 300), outputPath)
	} else {
		notifyCronJob("completed", job.ID, job.Name, truncate(summaryText, 300), outputPath)
	}
}

// buildCronPrompt assembles the full task prompt with optional skill injection.
func buildCronPrompt(job CronJob, log LogFunc) string {
	if len(job.Skills) == 0 {
		return job.Prompt
	}

	skills, _, err := resolveCronSkills(job.Skills, log)
	if err != nil {
		// Skills that fail at run time are skipped, not fatal.
		log(fmt.Sprintf("[cron] job %s skill resolution warning: %v", job.ID, err))
	}

	var sb strings.Builder
	sb.WriteString("Attached skill context:\n")
	for _, s := range skills {
		sb.WriteString(fmt.Sprintf("### SKILL: %s\n%s\n\n", s.Frontmatter.Name, s.Body))
	}
	sb.WriteString("---\n")
	sb.WriteString("Task: ")
	sb.WriteString(job.Prompt)
	return sb.String()
}

// buildCronInstruction returns the system instruction for a headless cron
// sub-agent, modeled after buildSubAgentInstruction.
func buildCronInstruction(name string) string {
	return fmt.Sprintf(
		"You are a scheduled task runner for cron job '%s'. You have no conversation history; the prompt contains everything needed. "+
			"Execute the task and return a concise result. Do not call delegate_task, clarify, memory, send_message, or cronjob.", name)
}

// writeCronOutput saves the job summary to outputs/cron/<id>-<ts>.md.
func writeCronOutput(job CronJob, summaryText string, silent bool) string {
	dir := filepath.Join("outputs", "cron")
	_ = os.MkdirAll(dir, 0755)

	ts := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("%s-%s.md", job.ID, ts)
	path := filepath.Join(dir, filename)

	status := "ok"
	if silent {
		status = "silent"
	}

	content := fmt.Sprintf(
		"# Cron Job %s (%s)\n\n"+
			"- **Schedule**: %s\n"+
			"- **Started**: %s\n"+
			"- **Finished**: %s\n"+
			"- **Status**: %s\n\n"+
			"%s\n",
		job.ID, job.Name, job.Schedule,
		time.Now().UTC().Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339),
		status,
		summaryText,
	)
	_ = os.WriteFile(path, []byte(content), 0644)
	return path
}

// updateCronJobAfterRun updates the registry after a job completes or fails.
func updateCronJobAfterRun(job CronJob, status string, summaryText, outputPath string) {
	reg, err := loadCronRegistry()
	if err != nil {
		return
	}
	for i := range reg.Jobs {
		if reg.Jobs[i].ID == job.ID {
			now := time.Now().UTC()
			reg.Jobs[i].LastRunAt = &now
			reg.Jobs[i].LastStatus = status
			reg.Jobs[i].RunCount++
			reg.Jobs[i].OutputPath = outputPath
			reg.Jobs[i].State = CronStateScheduled // clear running

			// Compute next run.
			sched, err := parseSchedule(reg.Jobs[i].Schedule)
			if err != nil || sched.OneShot || (reg.Jobs[i].Repeat > 0 && reg.Jobs[i].RunCount >= reg.Jobs[i].Repeat) {
				reg.Jobs[i].State = CronStateCompleted
				reg.Jobs[i].NextRunAt = nil
				reg.Jobs[i].Enabled = false
			} else {
				next, nerr := sched.next(now)
				if nerr != nil {
					reg.Jobs[i].State = CronStateCompleted
					reg.Jobs[i].NextRunAt = nil
				} else {
					reg.Jobs[i].NextRunAt = &next
				}
			}
			_ = saveCronRegistry(reg)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// 6. Skill resolution helper
// ---------------------------------------------------------------------------

// resolveCronSkills discovers markdown skills and returns the subset that
// match the requested names (case-insensitive on frontmatter name). Returns
// the resolved skills, the set of missing names, and an error listing missing
// names when any are not found.
func resolveCronSkills(names []string, log LogFunc) ([]MarkdownSkill, map[string]bool, error) {
	cwd, _ := os.Getwd()
	all := DiscoverMarkdownSkills(cwd, currentConfig.SkillDirs, log)

	byName := make(map[string]MarkdownSkill, len(all))
	for _, s := range all {
		key := strings.ToLower(s.Frontmatter.Name)
		byName[key] = s
	}

	var resolved []MarkdownSkill
	missing := make(map[string]bool)
	for _, name := range names {
		if _, ok := missing[name]; ok {
			continue
		}
		key := strings.ToLower(name)
		if sk, ok := byName[key]; ok {
			resolved = append(resolved, sk)
		} else {
			missing[name] = true
		}
	}
	if len(missing) > 0 {
		var msgs []string
		for n := range missing {
			msgs = append(msgs, n)
		}
		sort.Strings(msgs)
		return resolved, missing, fmt.Errorf("skill(s) not found: %s", strings.Join(msgs, ", "))
	}
	return resolved, nil, nil
}

// ---------------------------------------------------------------------------
// 7. CLI support helpers
// ---------------------------------------------------------------------------

// cronModelBootstrap performs the minimal provider bootstrap needed so the
// CLI can run jobs headless. It mirrors the top of setupRunner (lines
// 1467-1530) without duplicating the full runner setup.
func cronModelBootstrap() error {
	cfg, err := loadConfig(resolveConfigPath("config.json"))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	currentSandbox = LoadSandboxConfig(cfg.Sandbox)
	currentApproval = cfg.Approval
	currentClarify = cfg.Clarify
	currentGuard = loopGuardConfig(cfg.LoopGuard)
	currentConfig = cfg

	provider, err := ProviderFactory(cfg)
	if err != nil {
		return fmt.Errorf("create provider: %w", err)
	}
	if err := provider.ValidateConfig(cfg); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	modelName := cfg.ModelName
	if modelName == "" {
		modelName = provider.GetDefaultModel()
	}
	model, err := provider.CreateModel(context.Background(), modelName, cfg.APIKey)
	if err != nil {
		return fmt.Errorf("create model: %w", err)
	}
	currentModel = model

	if cfg.DelegateTimeoutSeconds > 0 {
		delegateTimeout = time.Duration(cfg.DelegateTimeoutSeconds) * time.Second
	} else {
		delegateTimeout = 300 * time.Second
	}

	// MCP manager is needed by buildSubAgentTools for the "default" agent
	// type. We create it here so sub-agent toolsets resolve correctly. A
	// broken MCP config is a warning, not a blocker: cron runs without MCP
	// tools rather than failing entirely.
	mcpManager, err := NewMCPServerManager(cfg, func(string) {})
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: warning: mcp servers unavailable: %v\n", err)
	} else {
		currentMCPManager = mcpManager
	}

	return nil
}

// dueCronJobs returns the subset of jobs in the registry whose next fire time
// is at or before now. Nil next-runs are skipped.
func dueCronJobs(now time.Time, reg CronRegistry) []CronJob {
	var due []CronJob
	for _, j := range reg.Jobs {
		if j.State == CronStateScheduled && j.Enabled && j.NextRunAt != nil && !j.NextRunAt.After(now) {
			due = append(due, j)
		}
	}
	return due
}

// triggerCronJob loads the registry, resolves the job by ID or name, spawns a
// background run goroutine, and returns the resolved job. The caller receives
// the job immediately; execution is asynchronous.
func triggerCronJob(idOrName string, log LogFunc) (CronJob, error) {
	reg, err := loadCronRegistry()
	if err != nil {
		return CronJob{}, err
	}
	job, err := getCronJob(reg, idOrName)
	if err != nil {
		return CronJob{}, err
	}
	jobCopy := *job
	go runCronJob(jobCopy, log)
	return jobCopy, nil
}
