// envconfig.go - HAKASE_SLEEP_* environment overrides for the sleep cycle
// (plan SL-023). The allowed key set is explicit and minimal; anything else
// with the prefix is ignored with a warning, and the two redaction-related
// keys are HARD ERRORS: redaction can only be disabled by the explicit
// --allow-unredacted CLI flag, never by env or file (SL-004 file-only-never,
// normative).
package sleep

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// SleepEnv carries the env overrides applied on top of the file config.
// Zero fields mean "no override".
type SleepEnv struct {
	Model            string
	JudgeModel       string
	MaxTokens        int
	LookbackHours    int
	MaxTasks         int
	LLMMine          bool
	FanOut           bool
	AutoAdopt        bool
	IncludeAuditArgs bool
}

// AllowedSleepEnvKeys is the explicit allowlist (SL-023). The cycle and CLI
// document exactly these; new keys land here first.
const AllowedSleepEnvKeys = "HAKASE_SLEEP_MODEL, HAKASE_SLEEP_JUDGE_MODEL, HAKASE_SLEEP_MAX_TOKENS, HAKASE_SLEEP_LOOKBACK_HOURS, HAKASE_SLEEP_MAX_TASKS, HAKASE_SLEEP_LLM_MINE, HAKASE_SLEEP_FAN_OUT, HAKASE_SLEEP_AUTO_ADOPT, HAKASE_SLEEP_INCLUDE_AUDIT_ARGS"

// LoadSleepEnv scans environ for HAKASE_SLEEP_* keys. Redaction-related keys
// fail closed; unknown keys warn and are skipped; the rest parse into
// overrides. environ is injectable for tests.
func LoadSleepEnv(environ []string) (SleepEnv, []string, error) {
	var env SleepEnv
	var warnings []string
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if !strings.HasPrefix(key, "HAKASE_SLEEP_") {
			continue
		}
		upper := strings.ToUpper(key)
		if strings.Contains(upper, "REDACT") || strings.Contains(upper, "ALLOW_UNREDACTED") {
			return SleepEnv{}, nil, fmt.Errorf("sleep: %s is not a valid override: redaction is controlled only by the explicit --allow-unredacted flag (SL-004)", key)
		}
		switch key {
		case "HAKASE_SLEEP_MODEL":
			env.Model = value
		case "HAKASE_SLEEP_JUDGE_MODEL":
			env.JudgeModel = value
		case "HAKASE_SLEEP_MAX_TOKENS":
			env.MaxTokens = atoiEnv(key, value)
		case "HAKASE_SLEEP_LOOKBACK_HOURS":
			env.LookbackHours = atoiEnv(key, value)
		case "HAKASE_SLEEP_MAX_TASKS":
			env.MaxTasks = atoiEnv(key, value)
		case "HAKASE_SLEEP_LLM_MINE":
			env.LLMMine = boolEnv(value)
		case "HAKASE_SLEEP_FAN_OUT":
			env.FanOut = boolEnv(value)
		case "HAKASE_SLEEP_AUTO_ADOPT":
			env.AutoAdopt = boolEnv(value)
		case "HAKASE_SLEEP_INCLUDE_AUDIT_ARGS":
			env.IncludeAuditArgs = boolEnv(value)
		default:
			warnings = append(warnings, fmt.Sprintf("ignoring unknown %s (allowed: %s)", key, AllowedSleepEnvKeys))
		}
	}
	return env, warnings, nil
}

// LoadSleepEnvFromOS is the process-environment convenience wrapper.
func LoadSleepEnvFromOS() (SleepEnv, []string, error) {
	return LoadSleepEnv(os.Environ())
}

// atoiEnv parses an int env value; unparseable values yield 0 (no override)
// - a malformed override must not silently change spend caps.
func atoiEnv(key, value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return n
}

// boolEnv interprets common boolean spellings; anything else is false.
func boolEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// ApplyToCycleOpts merges non-zero env overrides over opts. Explicit flags
// are applied by the caller AFTER this (flags > env > file).
func (e SleepEnv) ApplyToCycleOpts(opts *CycleOpts) {
	if e.MaxTokens > 0 {
		opts.MaxTokensPerNight = e.MaxTokens
	}
	if e.LookbackHours > 0 {
		opts.Harvest.LookbackHours = e.LookbackHours
	}
	if e.MaxTasks > 0 {
		opts.Mine.MaxTasksPerNight = e.MaxTasks
	}
	if e.FanOut {
		opts.FanOut = true
	}
	if e.AutoAdopt {
		opts.AutoAdopt = true
	}
	if e.IncludeAuditArgs {
		opts.Harvest.IncludeAuditArgs = true
	}
}
