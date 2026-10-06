// resume.go - durable-resume shared helpers (docs/durable-resume/plan.md).
//
// Phase 3: gate wrappers (ApproveExec, askClarify) record in-flight
// pauses to deps.PauseRegistry and remove them on resolve, so a
// restart can find interrupted pauses (Phases 5/7 consume the
// records). Recording is best-effort and never fails a gate.
package agent

import (
	"context"
	"time"

	hakasesession "amurru/hakase/internal/session"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/util"
)

// DefaultMaxResumeAgeMinutes caps how old a paused gate may be to
// resume when durable_resume.max_resume_age_minutes is unset (<= 0).
const DefaultMaxResumeAgeMinutes = 30

// ResumeMaxAge returns the configured max pause age for resume.
// Defaults to 30 minutes when not configured.
func ResumeMaxAge() time.Duration {
	if deps == nil || deps.Config == nil || deps.Config.DurableResume.MaxResumeAgeMinutes <= 0 {
		return time.Duration(DefaultMaxResumeAgeMinutes) * time.Minute
	}
	return time.Duration(deps.Config.DurableResume.MaxResumeAgeMinutes) * time.Minute
}

// recordGatePause persists a pause record for a gate that is about to
// block. Returns the pause ID, or "" when the registry is unavailable
// (disabled or unusable) — callers treat "" as "not recorded" and
// proceed with the gate unchanged.
func recordGatePause(ctx context.Context, gate, hakaseSessionID, summary string, detail map[string]any) string {
	if deps == nil || deps.PauseRegistry == nil {
		return ""
	}
	id, err := deps.PauseRegistry.Record(pauseRecord(
		ctx, gate, hakaseSessionID, summary, detail,
	))
	if err != nil {
		util.DebugWarn("pause_record_failed", "gate", gate, "error", err.Error())
		return ""
	}
	return id
}

// unrecordGatePause removes a pause record; "" is a no-op.
func unrecordGatePause(pauseID string) {
	if pauseID == "" || deps == nil || deps.PauseRegistry == nil {
		return
	}
	if err := deps.PauseRegistry.Unrecord(pauseID); err != nil {
		util.DebugWarn("pause_unrecord_failed", "pause_id", pauseID, "error", err.Error())
	}
}

// pauseRecord builds the session PauseRecord. The ADK session ID comes
// from the invocation context (per-turn task ID); empty outside a
// registered run.
func pauseRecord(ctx context.Context, gate, hakaseSessionID, summary string, detail map[string]any) hakasesession.PauseRecord {
	return hakasesession.PauseRecord{
		HakaseSessionID: hakaseSessionID,
		ADKSessionID:    interfaces.TaskIDFromCtx(ctx),
		Gate:            gate,
		Summary:         summary,
		Detail:          detail,
	}
}
