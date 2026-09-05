package context

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"amurru/hakase/internal/env"
	"amurru/hakase/internal/interfaces"
	sesspkg "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// HistoryBuilder prepends persisted conversation history to the root
// orchestrator's model requests via a BeforeModelCallback, and keeps that
// history within a token budget using a cheapest-first compaction cascade
// (tool-output trim -> deterministic snip -> async LLM summarization).
//
// The builder is created once in setupRunner with the sesspkg.SessionService and
// receives ModelInfo updates asynchronously once the provider fetch lands.
type HistoryBuilder struct {
	svc         *sesspkg.SessionService
	modelInfo   *interfaces.ModelInfo // guarded by modelInfoMu
	modelInfoMu sync.RWMutex
	logf        func(format string, args ...any)
	pending     *util.PendingQueue      // mid-run steering queue (may be nil)
	sidekick    *util.SidekickNoteQueue // sidekick advisory notes (may be nil)

	// Token estimates of the rendered project-context block and the git
	// workspace snapshot that are folded into the system prompt
	// (setupRunner). fitToBudget reserves them so large AGENTS.md files or
	// repo snapshots do not silently blow the token budget. They live on the
	// builder rather than in process-global state so concurrent runs keep
	// their own accounting instead of overwriting each other's reserve.
	contextBlockTokens      int
	gitWorkspaceBlockTokens int
}

// NewHistoryBuilder creates a HistoryBuilder bound to the given session
// service. svc may be nil (history injection is a no-op in that case).
func NewHistoryBuilder(svc *sesspkg.SessionService) *HistoryBuilder {
	return &HistoryBuilder{svc: svc}
}

// SetBlockTokenEstimates records the token estimates of the rendered
// project-context block and git workspace snapshot folded into the system
// prompt. Called once in setupRunner with the values rendered for this run.
func (h *HistoryBuilder) SetBlockTokenEstimates(contextBlockTokens, gitWorkspaceBlockTokens int) {
	h.contextBlockTokens = contextBlockTokens
	h.gitWorkspaceBlockTokens = gitWorkspaceBlockTokens
}

// SetPendingQueue attaches the TUI's mid-run message queue so queued prompts
// are steered into the request on every model call. May be nil (no steering).
func (h *HistoryBuilder) SetPendingQueue(q *util.PendingQueue) {
	h.pending = q
}

// SetSidekickQueue attaches the sidekick's advisory-note queue so notes
// produced by the watchdog are injected into the next model call. May be nil
// (no sidekick).
func (h *HistoryBuilder) SetSidekickQueue(q *util.SidekickNoteQueue) {
	h.sidekick = q
}

// SetModelInfo updates the model capabilities used for budget decisions.
// Called from the async ModelInfoMsg fetch once provider data lands.
func (h *HistoryBuilder) SetModelInfo(info *interfaces.ModelInfo) {
	h.modelInfoMu.Lock()
	defer h.modelInfoMu.Unlock()
	h.modelInfo = info
}

// SetLogFunc installs a logger (usually the TUI log pane) for compaction
// status messages.
func (h *HistoryBuilder) SetLogFunc(f func(format string, args ...any)) {
	h.logf = f
}

func (h *HistoryBuilder) logfSafe(format string, args ...any) {
	if h.logf != nil {
		h.logf(format, args...)
	}
}

// BeforeModelCallback prepends persisted conversation history to the request
// before the real model call. Returning (nil, nil) lets the call proceed.
//
// The callback fires once per LLM call; within one r.Run the tool loop makes
// several calls and ADK rebuilds req.Contents from session events each time
// (those events never contain our file-backed history), so we prepend on
// every call rather than tracking InvocationID. Each call receives a fresh
// request built from the ADK session, so there is no duplication risk.
func (h *HistoryBuilder) BeforeModelCallback(ctx agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	if h == nil || h.svc == nil || req == nil {
		return nil, nil
	}

	// Load the active persisted session (the one the UI is writing to).
	session, err := h.svc.GetActiveSession()
	if err != nil || session == nil || len(session.Messages) == 0 {
		return nil, nil
	}

	currentText := ""
	if uc := ctx.UserContent(); uc != nil {
		currentText = ContentText(uc)
	}

	// Build history: summaries first, then in-context transcript, skipping
	// the current user message (it is already persisted and in req.Contents).
	history := h.buildHistory(session.Messages, currentText)
	if len(history) == 0 {
		return nil, nil
	}

	// Fit history into the budget (cascade stages a+b when over trigger).
	h.modelInfoMu.RLock()
	info := h.modelInfo
	h.modelInfoMu.RUnlock()
	history = h.fitToBudget(session, history, req.Contents, info)

	// Prepend, do not replace: ADK manages the current run's contents
	// (user message + tool calls + results).
	current := req.Contents
	req.Contents = append(history, current...)

	// Live reconcile: if a project context file (AGENTS.md) changed
	// mid-session, inject a one-shot update notice at the head of the current
	// run's contents so the model follows the updated instructions. The
	// notice is not persisted; the model's next response reflects the change.
	if notice := ContextUpdateNotice(); notice != "" {
		h.logfSafe("⚠ project context files changed; update notice injected")
		noticeContent := genai.NewContentFromText(notice, genai.RoleUser)
		req.Contents = append(append(append([]*genai.Content{}, history...), noticeContent), current...)
	}

	// Environment staleness: if disk free or available memory drifted
	// materially since the startup snapshot, inject a one-shot notice so the
	// model re-checks live state instead of trusting stale values. Follows
	// the same pattern as the context update notice above.
	if notice := env.EnvStalenessNotice(); notice != "" {
		h.logfSafe("⚠ system state changed; environment update notice injected")
		noticeContent := genai.NewContentFromText(notice, genai.RoleUser)
		req.Contents = append(append(append([]*genai.Content{}, history...), noticeContent), current...)
	}

	// Steer queued user messages (typed while the agent was busy) into the
	// tail of the request as the most recent user intent. Injected on every
	// model call: ADK rebuilds req.Contents fresh per call from session
	// events (which never contain the queue), so re-injecting keeps the
	// steering active for the whole run. The queue drains at agentDoneMsg.
	if h.pending != nil && h.pending.Len() > 0 {
		for _, q := range h.pending.Snapshot() {
			req.Contents = append(req.Contents, util.SteeringContent(q))
		}
	}

	// Inject sidekick advisory notes (watchdog) as a user-role reminder so the
	// model reviews them before responding. Notes are also persisted to the
	// session (InContext) so they remain visible across turns (NotesInContext).
	if h.sidekick != nil && h.sidekick.Len() > 0 && session != nil {
		var sb strings.Builder
		sb.WriteString("SIDEKICK ADVISORY NOTES — review these before responding:")
		for _, n := range h.sidekick.Pending() {
			sb.WriteString(fmt.Sprintf("\n- [%s] %s", n.Severity, n.Text))
			session.AddMessageWithMeta("sidekick", n.Text, "", 0, sesspkg.MessageKindSidekick)
		}
		req.Contents = append(req.Contents, genai.NewContentFromText(sb.String(), genai.RoleUser))
		// Persist sidekick notes so they survive a crash before the next
		// natural save point.
		if h.svc != nil {
			_ = h.svc.SaveSession(session)
		}
	}
	return nil, nil
}

// buildHistory converts persisted messages to genai contents: summaries
// first, then the in-context transcript. The current user message (deduped
// against ctx.UserContent) is skipped since ADK already carries it.
func (h *HistoryBuilder) buildHistory(msgs []sesspkg.Message, currentText string) []*genai.Content {
	var summaries, history []*genai.Content
	for _, msg := range msgs {
		if !msg.InContext {
			continue
		}
		if msg.Kind == sesspkg.MessageKindSummary {
			summaries = append(summaries, MessageToContent(msg))
			continue
		}
		// Dedup: skip the current user message (persisted at send time).
		// Attachment-bearing messages persist only the bare prompt while the
		// in-flight request also carries the attachment parts, so compare by
		// prefix rather than plain equality.
		if msg.Role == "user" && CurrentUserMessageMatches(msg, currentText) {
			continue
		}
		history = append(history, MessageToContent(msg))
	}
	return append(summaries, history...)
}

// currentUserMessageMatches reports whether the in-flight request's user
// content corresponds to the given persisted user message (dedup guard for
// buildHistory).
func CurrentUserMessageMatches(msg sesspkg.Message, currentText string) bool {
	if currentText == msg.Content {
		return true
	}
	// Attachment-bearing messages persist the bare prompt; the request text
	// is the prompt followed by any attachment parts (text files), so a
	// prefix match means the same message.
	if len(msg.Attachments) > 0 && currentText != "" && strings.HasPrefix(currentText, msg.Content) {
		return true
	}
	// Image-only messages persist no text and the request carries only the
	// image part (no text either).
	if len(msg.Attachments) > 0 && currentText == "" && msg.Content == "" {
		return true
	}
	return false
}

// MessageToContent converts a persisted message into a genai.Content with
// the appropriate role. Agent messages map to the model role. Messages with
// attachments rebuild their parts: text verbatim, text files as text parts,
// images as inline data parts (content re-read from Path).
//
// All text content (message body and text-file attachment contents) is framed
// in <UNTRUSTED_DATA> tags via WrapUntrustedData to clearly delimit
// externally-sourced data when it is re-injected into the model context.
func MessageToContent(msg sesspkg.Message) *genai.Content {
	role := genai.Role(msg.Role)
	if msg.Role == "agent" {
		role = genai.RoleModel
	}
	if msg.Role == "sidekick" {
		// Advisory notes are authored content from a second LLM; surface them
		// to the model as user-role context so they are not mistaken for the
		// primary assistant's own output.
		role = genai.RoleUser
	}
	if len(msg.Attachments) == 0 {
		return genai.NewContentFromText(WrapUntrustedData(msg.Content), role)
	}
	var parts []*genai.Part
	if strings.TrimSpace(msg.Content) != "" {
		parts = append(parts, genai.NewPartFromText(WrapUntrustedData(msg.Content)))
	}
	for _, att := range msg.Attachments {
		if att.Path == "" {
			continue
		}
		data, err := os.ReadFile(att.Path)
		if err != nil {
			continue
		}
		if util.IsImageMIME(att.MIME) {
			parts = append(parts, genai.NewPartFromBytes(data, att.MIME))
		} else if !utf8.Valid(data) {
			// The file on disk is not text (deleted/replaced/binary all
			// along): summarize instead of injecting mangled bytes.
			parts = append(parts, genai.NewPartFromText(WrapUntrustedData(
				fmt.Sprintf("[binary file: %s, %d bytes - not readable as text]", att.MIME, len(data)),
			)))
		} else {
			parts = append(parts, genai.NewPartFromText(WrapUntrustedData(string(data))))
		}
	}
	return genai.NewContentFromParts(parts, role)
}

// ContentText extracts the concatenated text of a genai.Content for dedup
// comparison against persisted user messages.
func ContentText(c *genai.Content) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	for _, part := range c.Parts {
		if part != nil && part.Text != "" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// fitToBudget applies the compaction cascade when history + current contents
// exceed the trigger threshold (0.9 * effective max input). It returns the
// trimmed history slice; stage c (LLM summarization) is scheduled async and
// never runs in the callback's hot path.
func (h *HistoryBuilder) fitToBudget(session *sesspkg.Session, history []*genai.Content, current []*genai.Content, info *interfaces.ModelInfo) []*genai.Content {
	// No model info yet (async fetch not complete or failed): don't truncate,
	// but log the near-limit condition only when the session history is
	// suspiciously large. This is a legitimate state (e.g. web mode before
	// the model-info fetch lands), not an error.
	if info == nil {
		return history
	}
	effectiveMax := util.MaxInputTokens(&util.ModelBudget{ContextWindow: info.ContextWindow, MaxInputTokens: info.MaxInputTokens})
	if effectiveMax <= 0 {
		// No usable budget; don't truncate, but log the near-limit condition
		// only when the session history is suspiciously large.
		return history
	}

	// Reserve budget for the system prompt + tool schemas + current run
	// contents, plus the rendered project-context block and the git workspace
	// snapshot (estimates recorded on this builder in setupRunner from the
	// discovered AGENTS.md files and repo snapshot), and the
	// runtime-environment block (env.SystemEnvBlockTokens, set in setupRunner).
	// The flat baseline plus the blocks is a conservative approximation since
	// we cannot see the fully rendered prompt.
	const baseReserveTokens = 8000
	reserveTokens := baseReserveTokens + h.contextBlockTokens + h.gitWorkspaceBlockTokens + env.SystemEnvBlockTokens

	currentTokens := util.EstimateContentsTokens(current)
	trigger := int64(effectiveMax * 9 / 10)
	target := int64(effectiveMax * 7 / 10)
	historyTokens := int64(util.EstimateContentsTokens(history))

	if currentTokens+int(historyTokens)+reserveTokens <= int(trigger) {
		return history
	}

	h.logfSafe("⚠ context compaction scheduled: history %d + current %d tokens, budget %d", historyTokens, currentTokens, effectiveMax)

	// Stage (a): trim tool-output transcripts. Tool results are the cheapest
	// to evict (they were already used to produce the agent's answer).
	history = h.stageATrimToolResults(session, history, currentTextOf(current))

	// Stage (b): deterministic snip. Archive oldest messages until the tail
	// fits the target; keep the recent ~20k tokens + last 2 turns verbatim.
	if currentTokens+util.EstimateContentsTokens(history)+reserveTokens > int(target) {
		history = h.StageBSnip(session, history, currentTextOf(current), currentTokens, reserveTokens, target)
	}

	// Stage (c): async summarization, only if still over target. The summary
	// is picked up on the next turn; UI never blocks.
	if currentTokens+util.EstimateContentsTokens(history)+reserveTokens > int(target) {
		h.ScheduleSummarize(session.ID, "")
	}

	return history
}

// currentTextOf returns the concatenated text of the current contents for
// re-dedup after trimming (the callback may fire multiple times per turn).
func currentTextOf(contents []*genai.Content) string {
	if len(contents) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range contents {
		b.WriteString(ContentText(c))
	}
	return b.String()
}

// stageATrimToolResults marks tool_result messages out-of-context so they no
// longer enter the request, keeping the tool_use record. Returns the history
// rebuilt from the surviving messages.
func (h *HistoryBuilder) stageATrimToolResults(session *sesspkg.Session, history []*genai.Content, currentText string) []*genai.Content {
	changed := false
	for i := range session.Messages {
		msg := &session.Messages[i]
		if msg.InContext && msg.Kind == sesspkg.MessageKindToolResult {
			msg.InContext = false
			msg.Content = "[tool result trimmed]"
			changed = true
		}
	}
	if changed {
		h.persistSnapshot(session)
		return h.buildHistory(session.Messages, currentText)
	}
	return history
}

// stageBSnip archives oldest in-context messages (flips InContext=false,
// keeps them on disk per the aichat pattern) until the remaining tail fits
// within the target budget. The last 2 turns are always kept verbatim.
// Oldest-first eviction naturally preserves ~20k recent tokens whenever the
// budget allows; on tight budgets the budget wins over the recency heuristic.
// currentText is re-applied during history rebuild so the current user
// message stays deduped (it is already in the run's contents).
func (h *HistoryBuilder) StageBSnip(session *sesspkg.Session, history []*genai.Content, currentText string, currentTokens, reserveTokens int, target int64) []*genai.Content {
	// Hard keep region: the last 2 user turns (never evicted).
	last2TurnStart := -1
	userCount := 0
	for i := len(session.Messages) - 1; i >= 0; i-- {
		if session.Messages[i].Role == "user" && session.Messages[i].InContext {
			userCount++
			if userCount == 2 {
				last2TurnStart = i
				break
			}
		}
	}

	budget := int(target) - currentTokens - reserveTokens
	if budget < 0 {
		budget = 0
	}

	// Evict oldest in-context messages until the surviving history fits the
	// budget, never crossing into the protected last-2-turns region.
	changed := false
	for i := 0; i < len(session.Messages); i++ {
		if last2TurnStart != -1 && i >= last2TurnStart {
			break
		}
		if !session.Messages[i].InContext {
			continue
		}
		session.Messages[i].InContext = false
		changed = true
		if int64(util.EstimateContentsTokens(h.buildHistory(session.Messages, currentText))) <= int64(budget) {
			break
		}
	}

	if changed {
		h.persistSnapshot(session)
	}
	return h.buildHistory(session.Messages, currentText)
}

// persistSnapshot saves the session's current message state to disk (the
// InContext flips from the compaction cascade). Hint dedup state is synced
// onto the session so a resumed session does not re-attach hints.
func (h *HistoryBuilder) persistSnapshot(session *sesspkg.Session) {
	if h.svc == nil || session == nil {
		return
	}
	if sesspkg.BuildHintedPathsHook != nil {
		session.HintedContextFiles = sesspkg.BuildHintedPathsHook()
	}
	_ = h.svc.SaveSession(session)
}
