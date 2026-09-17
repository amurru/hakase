// lr.go - learning-rate scheduling for markdown-skill evolution (plan
// Phase 3, SL-030): the per-epoch edit budget decays from the configured
// learning rate toward a floor as nights accumulate, mirroring SkillOpt's
// constant/linear/cosine schedulers. Pure and deterministic so the night
// report can show exactly why an epoch had the budget it had.
package skill

import (
	"math"
	"strings"
)

// ScheduledEditBudget computes the effective edit budget for one epoch.
//
//   - scheduler "constant" (default, including "") returns base.
//   - "linear"/"cosine" decay from base to floor across horizon epochs:
//     linear interpolates 1-t, cosine 0.5*(1+cos(pi*t)), with
//     t = clamp(epoch/horizon). epoch >= horizon returns floor.
//   - horizon <= 0 leaves no decay schedule, so base is returned for every
//     scheduler (a scheduler without a horizon cannot decay).
//   - floor < 0 clamps to 0; floor >= base clamps to base.
//   - unknown schedulers fail closed to constant.
//
// base <= 0 is returned unchanged (the caller substitutes its default).
func ScheduledEditBudget(base int, scheduler string, epoch, horizon, floor int) int {
	if base <= 0 {
		return base
	}
	if floor < 0 {
		floor = 0
	}
	if floor > base {
		floor = base
	}
	sched := strings.ToLower(strings.TrimSpace(scheduler))
	switch sched {
	case "linear", "cosine":
	default:
		return base
	}
	if horizon <= 0 {
		return base
	}
	t := float64(epoch) / float64(horizon)
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	var factor float64
	if sched == "linear" {
		factor = 1 - t
	} else {
		factor = 0.5 * (1 + math.Cos(math.Pi*t))
	}
	budget := floor + int(float64(base-floor)*factor+0.5)
	if budget < floor {
		budget = floor
	}
	if budget > base {
		budget = base
	}
	return budget
}
