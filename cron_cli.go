// cron_cli.go - the `hakase cron` CLI: list, status, pause, resume, run, tick
package main

import (
	"amurru/hakase/internal/config"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func runCronCLI(args []string) int {
	if len(args) == 0 {
		cronCLIUsage()
		return 2
	}
	switch args[0] {
	case "list", "ls":
		return runCronList(args[1:])
	case "status":
		return runCronStatus(args[1:])
	case "pause":
		return runCronPause(args[1:])
	case "resume":
		return runCronResume(args[1:])
	case "run":
		return runCronRun(args[1:])
	case "tick":
		return runCronTick(args[1:])
	case "help", "-h", "--help":
		cronCLIUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown cron subcommand %q\n\n", args[0])
		cronCLIUsage()
		return 2
	}
}

func cronCLIUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase cron <subcommand>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list (ls)  list all scheduled jobs")
	fmt.Fprintln(os.Stderr, "  status     show registry path and job counts by state")
	fmt.Fprintln(os.Stderr, "  pause      pause a job by ID or name")
	fmt.Fprintln(os.Stderr, "  resume     resume a paused job by ID or name")
	fmt.Fprintln(os.Stderr, "  run        trigger a job immediately by ID or name")
	fmt.Fprintln(os.Stderr, "  tick       run all due jobs once")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Schedule formats:")
	fmt.Fprintln(os.Stderr, "  '30m' / '2h' / '1d'           relative one-shot")
	fmt.Fprintln(os.Stderr, "  'every 30m' / 'every 2 hours'  recurring interval")
	fmt.Fprintln(os.Stderr, "  '0 9 * * *'                   5-field cron")
	fmt.Fprintln(os.Stderr, "  '2026-06-01T09:00:00'         ISO timestamp")
}

// ------------------- list ----------------------------------------------------

func runCronList(args []string) int {
	reg, err := loadCronRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load cron registry: %v\n", err)
		return 1
	}

	if len(reg.Jobs) == 0 {
		fmt.Println("No cron jobs found.")
		return 0
	}

	// Sort by NextRunAt: nil last, otherwise chronological.
	sort.Slice(reg.Jobs, func(i, j int) bool {
		ni, nj := reg.Jobs[i].NextRunAt, reg.Jobs[j].NextRunAt
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

	fmt.Printf("Cron jobs (%d):\n", len(reg.Jobs))
	now := time.Now()
	for _, job := range reg.Jobs {
		label := job.Name
		if label == "" {
			label = job.ID
		}
		fmt.Printf("  %s %s\n", cronStateSymbol(job.State), label)

		nextStr := "-"
		if job.NextRunAt != nil {
			if !job.NextRunAt.After(now) {
				nextStr = "now"
			} else {
				nextStr = job.NextRunAt.Local().Format("2006-01-02 15:04")
			}
		}
		lastStatus := job.LastStatus
		if lastStatus == "" {
			lastStatus = "-"
		}
		enabled := "yes"
		if !job.Enabled {
			enabled = "no"
		}
		fmt.Printf("    ID: %s | Schedule: %s | State: %s | Enabled: %s | Next: %s | Last: %s | Runs: %d\n",
			job.ID, job.Schedule, job.State, enabled, nextStr, lastStatus, job.RunCount)
		if job.Prompt != "" {
			prompt := job.Prompt
			if len(prompt) > 80 {
				prompt = prompt[:80] + "..."
			}
			fmt.Printf("    Prompt: %s\n", prompt)
		}
		if len(job.Skills) > 0 {
			fmt.Printf("    Skills: %s\n", strings.Join(job.Skills, ", "))
		}
		fmt.Println()
	}

	return 0
}

// ------------------- status --------------------------------------------------

func runCronStatus(args []string) int {
	path := filepath.Join(config.HakaseHome(), "cronjobs.json")
	reg, err := loadCronRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load cron registry: %v\n", err)
		return 1
	}

	counts := map[CronJobState]int{}
	for _, job := range reg.Jobs {
		counts[job.State]++
	}

	fmt.Printf("Registry: %s\n", path)
	fmt.Printf("Jobs: %d\n", len(reg.Jobs))
	stateOrder := []CronJobState{CronStateScheduled, CronStatePaused, CronStateRunning, CronStateCompleted}
	for _, state := range stateOrder {
		fmt.Printf("  %s: %d\n", state, counts[state])
	}
	fmt.Println()
	fmt.Println("Note: the in-process scheduler only runs while hakase (the TUI) is open.")
	fmt.Println("Use 'hakase cron tick' to run due jobs once from the CLI.")
	return 0
}

// ------------------- pause ---------------------------------------------------

func runCronPause(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase cron pause <id>")
		return 2
	}
	id := args[0]

	reg, err := loadCronRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load cron registry: %v\n", err)
		return 1
	}
	job, err := getCronJob(reg, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	job.State = CronStatePaused
	job.Enabled = false
	job.NextRunAt = nil
	job.UpdatedAt = time.Now().UTC()

	if err := saveCronRegistry(reg); err != nil {
		fmt.Fprintf(os.Stderr, "failed to save cron registry: %v\n", err)
		return 1
	}

	jobName := job.Name
	if jobName == "" {
		jobName = job.ID
	}
	fmt.Printf("Paused job %s (%s)\n", job.ID, jobName)
	return 0
}

// ------------------- resume --------------------------------------------------

func runCronResume(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase cron resume <id>")
		return 2
	}
	id := args[0]

	reg, err := loadCronRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load cron registry: %v\n", err)
		return 1
	}
	job, err := getCronJob(reg, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	if job.State == CronStateCompleted {
		fmt.Fprintf(os.Stderr, "cannot resume a completed job\n")
		return 1
	}

	sched, err := parseSchedule(job.Schedule)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot parse stored schedule: %v\n", err)
		return 1
	}
	now := time.Now().UTC()
	next, err := sched.next(now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot compute next run: %v\n", err)
		return 1
	}

	job.State = CronStateScheduled
	job.Enabled = true
	job.NextRunAt = &next
	job.UpdatedAt = now

	if err := saveCronRegistry(reg); err != nil {
		fmt.Fprintf(os.Stderr, "failed to save cron registry: %v\n", err)
		return 1
	}

	jobName := job.Name
	if jobName == "" {
		jobName = job.ID
	}
	fmt.Printf("Resumed job %s (%s) - next run at %s\n",
		job.ID, jobName, next.Local().Format("2006-01-02 15:04"))
	return 0
}

// ------------------- run -----------------------------------------------------

func runCronRun(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase cron run <id>")
		return 2
	}
	id := args[0]

	if err := cronModelBootstrap(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to bootstrap model: %v\n", err)
		return 1
	}

	job, err := triggerCronJob(id, func(msg string) {
		fmt.Fprintln(os.Stderr, msg)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	jobName := job.Name
	if jobName == "" {
		jobName = job.ID
	}
	fmt.Printf("Triggered %s (%s) in the background\n", jobName, job.ID)
	return 0
}

// ------------------- tick ----------------------------------------------------

func runCronTick(args []string) int {
	if err := cronModelBootstrap(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to bootstrap model: %v\n", err)
		return 1
	}

	reg, err := loadCronRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load cron registry: %v\n", err)
		return 1
	}

	due := dueCronJobs(time.Now(), reg)
	if len(due) == 0 {
		fmt.Println("No due jobs.")
		return 0
	}

	for _, job := range due {
		_, err := triggerCronJob(job.ID, func(msg string) {
			fmt.Fprintln(os.Stderr, msg)
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to trigger %s: %v\n", job.ID, err)
			continue
		}
		jobName := job.Name
		if jobName == "" {
			jobName = job.ID
		}
		fmt.Printf("Triggered %s (%s) in the background\n", jobName, job.ID)
	}
	return 0
}

// ------------------- helpers -------------------------------------------------

// cronStateSymbol returns a compact visual indicator for a cron job state.
func cronStateSymbol(s CronJobState) string {
	switch s {
	case CronStateScheduled:
		return "[S]"
	case CronStatePaused:
		return "[P]"
	case CronStateRunning:
		return "[R]"
	case CronStateCompleted:
		return "[C]"
	default:
		return "[?]"
	}
}
