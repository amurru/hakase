// resume.go - durable-resume shared helpers and resume driver
// (docs/durable-resume/plan.md).
//
// Phase 3: gate wrappers (ApproveExec, askClarify) record in-flight
// pauses to deps.PauseRegistry and remove them on resolve, so a
// restart can find interrupted pauses.
//
// Phase 5: ListResumablePauses finds interrupted pauses whose ADK
// history still holds open long-running calls; ResumeClarify answers a
// clarify pause by injecting a FunctionResponse against the paused
// ADK session (the engine resumes without re-executing any tool);
// ResolveApprovalPause settles an approval pause (deny, or approve
// via a one-shot pre-grant plus a caller-driven fresh turn — the
// answer is NOT the tool result, so direct response injection would
// lie to the model about a command that never ran).
package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"
)

// ResumeAppName and ResumeUserID are the ADK session coordinates for
// main-harness turns. They must match SetupRunner (AppName) and the
// run drivers (agentrun, TUI) so the resume driver addresses the same
// sessions the paused turn wrote.
const (
	ResumeAppName = "hakase_harness"
	ResumeUserID  = "user-1"
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
// registered run. S10/H2 persisted boundary: the summary is redacted
// here as defense-in-depth (callers already redact), and string /
// []string detail values are redacted so the pause registry never
// persists cleartext secrets.
func pauseRecord(ctx context.Context, gate, hakaseSessionID, summary string, detail map[string]any) hakasesession.PauseRecord {
	return hakasesession.PauseRecord{
		HakaseSessionID: hakaseSessionID,
		ADKSessionID:    interfaces.TaskIDFromCtx(ctx),
		Gate:            gate,
		Summary:         util.RedactTruncate(summary, 0),
		Detail:          redactPauseDetail(detail),
	}
}

// redactPauseDetail redacts string and []string detail values for the
// persisted pause boundary (S10/H2). Non-string values pass through;
// redaction is idempotent so caller-redacted values are unchanged.
func redactPauseDetail(detail map[string]any) map[string]any {
	if detail == nil {
		return nil
	}
	out := make(map[string]any, len(detail))
	for k, v := range detail {
		switch t := v.(type) {
		case string:
			out[k] = util.RedactTruncate(t, 0)
		case []string:
			cp := make([]string, len(t))
			for i, s := range t {
				cp[i] = util.RedactTruncate(s, 0)
			}
			out[k] = cp
		default:
			out[k] = v
		}
	}
	return out
}

// OpenCall is one unanswered long-running tool call in a paused turn's
// ADK history: the marker the Phase 4 IsLongRunning flags produce.
type OpenCall struct {
	// ID is the FunctionCall ID the answer must reference.
	ID string
	// ToolName is the called tool, "" when the call part is missing
	// from history (marker without a matching call).
	ToolName string
}

// ResumablePause pairs an interrupted pause record with the open
// calls its ADK history still holds.
type ResumablePause struct {
	Record    hakasesession.PauseRecord
	OpenCalls []OpenCall
}

// adkServiceForResume opens the durable ADK service over the session
// store dir. A fresh instance is fine (read-through JSON store); the
// spike tests resume across instances this way.
func adkServiceForResume() (*hakasesession.DurableADKService, error) {
	if deps == nil || deps.SessionService == nil || deps.SessionService.Store() == nil {
		return nil, fmt.Errorf("durable_resume: no session store configured")
	}
	return hakasesession.NewDurableADKService(deps.SessionService.Store().Dir())
}

// scanPausedSession reads a paused turn's ADK history and splits it
// into open long-running calls (marked, unanswered — the engine's
// openLongRunningCallIDs shape) and completed tool-call counts by
// tool name (for the Phase 6 never-replay guard). A missing session
// is not an error: pruned history simply means nothing to resume.
func scanPausedSession(ctx context.Context, svc *hakasesession.DurableADKService, adkSessionID string) (open []OpenCall, completed map[string]int, err error) {
	completed = make(map[string]int)
	resp, gerr := svc.Get(ctx, &adksession.GetRequest{
		AppName: ResumeAppName, UserID: ResumeUserID, SessionID: adkSessionID,
	})
	if gerr != nil {
		if errors.Is(gerr, adksession.ErrNotFound) {
			return nil, completed, nil
		}
		return nil, nil, fmt.Errorf("durable_resume: read paused session: %w", gerr)
	}
	marked := make(map[string]struct{})
	callNames := make(map[string]string)
	answered := make(map[string]struct{})
	for ev := range resp.Session.Events().All() {
		if ev == nil {
			continue
		}
		for _, id := range ev.LongRunningToolIDs {
			marked[id] = struct{}{}
		}
		if ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p == nil {
				continue
			}
			if p.FunctionCall != nil && p.FunctionCall.ID != "" {
				callNames[p.FunctionCall.ID] = p.FunctionCall.Name
			}
			if p.FunctionResponse != nil && p.FunctionResponse.ID != "" {
				answered[p.FunctionResponse.ID] = struct{}{}
				completed[p.FunctionResponse.Name]++
			}
		}
	}
	for id := range marked {
		if _, ok := answered[id]; !ok {
			open = append(open, OpenCall{ID: id, ToolName: callNames[id]})
		}
	}
	return open, completed, nil
}

// ListResumablePauses returns interrupted pauses that can still be
// answered: fresh (within ResumeMaxAge), addressed to a past ADK
// session, and holding at least one open long-running call. Stale
// records, unaddressed records, and history without open calls are
// skipped (the gate resolved, or its history was pruned). Nil
// registry (feature off) yields no pauses and no error.
func ListResumablePauses(ctx context.Context) ([]ResumablePause, error) {
	if deps == nil || deps.PauseRegistry == nil {
		return nil, nil
	}
	recs, err := deps.PauseRegistry.List()
	if err != nil {
		return nil, fmt.Errorf("durable_resume: list pauses: %w", err)
	}
	svc, err := adkServiceForResume()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-ResumeMaxAge())
	var out []ResumablePause
	for _, rec := range recs {
		if rec.ADKSessionID == "" {
			continue
		}
		if rec.Gate != hakasesession.PauseGateApproval && rec.Gate != hakasesession.PauseGateClarify {
			continue
		}
		if !rec.CreatedAt.IsZero() && rec.CreatedAt.Before(cutoff) {
			continue
		}
		open, _, serr := scanPausedSession(ctx, svc, rec.ADKSessionID)
		if serr != nil {
			return nil, serr
		}
		if len(open) == 0 {
			continue
		}
		out = append(out, ResumablePause{Record: rec, OpenCalls: open})
	}
	return out, nil
}

// findPauseRecord returns the pause record with pauseID, or an error
// when the registry is unavailable or the record is gone (answered,
// expired-pruned, or never recorded).
func findPauseRecord(pauseID string) (hakasesession.PauseRecord, error) {
	if deps == nil || deps.PauseRegistry == nil {
		return hakasesession.PauseRecord{}, fmt.Errorf("durable_resume: pause registry unavailable")
	}
	recs, err := deps.PauseRegistry.List()
	if err != nil {
		return hakasesession.PauseRecord{}, fmt.Errorf("durable_resume: list pauses: %w", err)
	}
	for _, rec := range recs {
		if rec.PauseID == pauseID {
			return rec, nil
		}
	}
	return hakasesession.PauseRecord{}, fmt.Errorf("durable_resume: pause %q not found (already answered?)", pauseID)
}

// checkPauseFresh rejects stale pauses: an answer days late must not
// resurrect a dead run. Callers unrecord on expiry so the record
// does not linger; the run itself stays dead either way.
func checkPauseFresh(rec hakasesession.PauseRecord) error {
	if !rec.CreatedAt.IsZero() && time.Since(rec.CreatedAt) > ResumeMaxAge() {
		unrecordGatePause(rec.PauseID)
		return fmt.Errorf("durable_resume: pause %q expired", rec.PauseID)
	}
	return nil
}

// assertResumable refuses a fresh-turn re-drive when the paused turn's
// history already holds completed never-replay side effects: the
// re-drive would risk executing them twice. Shared by the legacy
// (tool, command) and flow (session, tool, argsHash, labelVersion)
// resume paths - both re-drive a fresh turn, so both must gate here.
func assertResumable(ctx context.Context, rec hakasesession.PauseRecord) error {
	svc, err := adkServiceForResume()
	if err != nil {
		return err
	}
	_, completed, err := scanPausedSession(ctx, svc, rec.ADKSessionID)
	if err != nil {
		return err
	}
	var risky []string
	for name := range completed {
		if ReplayPolicyFor(name) == ReplayNever {
			risky = append(risky, name)
		}
	}
	if len(risky) > 0 {
		return fmt.Errorf("durable_resume: pause %q unsafe to re-drive: completed side-effecting calls %v would replay; start a new turn instead", rec.PauseID, risky)
	}
	return nil
}

// ResumeClarify answers an interrupted clarify pause after restart and
// resumes the paused turn. The answer is injected as a
// FunctionResponse per open call against the paused ADK session; the
// engine matches it to the open interrupt and continues WITHOUT
// re-executing any tool (settled history is reused). Returns the
// resumed turn's text output. The pause is unrecorded only once the
// resumed run completes; a run error keeps the record so the answer
// can be retried.
func ResumeClarify(ctx context.Context, r *runner.Runner, pauseID string, answer ClarifyResponse) (string, error) {
	if r == nil {
		return "", fmt.Errorf("durable_resume: nil runner")
	}
	rec, err := findPauseRecord(pauseID)
	if err != nil {
		return "", err
	}
	if rec.Gate != hakasesession.PauseGateClarify {
		return "", fmt.Errorf("durable_resume: pause %q is a %s gate, not clarify", pauseID, rec.Gate)
	}
	if err := checkPauseFresh(rec); err != nil {
		return "", err
	}
	if rec.ADKSessionID == "" {
		return "", fmt.Errorf("durable_resume: pause %q has no ADK session", pauseID)
	}
	svc, err := adkServiceForResume()
	if err != nil {
		return "", err
	}
	open, _, err := scanPausedSession(ctx, svc, rec.ADKSessionID)
	if err != nil {
		return "", err
	}
	if len(open) == 0 {
		return "", fmt.Errorf("durable_resume: pause %q has no open calls", pauseID)
	}
	for _, c := range open {
		if c.ToolName != "" && c.ToolName != "clarify" {
			return "", fmt.Errorf("durable_resume: pause %q open call %q is tool %q, not clarify", pauseID, c.ID, c.ToolName)
		}
	}
	payload := map[string]any{
		"question":      rec.Detail["question"],
		"user_response": answer.Answer,
		"canceled":      answer.Canceled,
		"timed_out":     answer.TimedOut,
	}
	fr := &genai.Content{
		Role:  genai.RoleUser,
		Parts: make([]*genai.Part, 0, len(open)),
	}
	for _, c := range open {
		name := c.ToolName
		if name == "" {
			name = "clarify"
		}
		fr.Parts = append(fr.Parts, &genai.Part{
			FunctionResponse: &genai.FunctionResponse{
				ID: c.ID, Name: name, Response: payload,
			},
		})
	}
	// Route gates raised by the resumed turn (nested clarify/approval)
	// back to the asking conversation, and record any new pauses
	// against the continued ADK session.
	interfaces.RegisterTaskSession(rec.ADKSessionID, rec.HakaseSessionID)
	defer interfaces.UnregisterTask(rec.ADKSessionID)
	var sb strings.Builder
	for ev, rerr := range r.Run(ctx, ResumeUserID, rec.ADKSessionID, fr, adkagent.RunConfig{}) {
		if rerr != nil {
			return "", fmt.Errorf("durable_resume: resumed run: %w", rerr)
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.Text != "" && !p.Thought {
				sb.WriteString(p.Text)
			}
		}
	}
	unrecordGatePause(rec.PauseID)
	return sb.String(), nil
}

// preGrants holds one-shot approval pre-grants installed by
// ResolveApprovalPause: the next ApproveExec auto-approves without
// blocking. One-shot (consumed on use) so a grant cannot leak into
// unrelated later turns. S10/M3: keyed by (session, hash), never raw
// command - the key carries no secret and survives pause-record
// redaction (the persisted command is redacted, the live command is not,
// but both hash the same unredacted preimage at mint time).
//
// S17 hardening: every grant is scoped to (sessionID, tool, argsHash)
// with a 15-minute TTL and a process-wide cap (1024, evict-oldest: a
// dropped grant fails closed to a re-ask, never an allow). Empty
// sessionID never grants (fail-closed). Expiry is lazy on consume plus
// an opportunistic sweep on insert (no goroutine).
const (
	preGrantTTL  = 15 * time.Minute
	maxPreGrants = 1024
)

// preGrantNow is the grant clock; a var so tests fake expiry without
// waiting (S17 hermetic). Production always uses time.Now.
var preGrantNow = time.Now

// preGrantEntry is one live one-shot grant with its mint deadline.
type preGrantEntry struct {
	expires time.Time
}

var preGrants = struct {
	sync.Mutex
	m     map[string]preGrantEntry
	order []string
}{m: make(map[string]preGrantEntry)}

// preGrantKey binds (session, tool, command-hash). NUL separators keep the
// legs domain-separated: no value forges another key, and a legacy key
// never matches a flow (session, tool, argsHash, labelVersion) binding.
func preGrantKey(tool, command, sessionID string) string {
	return sessionID + "\x00" + permissions.ArgsHash(tool, command)
}

// preGrantKeyByHash binds a precomputed hash in the same domain (the
// S10/M3 resume path mints from the unredacted grantHash carried in the
// pause detail, never from the redacted display value).
func preGrantKeyByHash(hash, sessionID string) string {
	return sessionID + "\x00" + hash
}

// sweepPreGrantsLocked drops expired entries. Callers hold preGrants.
func sweepPreGrantsLocked(now time.Time) {
	for k, e := range preGrants.m {
		if !now.Before(e.expires) {
			delete(preGrants.m, k)
		}
	}
	if len(preGrants.order) > len(preGrants.m)*2 {
		kept := preGrants.order[:0]
		for _, k := range preGrants.order {
			if _, ok := preGrants.m[k]; ok {
				kept = append(kept, k)
			}
		}
		preGrants.order = kept
	}
}

// installPreGrantLocked inserts k with a fresh TTL, evicting oldest-first
// past the cap (fail-closed direction: the evicted grant re-asks).
func installPreGrantLocked(k string, now time.Time) {
	sweepPreGrantsLocked(now)
	if _, ok := preGrants.m[k]; !ok {
		preGrants.order = append(preGrants.order, k)
	}
	preGrants.m[k] = preGrantEntry{expires: now.Add(preGrantTTL)}
	for len(preGrants.order) > maxPreGrants {
		oldest := preGrants.order[0]
		preGrants.order = preGrants.order[1:]
		delete(preGrants.m, oldest)
	}
}

// grantApproval installs a one-shot pre-grant. Empty tool/command/session
// never grants (fail-closed).
func grantApproval(tool, command, sessionID string) {
	if tool == "" || command == "" || sessionID == "" {
		return
	}
	preGrants.Lock()
	defer preGrants.Unlock()
	installPreGrantLocked(preGrantKey(tool, command, sessionID), preGrantNow())
}

// grantPreApprovalByHash installs a one-shot pre-grant by its hash
// (S10/M3 resume path: the hash was minted from the unredacted command
// at pause time and carried in the detail as grantHash). Empty hash or
// empty session never grants (fail-closed).
func grantPreApprovalByHash(h, sessionID string) {
	if h == "" || sessionID == "" {
		return
	}
	preGrants.Lock()
	defer preGrants.Unlock()
	installPreGrantLocked(preGrantKeyByHash(h, sessionID), preGrantNow())
}

// consumePreGrant reports and consumes a matching pre-grant. Wrong
// session, expired, or missing grants return false (the caller re-asks).
func consumePreGrant(tool, command, sessionID string) bool {
	if tool == "" || command == "" || sessionID == "" {
		return false
	}
	preGrants.Lock()
	defer preGrants.Unlock()
	k := preGrantKey(tool, command, sessionID)
	e, ok := preGrants.m[k]
	if !ok {
		return false
	}
	delete(preGrants.m, k)
	for i, q := range preGrants.order {
		if q == k {
			preGrants.order = append(preGrants.order[:i], preGrants.order[i+1:]...)
			break
		}
	}
	if !preGrantNow().Before(e.expires) {
		return false
	}
	return true
}

// flowDetailVersion reads a flow label version out of a pause detail map.
// Pause registries round-trip through JSON, so the uint64 may come back
// as a float64, int, json.Number or string; unparseable input fails
// closed to 0 only in the sense that the grant then cannot match a
// versioned binding (never silently widened - the re-drive re-checks).
func flowDetailVersion(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case uint:
		return uint64(n)
	case int:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case float64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case string:
		if u, err := strconv.ParseUint(strings.TrimSpace(n), 10, 64); err == nil {
			return u
		}
		return 0
	default:
		return 0
	}
}

// ResolveApprovalPause settles an interrupted approval pause after
// restart. Denial (approved=false) unrecords the pause and returns
// false: the command never runs. Approval (approved=true) installs a
// one-shot pre-grant scoped to the recorded tool+command, unrecords
// the pause, and returns true: the caller must re-drive a FRESH turn
// (normal RunTurn), during which the re-issued tool call passes its
// gate without blocking.
//
// Flow scope (S7, one-way door): a pause recorded for a flow ask
// (detail carries a flowReason) never mints a legacy (tool, command)
// pre-grant - legacy grants cannot satisfy a FlowReason ask (ApproveExec
// skips them), so minting one would silently map the old key onto the
// new flow key. Instead the exact flow binding (session, tool,
// argsHash, labelVersion) is installed as a one-shot flow grant, which
// the re-drive consumes like a human approval of that exact call. A
// legacy pause record without flow keys under an installed v2 policy
// fails closed with an explicit error (the operator denies it or starts
// a new turn): it cannot prove what flow check it evaluated.
//
// Approval resume refuses (error, record kept) when the paused
// turn's history already holds completed never-replay side effects:
// a fresh re-drive would risk executing them twice. The operator then
// starts a new turn by hand instead. The check (assertResumable)
// runs before both the flow and legacy grant paths: both re-drive a
// fresh turn, so a flow pause with settled side effects is equally
// unsafe to re-drive.
func ResolveApprovalPause(ctx context.Context, pauseID string, approved bool) (bool, error) {
	rec, err := findPauseRecord(pauseID)
	if err != nil {
		return false, err
	}
	if rec.Gate != hakasesession.PauseGateApproval {
		return false, fmt.Errorf("durable_resume: pause %q is a %s gate, not approval", pauseID, rec.Gate)
	}
	if err := checkPauseFresh(rec); err != nil {
		return false, err
	}
	if !approved {
		unrecordGatePause(rec.PauseID)
		return false, nil
	}
	if rec.ADKSessionID == "" {
		return false, fmt.Errorf("durable_resume: pause %q has no ADK session", pauseID)
	}
	tool, _ := rec.Detail["tool"].(string)
	command, _ := rec.Detail["command"].(string)
	if tool == "" || command == "" {
		return false, fmt.Errorf("durable_resume: pause %q record lacks tool/command; cannot pre-grant safely", pauseID)
	}
	// S7 flow scoping (one-way door: old records fail closed, never
	// silently mapped onto the new key).
	_, hasFlowKeys := rec.Detail["flowReason"]
	flowReason, _ := rec.Detail["flowReason"].(string)
	if !hasFlowKeys && permissions.FlowEnabled() {
		return false, fmt.Errorf("durable_resume: pause %q predates flow scoping and cannot pre-grant safely under a v2 policy; deny it or start a new turn instead", pauseID)
	}
	// H-1: gate both grant paths on the re-drive replay check (a flow
	// re-drive replays settled history the same way a legacy one does).
	if err := assertResumable(ctx, rec); err != nil {
		return false, err
	}
	if flowReason != "" {
		flowArgsHash, _ := rec.Detail["flowArgsHash"].(string)
		if flowArgsHash == "" {
			return false, fmt.Errorf("durable_resume: pause %q flow record lacks argsHash; cannot pre-grant safely", pauseID)
		}
		// S25: a resume approval is a fresh approval - it mints twin
		// v1+v2 records so the re-drive dual-consumes exactly once.
		b := flowAskBinding{
			sessionID:    rec.HakaseSessionID,
			tool:         tool,
			argsHash:     flowArgsHash,
			labelVersion: flowDetailVersion(rec.Detail["flowLabelVersion"]),
		}
		grantFlowCall(b)
		grantFlowCallV2(b)
		unrecordGatePause(rec.PauseID)
		return true, nil
	}
	// S10/M3: mint the pre-grant in the session scope from the
	// unredacted hash carried as grantHash (never from the redacted
	// command display value).
	if h, _ := rec.Detail["grantHash"].(string); h != "" {
		grantPreApprovalByHash(h, rec.HakaseSessionID)
	} else {
		grantApproval(tool, command, rec.HakaseSessionID)
	}
	unrecordGatePause(rec.PauseID)
	return true, nil
}

// FindResumablePause returns one resumable pause by record ID, or an
// error when it is missing, stale, or no longer holds open calls.
// Transports use it to validate an answered resurrected prompt before
// settling it.
func FindResumablePause(ctx context.Context, pauseID string) (ResumablePause, error) {
	recs, err := ListResumablePauses(ctx)
	if err != nil {
		return ResumablePause{}, err
	}
	for _, rec := range recs {
		if rec.Record.PauseID == pauseID {
			return rec, nil
		}
	}
	return ResumablePause{}, fmt.Errorf("durable_resume: pause %q not resumable (answered, expired, or settled)", pauseID)
}

// PausedTurnInput returns the paused turn's original user message: the
// first user-authored content in its ADK history. Approval resume
// re-drives a fresh turn with it (the answer pre-grants the gate, the
// model re-derives the tool calls).
func PausedTurnInput(ctx context.Context, adkSessionID string) (*genai.Content, error) {
	svc, err := adkServiceForResume()
	if err != nil {
		return nil, err
	}
	resp, gerr := svc.Get(ctx, &adksession.GetRequest{
		AppName: ResumeAppName, UserID: ResumeUserID, SessionID: adkSessionID,
	})
	if gerr != nil {
		return nil, fmt.Errorf("durable_resume: read paused session: %w", gerr)
	}
	for ev := range resp.Session.Events().All() {
		if ev == nil || ev.Content == nil || ev.Author != "user" {
			continue
		}
		// Skip injected answers (FunctionResponse-only events from a
		// previous resume): the turn input is user-authored content.
		hasInput := false
		for _, p := range ev.Content.Parts {
			if p != nil && p.FunctionResponse == nil {
				hasInput = true
				break
			}
		}
		if hasInput {
			return ev.Content, nil
		}
	}
	return nil, fmt.Errorf("durable_resume: paused session %q holds no user input", adkSessionID)
}
