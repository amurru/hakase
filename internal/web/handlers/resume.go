// resume.go - transport run-view resurrection (durable-resume Phase 7).
//
// After a restart, in-flight gate prompts are gone from memory (the
// gates' pending maps died with the process) but their pause records
// survive in the registry. This file closes the loop on the web
// transport:
//
//   - GET /api/resumable lists interrupted pauses for "interrupted —
//     click to resume" display.
//   - ResurrectInterruptedPrompts re-emits gate prompts for resumable
//     pauses at startup (behind AutoResumeOnStartup), addressed so the
//     existing POST /approvals|clarifications/{id}/respond endpoints
//     answer them: unknown live IDs fall through to the resume backend
//     implemented here by *ChatAPI.
//   - Approval answers settle via ResolveApprovalPause; an approval
//     re-drives a fresh turn with the paused turn's original input. A
//     clarify answer resumes the paused turn in the background,
//     streaming to the asking session like any other run.
//
// TUI and channel transports share the agent driver (ListResumablePauses,
// ResumeClarify, ResolveApprovalPause) but need their own prompt
// re-emission; that wiring is a follow-up.
package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"
)

// resurrectedPromptPrefix marks prompt IDs that re-emit an interrupted
// pause (no live handler waits for them).
const resurrectedPromptPrefix = "rsm_"

// ResumeBackend answers re-emitted gate prompts. *ChatAPI implements it;
// the gates hold it and the respond endpoints fall through to it when
// no live prompt matches. Status codes mirror the live endpoints (200
// settled, 202 redriving in background) with 404/409/410/429/503 for
// the resume-specific failures.
type ResumeBackend interface {
	AnswerResurrectedApproval(ctx context.Context, promptID string, approved bool) (int, map[string]string)
	AnswerResurrectedClarify(ctx context.Context, promptID string, resp interfaces.ClarifyResponse) (int, map[string]string)
}

// Compile-time check: answering runs through ChatAPI (driver, bridge,
// session store, semaphores).
var _ ResumeBackend = (*ChatAPI)(nil)

// resumablePauseJSON is the GET /api/resumable element shape.
type resumablePauseJSON struct {
	PauseID   string         `json:"pause_id"`
	SessionID string         `json:"session_id"`
	Gate      string         `json:"gate"`
	Summary   string         `json:"summary"`
	Detail    map[string]any `json:"detail,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	OpenCalls []string       `json:"open_calls"`
}

// GetResumable handles GET /api/resumable: interrupted pauses that can
// still be answered. Empty (not 503) when durable resume is off — there
// is simply nothing to resume.
func (api *ChatAPI) GetResumable(w http.ResponseWriter, r *http.Request) {
	pauses, err := hakaseagent.ListResumablePauses(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]resumablePauseJSON, 0, len(pauses))
	for _, p := range pauses {
		calls := make([]string, 0, len(p.OpenCalls))
		for _, c := range p.OpenCalls {
			calls = append(calls, c.ID)
		}
		out = append(out, resumablePauseJSON{
			PauseID:   p.Record.PauseID,
			SessionID: p.Record.HakaseSessionID,
			Gate:      p.Record.Gate,
			Summary:   p.Record.Summary,
			Detail:    p.Record.Detail,
			CreatedAt: p.Record.CreatedAt,
			OpenCalls: calls,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pauses": out})
}

// webGates resolves the process web gates behind the runtime for
// resurrection wiring. Comma-ok: other gate implementations (tests,
// TUI-embedded) simply skip re-emission.
func (api *ChatAPI) webGates() (*WebApprovalGate, *WebClarifyGate) {
	if api.runtime == nil {
		return nil, nil
	}
	ag, _ := api.runtime.ApprovalGate().(*WebApprovalGate)
	cg, _ := api.runtime.ClarifyGate().(*WebClarifyGate)
	return ag, cg
}

// ResurrectInterruptedPrompts re-emits gate prompts for every resumable
// pause so the UI can answer runs interrupted by a restart. Each
// re-emitted prompt carries a resurrected ID the respond endpoints
// route to the resume backend. Best-effort per pause: one bad record
// never blocks the rest. Returns the re-emitted count.
func (api *ChatAPI) ResurrectInterruptedPrompts(ctx context.Context) (int, error) {
	pauses, err := hakaseagent.ListResumablePauses(ctx)
	if err != nil {
		return 0, err
	}
	ag, cg := api.webGates()
	if ag == nil || cg == nil {
		return 0, fmt.Errorf("resurrect: web gates unavailable")
	}
	n := 0
	for _, p := range pauses {
		promptID := resurrectedPromptPrefix + p.Record.PauseID
		switch p.Record.Gate {
		case hakasesession.PauseGateApproval:
			ag.TrackResurrected(promptID, p.Record.PauseID)
			ag.bridge.SendApprovalPrompt("", p.Record.HakaseSessionID, promptID,
				strDetail(p.Record.Detail, "tool"),
				strDetail(p.Record.Detail, "risk"),
				strDetail(p.Record.Detail, "reason"),
				strDetail(p.Record.Detail, "command"))
			n++
		case hakasesession.PauseGateClarify:
			cg.TrackResurrected(promptID, p.Record.PauseID)
			cg.bridge.SendClarifyPrompt("", p.Record.HakaseSessionID, promptID,
				strDetail(p.Record.Detail, "question"),
				sliceDetail(p.Record.Detail, "choices"),
				boolDetail(p.Record.Detail, "multi_select"))
			n++
		default:
			log.Printf("resurrect: unknown gate %q for pause %s", p.Record.Gate, p.Record.PauseID)
		}
	}
	return n, nil
}

// AnswerResurrectedApproval settles a re-emitted approval prompt.
// Denial ends the run (the command never ran). Approval re-drives a
// fresh turn with the paused turn's original input in the background
// (202); the pre-grant installed by ResolveApprovalPause lets the
// re-issued call pass its gate without blocking.
func (api *ChatAPI) AnswerResurrectedApproval(ctx context.Context, promptID string, approved bool) (int, map[string]string) {
	ag, _ := api.webGates()
	if ag == nil {
		return http.StatusServiceUnavailable, map[string]string{"error": "resume unavailable"}
	}
	pauseID, ok := ag.ResurrectedPause(promptID)
	if !ok {
		return http.StatusNotFound, map[string]string{"error": "approval not found or expired"}
	}
	rec, err := hakaseagent.FindResumablePause(ctx, pauseID)
	if err != nil {
		ag.DropResurrected(promptID)
		return resumeStatus(err)
	}
	drive, err := hakaseagent.ResolveApprovalPause(ctx, pauseID, approved)
	if err != nil {
		if terminalResumeErr(err) {
			ag.DropResurrected(promptID)
		}
		status, body := resumeStatus(err)
		return status, body
	}
	ag.DropResurrected(promptID)
	if !drive {
		return http.StatusOK, map[string]string{"status": "denied"}
	}
	input, err := hakaseagent.PausedTurnInput(ctx, rec.Record.ADKSessionID)
	if err != nil {
		return http.StatusInternalServerError, map[string]string{"error": err.Error()}
	}
	sem := api.getOrCreateSem(rec.Record.HakaseSessionID)
	if !sem.acquire(2) {
		return http.StatusTooManyRequests, map[string]string{"error": "too many concurrent requests for this session"}
	}
	go func() {
		defer sem.release()
		api.runAgentTask(context.Background(), rec.Record.HakaseSessionID, input)
	}()
	return http.StatusAccepted, map[string]string{"status": "redriving", "session_id": rec.Record.HakaseSessionID}
}

// AnswerResurrectedClarify resumes a re-emitted clarify pause in the
// background (202): the answer becomes the paused tool's result and
// the continued turn streams to the asking session. The mapping is
// dropped only on success so a failed resume keeps its answer path.
func (api *ChatAPI) AnswerResurrectedClarify(ctx context.Context, promptID string, resp interfaces.ClarifyResponse) (int, map[string]string) {
	_, cg := api.webGates()
	if cg == nil {
		return http.StatusServiceUnavailable, map[string]string{"error": "resume unavailable"}
	}
	pauseID, ok := cg.ResurrectedPause(promptID)
	if !ok {
		return http.StatusNotFound, map[string]string{"error": "clarification not found or expired"}
	}
	rec, err := hakaseagent.FindResumablePause(ctx, pauseID)
	if err != nil {
		cg.DropResurrected(promptID)
		return resumeStatus(err)
	}
	if api.runner == nil {
		return http.StatusServiceUnavailable, map[string]string{"error": "agent runner unavailable"}
	}
	sem := api.getOrCreateSem(rec.Record.HakaseSessionID)
	if !sem.acquire(2) {
		return http.StatusTooManyRequests, map[string]string{"error": "too many concurrent requests for this session"}
	}
	go func() {
		defer sem.release()
		sink := bridgeSink{b: api.bridge}
		out, rerr := hakaseagent.ResumeClarify(context.Background(), api.runner, pauseID,
			hakaseagent.ClarifyResponse{Answer: resp.Answer, Canceled: resp.Canceled, TimedOut: resp.TimedOut})
		if rerr != nil {
			log.Printf("resurrect: clarify resume failed for pause %s: %v", pauseID, rerr)
			sink.OnLog(rec.Record.HakaseSessionID, fmt.Sprintf("Resume failed: %v", rerr))
			sink.OnDone(rec.Record.HakaseSessionID)
			return
		}
		cg.DropResurrected(promptID)
		if strings.TrimSpace(out) != "" {
			sink.OnStream(rec.Record.HakaseSessionID, out, "")
		}
		persistResumedAnswer(api.sessionSvc, rec.Record.HakaseSessionID, out)
		sink.OnDone(rec.Record.HakaseSessionID)
	}()
	return http.StatusAccepted, map[string]string{"status": "resuming", "session_id": rec.Record.HakaseSessionID}
}

// persistResumedAnswer appends a resumed turn's output to the hakase
// session transcript (same shape as agentrun persistence, without
// usage metadata which resume does not track).
func persistResumedAnswer(svc *hakasesession.SessionService, sessionID, content string) {
	if svc == nil || svc.Store() == nil || strings.TrimSpace(content) == "" {
		return
	}
	sess, err := svc.Store().Load(sessionID)
	if err != nil {
		log.Printf("resurrect: failed to load session %s for persistence: %v", sessionID, err)
		return
	}
	sess.AddMessageWithMetaAndAttachments("agent", content, "", 0, hakasesession.MessageKindText, nil)
	if err := svc.Store().Save(sess); err != nil {
		log.Printf("resurrect: failed to persist resumed answer for session %s: %v", sessionID, err)
	}
}

// resumeStatus maps resume-driver errors to HTTP statuses with the
// driver message preserved for the UI.
func resumeStatus(err error) (int, map[string]string) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not found") || strings.Contains(msg, "not resumable"):
		return http.StatusNotFound, map[string]string{"error": msg}
	case strings.Contains(msg, "expired"):
		return http.StatusGone, map[string]string{"error": msg}
	case strings.Contains(msg, "unsafe to re-drive"):
		return http.StatusConflict, map[string]string{"error": msg}
	default:
		return http.StatusInternalServerError, map[string]string{"error": msg}
	}
}

// terminalResumeErr reports whether a failed settle retired the pause
// (drop its prompt mapping): expired/gone pauses unrecord themselves,
// unsafe pauses keep record AND mapping for a later manual answer.
func terminalResumeErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "expired") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "not resumable") ||
		strings.Contains(msg, "no ADK session") ||
		strings.Contains(msg, "lacks tool/command")
}

// strDetail reads an optional string from a pause Detail map (the
// registry round-trips through JSON, so values are plain types).
func strDetail(detail map[string]any, key string) string {
	s, _ := detail[key].(string)
	return s
}

// sliceDetail reads an optional string slice (JSON []any form).
func sliceDetail(detail map[string]any, key string) []string {
	switch v := detail[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// boolDetail reads an optional bool from a pause Detail map.
func boolDetail(detail map[string]any, key string) bool {
	b, _ := detail[key].(bool)
	return b
}
