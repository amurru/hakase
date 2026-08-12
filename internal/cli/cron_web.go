// cron_web.go - exported wrappers for the web API handlers.
// These thin functions expose the internal cron registry operations so the
// HTTP handlers in internal/web/handlers can manage cron jobs without
// duplicating the persistence logic.
package cli

import (
	"fmt"
	"time"
)

// CronLoadRegistry loads the cron job registry from disk.
// Thread-safe (uses cronRegistryMu internally).
func CronLoadRegistry() (CronRegistry, error) {
	return loadCronRegistry()
}

// CronGetJob finds a job by exact ID or case-insensitive name.
// Returns an error if not found or ambiguous.
func CronGetJob(reg CronRegistry, idOrName string) (*CronJob, error) {
	return getCronJob(reg, idOrName)
}

// CronSaveRegistry writes the registry to disk atomically.
// Thread-safe (uses cronRegistryMu internally).
func CronSaveRegistry(reg CronRegistry) error {
	return saveCronRegistry(reg)
}

// CronTriggerJob triggers a job in the background, returning immediately.
// The job runs asynchronously; the returned CronJob is the snapshot at trigger time.
func CronTriggerJob(idOrName string) (CronJob, error) {
	job, err := triggerCronJob(idOrName, func(msg string) { /* no-op for HTTP */ })
	if err != nil {
		return CronJob{}, err
	}
	return job, nil
}

// CronParseSchedule parses a raw schedule string and returns the next fire time.
// Used by the resume handler to recompute NextRunAt.
func CronParseSchedule(raw string) (time.Time, error) {
	sched, err := parseSchedule(raw)
	if err != nil {
		return time.Time{}, err
	}
	next, err := sched.next(time.Now().UTC())
	if err != nil {
		return time.Time{}, fmt.Errorf("cannot compute next run: %v", err)
	}
	return next, nil
}
