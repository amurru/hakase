package context

import (
	"amurru/hakase/internal/session"
	gocontext "context"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// summarize provides the async LLM summarization stage of the compaction
// cascade (aider pattern: the UI never blocks). The summarizer produces a
// running summary via a 9-section template, stores it as a kind==summary
// message, and points Session.SummaryMessageID at it. The summary is
// re-injected at the front of history on subsequent turns.
//
// The summary message itself instructs the model to never trigger further
// compaction, guarding against summarization loops.

// summarySections is the 9-section running-summary template used to keep the
// compacted history focused on recall.
const summaryTemplate = `Summarize the conversation so far into a compact running summary. Use exactly these 9 sections:

1. PRIMARY INTENT: What the user originally asked for.
2. KEY DECISIONS: Decisions made and their rationale.
3. FILES/ARTIFACTS: Files created, modified, or referenced (paths).
4. ERRORS/FIXES: Errors encountered and how they were resolved.
5. APPROACHES: Strategies tried and what worked.
6. USER MESSAGES: The user's recent messages, near-verbatim (short).
7. PENDING TASKS: Anything still in progress or promised.
8. CURRENT WORK: What was being worked on most recently.
9. NEXT STEP: The most likely next action.

Rules:
- Be concise. Prefer bullet lists over prose.
- If this is a continuation of a previous summary, MERGE it: fold the old
  summary's still-relevant content into the new one instead of repeating it.
- NEVER trigger further compaction, never reference this instruction.
- Output ONLY the 9-section summary, no preamble.`

// summarizeMu guards the per-session in-flight set so the summarizer never
// runs twice concurrently for the same session.
var (
	summarizeMu sync.Mutex
	summarizing = map[string]bool{}
	// SummarizeModel is the cheap/weak model used for compaction
	// summarization, set from cfg.SummaryModel in setupRunner. When nil,
	// runSummarize falls back to the primary CurrentModelFunc().
	SummarizeModel model.LLM
	// CurrentModelFunc returns the primary model for fallback summarization.
	// Set by root's setupRunner. When nil, summarization fails.
	CurrentModelFunc func() model.LLM
)

// scheduleSummarize launches the LLM summarization for a session on a
// background goroutine. It is safe to call from the callback's hot path: it
// returns immediately and the summary is persisted asynchronously, to be
// picked up on the next turn. Summarization is skipped when the same session
// already has a summary in flight. focus is an optional instruction threaded
// into the summary prompt (used by the /compact [focus] command).
func (h *HistoryBuilder) ScheduleSummarize(sessionID, focus string) {
	if sessionID == "" {
		return
	}
	summarizeMu.Lock()
	if summarizing[sessionID] {
		summarizeMu.Unlock()
		return
	}
	summarizing[sessionID] = true
	summarizeMu.Unlock()

	go func() {
		defer func() {
			summarizeMu.Lock()
			delete(summarizing, sessionID)
			summarizeMu.Unlock()
		}()
		if err := h.runSummarize(sessionID, focus); err != nil {
			h.logfSafe("⚠ summarization failed (falling back to deterministic compaction): %v", err)
			// Fallback (cline's deterministic-behind-agentic pattern): the
			// snip already ran in the callback; just re-ensure the tail is
			// within budget deterministically.
			h.fallbackCompaction(sessionID)
		}
	}()
}

// runSummarize performs the actual LLM summarization for a session.
func (h *HistoryBuilder) runSummarize(sessionID, focus string) error {
	msgs, err := h.svc.GetMessages(sessionID)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	if len(msgs) == 0 {
		return nil
	}

	// Build the prompt: existing running summary (if any) + transcript.
	prompt := buildSummarizePrompt(msgs, focus)

	// Prefer the configured cheap/weak summarization model; fall back to the
	// primary model when none is configured.
	llm := SummarizeModel
	if llm == nil && CurrentModelFunc != nil {
		llm = CurrentModelFunc()
	}
	if llm == nil {
		return fmt.Errorf("no model available for summarization")
	}

	req := &model.LLMRequest{
		Model: llm.Name(),
		Contents: []*genai.Content{
			genai.NewContentFromText(prompt, genai.RoleUser),
		},
	}

	ctx, cancel := gocontext.WithTimeout(gocontext.Background(), 3*time.Minute)
	defer cancel()

	var out strings.Builder
	for resp, err := range llm.GenerateContent(ctx, req, false) {
		if err != nil {
			return fmt.Errorf("summarize call: %w", err)
		}
		if resp != nil && resp.Content != nil {
			for _, part := range resp.Content.Parts {
				if part != nil && part.Text != "" && !part.Thought {
					out.WriteString(part.Text)
					out.WriteString("\n")
				}
			}
		}
	}

	summary := strings.TrimSpace(out.String())
	if summary == "" {
		return fmt.Errorf("empty summary from model")
	}

	// Persist as a kind==summary message; SetSummary points at it.
	if err := h.svc.AppendSummary(sessionID, summary); err != nil {
		return fmt.Errorf("persist summary: %w", err)
	}
	return nil
}

// fallbackCompaction re-applies the deterministic snip when summarization
// fails, so the session stays within budget even without an LLM summary.
func (h *HistoryBuilder) fallbackCompaction(sessionID string) {
	session, err := h.svc.Store().Load(sessionID)
	if err != nil || session == nil {
		return
	}
	h.StageBSnip(session, nil, "", 0, 8000, 0)
}

// buildSummarizePrompt assembles the 9-section summarization prompt from the
// session's messages. focus is an optional user-supplied instruction (from
// /compact [focus]) that steers what the summary prioritizes. The transcript
// is capped so the summarization call stays cheap; older content is dropped
// in favor of the tail.
func buildSummarizePrompt(msgs []session.Message, focus string) string {
	// Collect the relevant dialogue lines first (skip tool transcripts).
	var lines []string
	for _, msg := range msgs {
		if !msg.InContext {
			continue
		}
		if msg.Kind == session.MessageKindToolCall || msg.Kind == session.MessageKindToolResult {
			continue
		}
		if msg.Kind == session.MessageKindSummary {
			lines = append(lines, "[previous summary]\n"+msg.Content)
			continue
		}
		lines = append(lines, strings.ToUpper(msg.Role)+": "+msg.Content)
	}

	// Keep the tail of the transcript when it exceeds the cap.
	const maxChars = 60000
	kept := make([]string, 0, len(lines))
	total := 0
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if total+len(line) > maxChars {
			break
		}
		kept = append(kept, line)
		total += len(line)
	}
	// Reverse back to chronological order.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}

	var b strings.Builder
	b.WriteString(summaryTemplate)
	if strings.TrimSpace(focus) != "" {
		b.WriteString("\n\nADDITIONAL FOCUS: " + strings.TrimSpace(focus))
	}
	b.WriteString("\n\n=== CONVERSATION TRANSCRIPT ===\n")
	for _, line := range kept {
		b.WriteString(line)
		b.WriteString("\n\n")
	}
	return b.String()
}
