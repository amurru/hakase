// run.go - per-DM agent runs: session binding, streaming runView.
package discord

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/channel/state"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/web/sse"

	"google.golang.org/genai"
)

// statusEditPacing floors status-message edits (DC-009: Discord pacing is
// stricter than Telegram's 2s pump).
const statusEditPacing = 5 * time.Second

// startRun launches one agent turn for a DM prompt (busy-guarded,
// session-bound, manifest-persisted like every other surface).
func (b *Bot) startRun(ctx context.Context, userID int64, promptMsgID, prompt string) {
	rk := runKey(userID)
	if _, running := b.runs.Running(rk); running {
		b.sendText(ctx, userID, "⏳ A run is already active here — send /stop to cancel it first.")
		return
	}
	if b.driver == nil || b.sessions == nil {
		b.sendText(ctx, userID, "⚠️ Agent runtime unavailable (channel not wired to a runner).")
		return
	}

	sessionID, err := b.resolveSession(userID, prompt)
	if err != nil {
		b.sendText(ctx, userID, "⚠️ Could not resolve a session: "+err.Error())
		return
	}
	_ = promptMsgID // receipt reactions use the prompt message below

	runCtx, cancel := context.WithCancel(context.Background())
	if !b.runs.TryStart(rk, sessionID, cancel) {
		cancel()
		b.sendText(ctx, userID, "⏳ A run is already active here — send /stop to cancel it first.")
		return
	}

	if err := b.sessions.RecordUsageInSession(sessionID, "user", prompt, "", 0, nil); err != nil {
		b.log("failed to save user message: %v", err)
	}
	content := genai.NewContentFromParts([]*genai.Part{genai.NewPartFromText(prompt)}, genai.RoleUser)

	rv := newRunView(b, userID, sessionID, runCtx)
	go func() {
		defer b.runs.Finish(rk)
		rv.begin(ctx, promptMsgID)
		stop := make(chan struct{})
		go rv.pumpLoop(stop)
		b.driver.RunTurn(runCtx, sessionID, content, rv)
		close(stop)
		rv.finalize(ctx)
	}()
}

// resolveSession returns the user's bound session, validating it still
// exists; otherwise creates (and binds) a fresh "Discord · …" one.
// Rebinds preserve the chat record (Notify prefs survive).
func (b *Bot) resolveSession(userID int64, prompt string) (string, error) {
	key := state.ChatKey(ChannelName, userID)
	st := b.store.Get()
	if chat, ok := st.Chats[key]; ok && chat.SessionID != "" {
		if _, err := b.sessions.Store().Load(chat.SessionID); err == nil {
			return chat.SessionID, nil
		}
		// Bound session vanished elsewhere: fall through and replace.
	}
	title := "Discord · " + firstRunes(prompt, 40)
	if strings.TrimSpace(prompt) == "" {
		title = "Discord · " + time.Now().Format("Jan 2 15:04")
	}
	sess, err := b.sessions.CreateSession(title)
	if err != nil {
		return "", err
	}
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		chat := s.Chats[key]
		chat.SessionID = sess.ID
		s.Chats[key] = chat
		return nil
	}); err != nil {
		b.log("cannot persist chat binding: %v", err)
	}
	return sess.ID, nil
}

func firstRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n])
}

// runView streams one turn into a DM: a receipt reaction, a paced status
// message while working, and the chunked final answer.
type runView struct {
	b         *Bot
	userID    int64
	sessionID string
	ctx       context.Context

	mu        sync.Mutex
	answer    strings.Builder
	streamed  bool
	lastTool  string
	lastError string
	hasError  bool
	tokens    int
	statusID  string
	channelID string
	lastEdit  time.Time
	dirty     bool
}

func newRunView(b *Bot, userID int64, sessionID string, ctx context.Context) *runView {
	return &runView{b: b, userID: userID, sessionID: sessionID, ctx: ctx}
}

// begin reacts to the prompt and posts the working status message.
func (rv *runView) begin(ctx context.Context, promptMsgID string) {
	ch, err := rv.b.openDM(rv.userID)
	if err != nil {
		return
	}
	rv.channelID = ch
	_ = rv.b.sender.React(ch, promptMsgID, "👀")
	id, err := rv.b.sender.Send(ch, "Working on it…")
	if err != nil {
		rv.b.log("status message: %v", err)
		return
	}
	rv.statusID = id
	rv.lastEdit = time.Now()
}

// pumpLoop refreshes the status message at most every statusEditPacing
// while the turn runs.
func (rv *runView) pumpLoop(stop <-chan struct{}) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-rv.ctx.Done():
			return
		case <-t.C:
			rv.maybeEdit()
		}
	}
}

func (rv *runView) statusText() string {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	var sb strings.Builder
	sb.WriteString("Working")
	if rv.lastTool != "" {
		sb.WriteString(" — `")
		sb.WriteString(rv.lastTool)
		sb.WriteString("`")
	}
	if rv.tokens > 0 {
		fmt.Fprintf(&sb, " (%d tokens)", rv.tokens)
	}
	sb.WriteString("…")
	return sb.String()
}

func (rv *runView) maybeEdit() {
	rv.mu.Lock()
	if !rv.dirty || rv.statusID == "" || time.Since(rv.lastEdit) < statusEditPacing {
		rv.mu.Unlock()
		return
	}
	rv.dirty = false
	rv.lastEdit = time.Now()
	rv.mu.Unlock()
	text := rv.statusText()
	if err := rv.b.sender.Edit(rv.channelID, rv.statusID, text); err != nil {
		rv.b.log("status edit: %v", err)
	}
}

// finalize replaces the status with the outcome and sends the chunked
// answer as follow-ups (edits are paced; answers are sends).
func (rv *runView) finalize(ctx context.Context) {
	rv.mu.Lock()
	answer := strings.TrimSpace(rv.answer.String())
	streamed := rv.streamed
	hasError := rv.hasError
	lastError := rv.lastError
	statusID := rv.statusID
	rv.mu.Unlock()

	outcome := "Done."
	if hasError {
		outcome = "Run failed."
		if lastError != "" {
			outcome += " " + lastError
		}
	}
	if statusID != "" {
		_ = rv.b.sender.Edit(rv.channelID, statusID, outcome)
	}
	if streamed && answer != "" {
		for _, chunk := range ChunkText(MarkdownToDiscord(answer)) {
			rv.b.sendText(ctx, rv.userID, chunk)
		}
	} else if !streamed && !hasError {
		rv.b.sendText(ctx, rv.userID, "Done — no text output.")
	}
}

// mirrorBridge forwards a sink event to the SSE bridge so a Discord-started
// run is watchable live from the web UI exactly like any other surface.
func (rv *runView) mirrorBridge(f func(bridge *sse.EventBridge)) {
	if b := rv.b.bridge; b != nil {
		f(b)
	}
}

// OnStream implements agentrun.EventSink.
func (rv *runView) OnStream(sessionID, content, thinking string) {
	if content == "" && thinking == "" {
		return
	}
	rv.mu.Lock()
	if content != "" {
		rv.answer.WriteString(content)
		rv.streamed = rv.streamed || strings.TrimSpace(content) != ""
		rv.dirty = true
	}
	rv.mu.Unlock()
	rv.mirrorBridge(func(b *sse.EventBridge) { b.SendStreamContent(sessionID, content, thinking) })
}

// OnLog implements agentrun.EventSink.
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
		rv.dirty = true
		return
	}
	if c, ok := strings.CutPrefix(line, "Error: "); ok {
		if !rv.hasError {
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
	rv.dirty = true
	rv.mu.Unlock()
}

// OnDone implements agentrun.EventSink; terminal rendering is finalize.
func (rv *runView) OnDone(sessionID string) {
	rv.mirrorBridge(func(b *sse.EventBridge) { b.SendDone(sessionID) })
}

// OnGraphEvent implements agentrun.EventSink.
func (rv *runView) OnGraphEvent(sessionID string, ev interfaces.GraphEvent) {
	rv.mirrorBridge(func(b *sse.EventBridge) { b.SendGraph(sessionID, ev) })
}

var _ agentrun.EventSink = (*runView)(nil)
