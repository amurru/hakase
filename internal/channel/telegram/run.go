package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/channel"
	"amurru/hakase/internal/channel/state"
	hakasesession "amurru/hakase/internal/session"

	"google.golang.org/genai"
)

// streamEditInterval spaces live answer-message edits (at most one edit per
// interval, only when content changed). A var so tests can shorten it.
var streamEditInterval = 2 * time.Second

// statusEditInterval spaces quiet status-line edits while nothing has
// streamed yet. A var so tests can shorten it.
var statusEditInterval = 2500 * time.Millisecond

// streamFlushLen is the raw-answer length at which a streaming message is
// finalized and a continuation message starts (the design's "~3,800 chars",
// kept at ChunkReply's chunkLimit so every final render has the same headroom
// below Telegram's 4096 limit after HTML conversion).
const streamFlushLen = 3500

// maxStatusErrLen bounds the error summary shown on a failed run's status line.
const maxStatusErrLen = 200

// startRun is the inbound-prompt entry point: it resolves the conversation's
// session, persists the user turn, and launches the shared driver with a
// Telegram run view. parts/refs/manifest carry photos (genai inline data,
// session attachment refs, and the manifest lines appended to the prompt).
// promptID is the user's prompt message (reaction receipts and the turn pin).
func (b *Bot) startRun(ctx context.Context, c conv, promptID int, prompt string, photoParts []*genai.Part, refs []hakasesession.AttachmentRef, manifest []string) {
	rk := threadKey(c)
	if _, running := b.runs.Running(rk); running {
		b.sendText(ctx, c, "⏳ A run is already active here — send /stop to cancel it first.", nil, false)
		return
	}
	if b.driver == nil || b.sessions == nil {
		b.sendText(ctx, c, "⚠️ Agent runtime unavailable (channel not wired to a runner).", nil, false)
		return
	}

	sessionID, err := b.resolveSession(chatKey(c.chatID), prompt)
	if err != nil {
		b.sendText(ctx, c, "⚠️ Could not resolve a session: "+err.Error(), nil, false)
		return
	}

	// Persist the user turn (same contract as the web handler): make the
	// session active, then record with attachment refs for history rebuilds.
	fullPrompt := prompt
	if len(manifest) > 0 {
		fullPrompt = strings.TrimSpace(fullPrompt + "\n[attachments]\n" + strings.Join(manifest, "\n"))
	}
	if err := b.sessions.SetActiveSession(sessionID); err != nil {
		b.sendText(ctx, c, "⚠️ Session unavailable: "+err.Error(), nil, false)
		return
	}
	if err := b.sessions.RecordUsageWithAttachments("user", fullPrompt, "", 0, refs); err != nil {
		b.log("failed to save user message: %v", err)
	}

	parts := make([]*genai.Part, 0, len(photoParts)+1)
	if fullPrompt != "" {
		parts = append(parts, genai.NewPartFromText(fullPrompt))
	}
	parts = append(parts, photoParts...)
	if len(parts) == 0 {
		return
	}
	content := genai.NewContentFromParts(parts, genai.RoleUser)

	runCtx, cancel := context.WithCancel(context.Background())
	if !b.runs.TryStart(rk, sessionID, cancel) {
		cancel()
		b.sendText(ctx, c, "⏳ A run is already active here — send /stop to cancel it first.", nil, false)
		return
	}

	rv := newRunView(b, c, promptID, runCtx)
	go func() {
		defer b.runs.Finish(rk)
		rv.begin() // 👀 receipt and the optional turn pin
		stop := make(chan struct{})
		go rv.pumpLoop(stop)
		b.driver.RunTurn(runCtx, sessionID, content, rv)
		close(stop)
		rv.finalize()
	}()
}

// resolveSession returns the chat's bound session, validating it still
// exists; otherwise it creates (and binds) a fresh one titled from the prompt.
func (b *Bot) resolveSession(chatKeyStr, prompt string) (string, error) {
	st := b.store.Get()
	if chat, ok := st.Chats[chatKeyStr]; ok && chat.SessionID != "" {
		if _, err := b.sessions.Store().Load(chat.SessionID); err == nil {
			return chat.SessionID, nil
		}
		// Bound session vanished (deleted/archived elsewhere): fall through
		// and create a replacement.
	}
	title := "Telegram · " + firstRunes(prompt, 40)
	if strings.TrimSpace(prompt) == "" {
		title = "Telegram · " + time.Now().Format("Jan 2 15:04")
	}
	sess, err := b.sessions.CreateSession(title)
	if err != nil {
		return "", err
	}
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		s.Chats[chatKeyStr] = state.Chat{SessionID: sess.ID}
		return nil
	}); err != nil {
		b.log("cannot persist chat binding: %v", err)
	}
	return sess.ID, nil
}

func firstRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// runView carries the live-rendering state of one run: a quiet status line
// edited in place until the answer starts, and the answer streamed into
// progressively edited messages (first creation is the turn's one
// notification; everything else is silent). It doubles as the
// agentrun.EventSink for the turn.
type runView struct {
	b        *Bot
	c        conv
	promptID int // the user's prompt message (receipts + pin)
	started  time.Time
	ctx      context.Context

	// renderMu serializes every Telegram render (pump ticks vs finalize) so a
	// late tick can never overwrite the final render.
	renderMu sync.Mutex

	mu              sync.Mutex
	statusID        int    // silent status-line message (0 = not created yet)
	lastTool        string // most recent tool call name
	tokens          int
	lastError       string // first error line seen, truncated
	hasError        bool
	answerID        int    // current streaming answer message (0 = none yet)
	answerContinued bool   // an overflow continuation happened (creations after the first are silent)
	seg             []rune // raw markdown of the current answer message
	inFence         bool   // code-fence state carried across edits and messages
	streamedAny     bool   // at least one non-blank content delta arrived
	dirty           bool   // seg changed since the last edit
	finished        bool   // finalize has run; pump must not edit anymore
}

func newRunView(b *Bot, c conv, promptID int, ctx context.Context) *runView {
	return &runView{b: b, c: c, promptID: promptID, started: time.Now(), ctx: ctx}
}

// begin marks the turn start: 👀 receipt on the prompt and the optional
// silent pin (Hermes-style turn marker).
func (rv *runView) begin() {
	rv.b.react(rv.ctx, rv.c, rv.promptID, reactionLooking)
	if rv.b.pins {
		rv.b.pinMessage(rv.ctx, rv.c, rv.promptID)
	}
}

// pumpLoop keeps the answer message (and, before streaming starts, the status
// line) refreshed until stop is closed or the run ctx is cancelled.
func (rv *runView) pumpLoop(stop <-chan struct{}) {
	rv.pump() // post the status line immediately
	ticker := time.NewTicker(streamEditInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-rv.ctx.Done():
			return
		case <-ticker.C:
			rv.pump()
		}
	}
}

// pump performs one render pass: status-line edits until the answer starts,
// then throttled answer edits with overflow continuation.
func (rv *runView) pump() {
	rv.renderMu.Lock()
	defer rv.renderMu.Unlock()

	rv.mu.Lock()
	if rv.finished {
		rv.mu.Unlock()
		return
	}
	if rv.dirty {
		rv.dirty = false
	}
	streaming := rv.streamedAny
	answerID := rv.answerID
	statusID := rv.statusID
	lastTool := rv.lastTool
	seg := make([]rune, len(rv.seg))
	copy(seg, rv.seg)
	rv.mu.Unlock()

	// The status line exists before anything streams; creation is silent.
	if statusID == 0 {
		if msg := rv.b.sendText(rv.ctx, rv.c, statusWorking(lastTool, time.Since(rv.started)), nil, true); msg != nil {
			rv.mu.Lock()
			rv.statusID = msg.ID
			rv.mu.Unlock()
		}
		return
	}
	if !streaming {
		rv.b.editText(rv.ctx, rv.c, statusID, statusWorking(lastTool, time.Since(rv.started)), nil)
		return
	}

	// Overflow: when the segment exceeds the flush length, the part before
	// the cut finalizes the current message and the remainder continues in a
	// fresh one.
	rest, done, overflowed := splitOverflow(seg)

	if answerID == 0 {
		// First content arrived: create the answer message (the turn's one
		// notification; continuation messages are created silently).
		body := seg
		if overflowed {
			body = done
		}
		rv.mu.Lock()
		silent := rv.answerContinued
		rv.mu.Unlock()
		msg := rv.b.sendText(rv.ctx, rv.c, rv.renderSegment(body), nil, silent)
		if msg == nil {
			return // creation failed; the next tick (or finalize) retries
		}
		rv.mu.Lock()
		rv.answerID = msg.ID
		if overflowed {
			rv.seg = append([]rune(nil), rest...)
			rv.answerID = 0 // the remainder continues in a new message
			rv.answerContinued = true
		}
		rv.mu.Unlock()
		return
	}

	if overflowed {
		rv.b.editText(rv.ctx, rv.c, answerID, rv.renderSegment(done), nil)
		rv.mu.Lock()
		rv.seg = append([]rune(nil), rest...)
		rv.answerID = 0
		rv.answerContinued = true
		rv.mu.Unlock()
		return
	}
	if len(seg) > 0 {
		rv.b.editText(rv.ctx, rv.c, answerID, rv.renderSegment(seg), nil)
	}
}

// splitOverflow, when seg exceeds the flush length, cuts it at the streaming
// boundary (preferring a line boundary, like channel.ChunkText): done is the
// part that finalizes the current message, rest continues in a new one.
func splitOverflow(seg []rune) (rest, done []rune, ok bool) {
	if len(seg) <= streamFlushLen {
		return nil, nil, false
	}
	cut := streamFlushLen
	if idx := strings.LastIndex(string(seg[:streamFlushLen]), "\n"); idx > streamFlushLen/2 {
		cut = idx
	}
	done = []rune(strings.TrimRight(string(seg[:cut]), "\n"))
	rest = []rune(strings.TrimSpace(string(seg[cut:])))
	if len(done) == 0 || len(rest) == 0 {
		return nil, nil, false
	}
	return rest, done, true
}

// renderSegment converts the raw markdown segment to Telegram HTML, carrying
// the code-fence state across edits and continuation messages (the same
// mechanism ChunkReply uses, so split answers render balanced tags).
func (rv *runView) renderSegment(seg []rune) string {
	rv.mu.Lock()
	fence := rv.inFence
	rv.mu.Unlock()
	html := channel.MarkdownToTelegramHTMLState(string(seg), &fence)
	rv.mu.Lock()
	rv.inFence = fence
	rv.mu.Unlock()
	return html
}

// finalize stops the pump and renders the terminal states: the answer's final
// render, the compact status line, the ✅/❌ receipt, and the unpin.
func (rv *runView) finalize() {
	rv.renderMu.Lock()
	defer rv.renderMu.Unlock()

	rv.mu.Lock()
	rv.finished = true
	answerID := rv.answerID
	statusID := rv.statusID
	seg := make([]rune, len(rv.seg))
	copy(seg, rv.seg)
	hasText := rv.streamedAny && strings.TrimSpace(string(seg)) != ""
	tokens := rv.tokens
	lastError := rv.lastError
	hasError := rv.hasError
	rv.mu.Unlock()

	ctx := context.Background()
	elapsed := time.Since(rv.started).Round(time.Second)

	switch {
	case rv.ctx.Err() != nil:
		rv.editStatus(ctx, statusID, "⏹ Stopped · "+elapsed.String())
	case !hasText:
		// No text (or an error): only the silent status line moves and the
		// ❌ receipt lands — the phone never buzzes for a reply-less run.
		rv.editStatus(ctx, statusID, statusFailed(elapsed, lastError, hasError))
		rv.b.react(ctx, rv.c, rv.promptID, reactionFailed)
	default:
		// Final render (finalized continuations already carry their complete
		// segment from their last edit). The tail may still exceed the flush
		// length if the run ended between pump ticks: drain it the same way.
		// Only the very first answer creation notifies; if earlier content
		// was already delivered (a live message or an overflow split), the
		// drain's creations are silent continuations.
		loudNext := answerID == 0 && !rv.answerContinued
		for {
			rest, done, tooLong := splitOverflow(seg)
			if !tooLong {
				break
			}
			if answerID == 0 {
				rv.b.sendText(ctx, rv.c, rv.renderSegment(done), nil, !loudNext)
				loudNext = false
			} else {
				rv.b.editText(ctx, rv.c, answerID, rv.renderSegment(done), nil)
			}
			answerID, seg = 0, rest
		}
		if len(seg) > 0 {
			if answerID == 0 {
				// Streaming edits never landed (API hiccup): one last-ditch
				// creation, otherwise the answer would vanish.
				rv.b.sendText(ctx, rv.c, rv.renderSegment(seg), nil, !loudNext)
			} else {
				rv.b.editText(ctx, rv.c, answerID, rv.renderSegment(seg), nil)
			}
		}
		rv.editStatus(ctx, statusID, statusDone(elapsed, tokens))
		rv.b.react(ctx, rv.c, rv.promptID, reactionDone)
	}

	if rv.b.pins {
		rv.b.unpinMessage(ctx, rv.c, rv.promptID)
	}
}

// editStatus edits the status line when it exists; a never-created status
// message (API hiccup) skips completion info silently.
func (rv *runView) editStatus(ctx context.Context, statusID int, text string) {
	if statusID == 0 {
		return
	}
	rv.b.editText(ctx, rv.c, statusID, text, nil)
}

// statusWorking is the pre-stream status line: ⚙ <last tool> · <elapsed>.
func statusWorking(lastTool string, elapsed time.Duration) string {
	if lastTool == "" {
		return "⚙ Working · " + elapsed.String()
	}
	return "⚙ " + lastTool + " · " + elapsed.String()
}

// statusDone is the compact completion line: ✓ 54s · 39.8k tok.
func statusDone(elapsed time.Duration, tokens int) string {
	s := "✓ " + elapsed.String()
	if tokens > 0 {
		s += " · " + humanTokens(tokens) + " tok"
	}
	return s
}

// statusFailed is the failure line: ❌ + error summary, or the no-output
// notice for a run that simply produced nothing.
func statusFailed(elapsed time.Duration, lastError string, hasError bool) string {
	if hasError {
		return "❌ " + truncateRunes(lastError, maxStatusErrLen) + " · " + elapsed.String()
	}
	return "⚠️ Finished (" + elapsed.String() + ") without any text output — check the server logs."
}

// sink view ---------------------------------------------------------------

// OnStream implements agentrun.EventSink: content deltas stream into the
// answer message; thinking is not displayed on Telegram.
func (rv *runView) OnStream(sessionID, content, thinking string) {
	if content == "" {
		return
	}
	rv.mu.Lock()
	rv.seg = append(rv.seg, []rune(content)...)
	rv.streamedAny = rv.streamedAny || strings.TrimSpace(content) != ""
	rv.dirty = true
	rv.mu.Unlock()
}

// OnLog implements agentrun.EventSink: tool calls feed the status line's last
// tool; error lines become the failure summary.
func (rv *runView) OnLog(sessionID, line string) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	if c, ok := strings.CutPrefix(line, "Call: "); ok {
		name := c
		if i := strings.IndexByte(c, '('); i > 0 {
			name = c[:i]
		}
		rv.lastTool = name
		return
	}
	if c, ok := strings.CutPrefix(line, "Error: "); ok {
		if !rv.hasError { // keep the first error; later ones are usually echoes
			rv.hasError = true
			rv.lastError = c
		}
	}
}

// OnUsage implements agentrun.EventSink.
func (rv *runView) OnUsage(sessionID string, tokens, percent int) {
	rv.mu.Lock()
	rv.tokens = tokens
	rv.mu.Unlock()
}

// OnDone implements agentrun.EventSink; terminal rendering happens in
// finalize, which runs after the driver returns.
func (rv *runView) OnDone(sessionID string) {}

func humanTokens(n int) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}
