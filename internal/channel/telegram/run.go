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
	"amurru/hakase/internal/web/sse"

	"google.golang.org/genai"
)

// streamEditInterval spaces render passes: at most one answer edit per
// interval (only when content changed), and the pre-stream status line is
// refreshed on the same cadence. A var so tests can shorten it.
var streamEditInterval = 2 * time.Second

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

	sessionID, err := b.resolveSession(c, prompt)
	if err != nil {
		b.sendText(ctx, c, "⚠️ Could not resolve a session: "+err.Error(), nil, false)
		return
	}

	runCtx, cancel := context.WithCancel(context.Background())
	if !b.runs.TryStart(rk, sessionID, cancel) {
		cancel()
		b.sendText(ctx, c, "⏳ A run is already active here — send /stop to cancel it first.", nil, false)
		return
	}

	// Persist the user turn (same contract as the web handler): the prompt
	// plus attachment refs, written to THIS run's session — never to the
	// global active-session pointer, which parallel runs race over. After the
	// TryStart gate, so a refused prompt does not pollute the session.
	fullPrompt := prompt
	if len(manifest) > 0 {
		fullPrompt = strings.TrimSpace(fullPrompt + "\n[attachments]\n" + strings.Join(manifest, "\n"))
	}
	if err := b.sessions.RecordUsageInSession(sessionID, "user", fullPrompt, "", 0, refs); err != nil {
		b.log("failed to save user message: %v", err)
	}

	parts := make([]*genai.Part, 0, len(photoParts)+1)
	if fullPrompt != "" {
		parts = append(parts, genai.NewPartFromText(fullPrompt))
	}
	parts = append(parts, photoParts...)
	if len(parts) == 0 {
		cancel()
		b.runs.Finish(rk)
		return
	}
	content := genai.NewContentFromParts(parts, genai.RoleUser)

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

// resolveSession returns the conversation's bound session, validating it
// still exists; otherwise it creates (and binds) a fresh one titled from the
// prompt. Topics-mode threads bind through state.Threads and rename the
// Telegram topic to the session title; the root area keeps the legacy chat
// binding (exactly today's behavior).
func (b *Bot) resolveSession(c conv, prompt string) (string, error) {
	st := b.store.Get()
	if c.threadID == 0 {
		if chat, ok := st.Chats[chatKey(c.chatID)]; ok && chat.SessionID != "" {
			if _, err := b.sessions.Store().Load(chat.SessionID); err == nil {
				return chat.SessionID, nil
			}
			// Bound session vanished (deleted/archived elsewhere): fall
			// through and create a replacement.
		}
	} else if th, ok := st.Threads[threadKey(c)]; ok && th.SessionID != "" {
		if _, err := b.sessions.Store().Load(th.SessionID); err == nil {
			return th.SessionID, nil
		}
	}
	title := "Telegram · " + firstRunes(prompt, 40)
	if strings.TrimSpace(prompt) == "" {
		title = "Telegram · " + time.Now().Format("Jan 2 15:04")
	}
	sess, err := b.sessions.CreateSession(title)
	if err != nil {
		return "", err
	}
	if c.threadID == 0 {
		if err := b.store.Update(func(s *state.State) error {
			if s.Chats == nil {
				s.Chats = map[string]state.Chat{}
			}
			s.Chats[chatKey(c.chatID)] = state.Chat{SessionID: sess.ID}
			return nil
		}); err != nil {
			b.log("cannot persist chat binding: %v", err)
		}
	} else {
		// Binding first, rename second: a failed rename (bots may lack the
		// rights in edge cases) must not lose the session.
		if err := b.bindThread(c, sess.ID, sess.Title); err != nil {
			b.log("cannot persist thread binding: %v", err)
		}
		b.renameTopic(context.Background(), c, sess.Title)
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
	streaming := rv.streamedAny
	answerID := rv.answerID
	statusID := rv.statusID
	lastTool := rv.lastTool
	dirty := rv.dirty
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

	// Overflow: when the rendered segment exceeds the flush length, close the
	// current message and continue in a fresh one. The split is computed from
	// and committed to rv.seg in one locked step, so deltas appended while we
	// were pacing the previous render are part of the split, never lost.
	overflowCommitted := false
	if len(seg) > streamFlushLen {
		var rest, done []rune
		rv.mu.Lock()
		if r, d, ok := splitOverflow(rv.seg); ok {
			rv.seg = append([]rune(nil), r...)
			rv.answerID = 0
			rv.answerContinued = true
			rv.dirty = true
			rest, done, overflowCommitted = r, d, true
		}
		rv.mu.Unlock()
		if overflowCommitted {
			if answerID != 0 {
				html, commit := rv.renderSegment(done)
				rv.b.editText(rv.ctx, rv.c, answerID, html, nil)
				commit()
			} else {
				// No message owns the closed part yet: create it now. This is
				// the first answer creation — the turn's one notification.
				html, commit := rv.renderSegment(done)
				if msg := rv.b.sendText(rv.ctx, rv.c, html, nil, false); msg != nil {
					commit()
					rv.mu.Lock()
					rv.answerID = msg.ID
					rv.mu.Unlock()
				}
			}
			seg, answerID, dirty = rest, 0, true
		}
	}

	if answerID == 0 {
		if !dirty {
			return // nothing new to render into a message yet
		}
		// First content arrived: create the answer message (the turn's one
		// notification; continuation messages are created silently). Render
		// the freshest segment; if more deltas land while the creation is in
		// flight, dirty stays set and the next tick edits them in.
		rv.mu.Lock()
		silent := rv.answerContinued
		cur := make([]rune, len(rv.seg))
		copy(cur, rv.seg)
		rv.mu.Unlock()
		html, commit := rv.renderSegment(cur)
		msg := rv.b.sendText(rv.ctx, rv.c, html, nil, silent)
		if msg == nil {
			return // creation failed; the next tick (or finalize) retries
		}
		commit() // only on success: a retry must start from the old fence state
		rv.mu.Lock()
		rv.answerID = msg.ID
		if len(rv.seg) == len(cur) {
			rv.dirty = false
		}
		rv.mu.Unlock()
		return
	}
	if dirty && len(seg) > 0 {
		html, commit := rv.renderSegment(seg)
		rv.b.editText(rv.ctx, rv.c, answerID, html, nil)
		commit()
		rv.mu.Lock()
		if len(rv.seg) == len(seg) {
			rv.dirty = false
		}
		rv.mu.Unlock()
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
// mechanism ChunkReply uses, so split answers render balanced tags). The
// returned commit persists the advanced fence state — call it only once the
// render has been delivered, so a failed creation retries from the old state.
func (rv *runView) renderSegment(seg []rune) (html string, commit func()) {
	rv.mu.Lock()
	fence := rv.inFence
	rv.mu.Unlock()
	html = channel.MarkdownToTelegramHTMLState(string(seg), &fence)
	return html, func() {
		rv.mu.Lock()
		rv.inFence = fence
		rv.mu.Unlock()
	}
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
				html, commit := rv.renderSegment(done)
				rv.b.sendText(ctx, rv.c, html, nil, !loudNext)
				commit()
				loudNext = false
			} else {
				html, commit := rv.renderSegment(done)
				rv.b.editText(ctx, rv.c, answerID, html, nil)
				commit()
			}
			answerID, seg = 0, rest
		}
		if len(seg) > 0 {
			html, commit := rv.renderSegment(seg)
			if answerID == 0 {
				// Streaming edits never landed (API hiccup): one last-ditch
				// creation, otherwise the answer would vanish.
				rv.b.sendText(ctx, rv.c, html, nil, !loudNext)
			} else {
				rv.b.editText(ctx, rv.c, answerID, html, nil)
			}
			commit()
		}
		rv.editStatus(ctx, statusID, statusDone(elapsed, tokens, lastError, hasError))
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

// statusDone is the compact completion line: ✓ 54s · 39.8k tok. An error
// that arrived after text was already streaming is appended — the answer is
// out, but the user should know the run did not end cleanly.
func statusDone(elapsed time.Duration, tokens int, lastError string, hasError bool) string {
	s := "✓ " + elapsed.String()
	if tokens > 0 {
		s += " · " + humanTokens(tokens) + " tok"
	}
	if hasError {
		s += " · ⚠ " + truncateRunes(lastError, maxStatusErrLen)
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

// mirrorBridge forwards a sink event to the SSE bridge when the transport has
// one, so a channel-started run is watchable live from the web UI exactly
// like a web-started one (the webui consumes the same stream events).
func (rv *runView) mirrorBridge(f func(bridge *sse.EventBridge)) {
	if b := rv.b.bridge; b != nil {
		f(b)
	}
}

// OnStream implements agentrun.EventSink: content deltas stream into the
// answer message; thinking is not displayed on Telegram but is mirrored to
// the web bridge.
func (rv *runView) OnStream(sessionID, content, thinking string) {
	if content == "" && thinking == "" {
		return
	}
	rv.mu.Lock()
	if content != "" {
		rv.seg = append(rv.seg, []rune(content)...)
		rv.streamedAny = rv.streamedAny || strings.TrimSpace(content) != ""
		rv.dirty = true
	}
	rv.mu.Unlock()
	rv.mirrorBridge(func(b *sse.EventBridge) { b.SendStreamContent(sessionID, content, thinking) })
}

// OnLog implements agentrun.EventSink: tool calls feed the status line's last
// tool; error lines become the failure summary. All lines mirror to the bridge.
func (rv *runView) OnLog(sessionID, line string) {
	rv.mirrorBridge(func(b *sse.EventBridge) { b.SendLog(sessionID, line) })
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
	rv.mirrorBridge(func(b *sse.EventBridge) { b.SendUsage(sessionID, tokens, percent) })
	rv.mu.Lock()
	rv.tokens = tokens
	rv.mu.Unlock()
}

// OnDone implements agentrun.EventSink; terminal rendering happens in
// finalize, which runs after the driver returns.
func (rv *runView) OnDone(sessionID string) {
	rv.mirrorBridge(func(b *sse.EventBridge) { b.SendDone(sessionID) })
}

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
