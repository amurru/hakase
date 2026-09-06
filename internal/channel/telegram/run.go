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

// statusEditInterval spaces live status-message edits (Telegram re-renders
// edits aggressively; ~2.5s keeps the chat readable and the API quiet).
const statusEditInterval = 2500 * time.Millisecond

// statusToolLines is how many recent tool lines the status message shows.
const statusToolLines = 8

// startRun is the inbound-prompt entry point: it resolves the conversation's
// session, persists the user turn, and launches the shared driver with a
// Telegram status sink. parts/refs/manifest carry photos (genai inline data,
// session attachment refs, and the manifest lines appended to the prompt).
func (b *Bot) startRun(ctx context.Context, c conv, prompt string, photoParts []*genai.Part, refs []hakasesession.AttachmentRef, manifest []string) {
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

	rs := newRunStatus(b, c, runCtx)
	go func() {
		defer b.runs.Finish(rk)
		stopEdits := make(chan struct{})
		go rs.beginEditing(stopEdits)
		b.driver.RunTurn(runCtx, sessionID, content, rs)
		close(stopEdits)
		rs.finalize()
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

// runStatus carries the live-rendering state of one run's status message. It
// doubles as the agentrun.EventSink for the turn.
type runStatus struct {
	b       *Bot
	c       conv
	msgID   int
	started time.Time
	ctx     context.Context

	mu        sync.Mutex
	toolLines []string
	tokens    int
	content   strings.Builder
	done      bool
}

func newRunStatus(b *Bot, c conv, ctx context.Context) *runStatus {
	return &runStatus{b: b, c: c, started: time.Now(), ctx: ctx}
}

// beginEditing posts the initial status message and keeps it refreshed until
// stop is closed (or the run ctx is cancelled). Runs in its own goroutine.
func (rs *runStatus) beginEditing(stop <-chan struct{}) {
	// The status line is progress traffic: always silent.
	msg := rs.b.sendText(rs.ctx, rs.c, rs.render(), nil, true)
	if msg != nil {
		rs.setMsgID(msg.ID)
	}
	ticker := time.NewTicker(statusEditInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-rs.ctx.Done():
			return
		case <-ticker.C:
			if id := rs.getMsgID(); id != 0 {
				rs.b.editText(rs.ctx, rs.c, id, rs.render(), nil)
			}
		}
	}
}

// setMsgID records the status message id (cross-goroutine safe).
func (rs *runStatus) setMsgID(id int) {
	rs.mu.Lock()
	rs.msgID = id
	rs.mu.Unlock()
}

func (rs *runStatus) getMsgID() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.msgID
}

// finalize edits the status to its terminal state and delivers the reply.
func (rs *runStatus) finalize() {
	rs.mu.Lock()
	reply := rs.content.String()
	tokens := rs.tokens
	rs.mu.Unlock()

	msgID := rs.getMsgID()
	elapsed := time.Since(rs.started).Round(time.Second)
	switch {
	case rs.ctx.Err() != nil:
		if msgID != 0 {
			rs.b.editText(context.Background(), rs.c, msgID, "⏹ Stopped ("+elapsed.String()+")", nil)
		}
		rs.b.sendText(context.Background(), rs.c, "⏹ Run stopped.", nil, false)
	case strings.TrimSpace(reply) == "":
		if msgID != 0 {
			rs.b.editText(context.Background(), rs.c, msgID, "⚠️ Finished ("+elapsed.String()+") without any text output — check the server logs.", nil)
		}
	default:
		summary := fmt.Sprintf("✅ Done · %s", elapsed.String())
		if tokens > 0 {
			summary += fmt.Sprintf(" · %s tok", humanTokens(tokens))
		}
		if msgID != 0 {
			rs.b.editText(context.Background(), rs.c, msgID, summary, nil)
		}
		for _, chunk := range channel.ChunkReply(reply) {
			rs.b.sendText(context.Background(), rs.c, chunk, nil, false)
		}
	}
}

// sink view ---------------------------------------------------------------

// OnStream implements agentrun.EventSink: content deltas accumulate for the
// final reply; thinking is not displayed on Telegram.
func (rs *runStatus) OnStream(sessionID, content, thinking string) {
	rs.mu.Lock()
	rs.content.WriteString(content)
	rs.mu.Unlock()
}

// OnLog implements agentrun.EventSink: tool calls/responses and errors become
// compact status lines.
func (rs *runStatus) OnLog(sessionID, line string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if c, ok := strings.CutPrefix(line, "Call: "); ok {
		name := c
		if i := strings.IndexByte(c, '('); i > 0 {
			name = c[:i]
		}
		rs.toolLines = append(rs.toolLines, "🔧 "+name)
	} else if c, ok := strings.CutPrefix(line, "Response: "); ok {
		rs.toolLines = append(rs.toolLines, "✓ "+c)
	} else {
		rs.toolLines = append(rs.toolLines, line)
	}
	if len(rs.toolLines) > statusToolLines {
		rs.toolLines = rs.toolLines[len(rs.toolLines)-statusToolLines:]
	}
}

// OnUsage implements agentrun.EventSink.
func (rs *runStatus) OnUsage(sessionID string, tokens, percent int) {
	rs.mu.Lock()
	rs.tokens = tokens
	rs.mu.Unlock()
}

// OnDone implements agentrun.EventSink.
func (rs *runStatus) OnDone(sessionID string) {
	rs.mu.Lock()
	rs.done = true
	rs.mu.Unlock()
}

// render builds the current status text.
func (rs *runStatus) render() string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	elapsed := time.Since(rs.started).Round(time.Second)
	var b strings.Builder
	if rs.done {
		b.WriteString("✅ Done · " + elapsed.String())
	} else {
		b.WriteString("⚙️ Working · " + elapsed.String())
	}
	if rs.tokens > 0 {
		b.WriteString(fmt.Sprintf(" · %s tok", humanTokens(rs.tokens)))
	}
	b.WriteString("\n")
	for _, l := range rs.toolLines {
		b.WriteString("<code>" + l + "</code>\n")
	}
	return strings.TrimRight(b.String(), "\n")
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
