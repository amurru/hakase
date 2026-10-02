// dispatch.go - inbound gateway routing: messages and interactions.
package discord

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"amurru/hakase/internal/channel/state"
	"amurru/hakase/internal/interfaces"

	"github.com/bwmarrin/discordgo"
)

// sendPacing spaces outbound messages per user (Discord per-channel rate
// limits; same 1100ms cadence as the Telegram transport). A var so tests
// can zero it.
var sendPacing = 1100 * time.Millisecond

// openDM returns the cached DM channel for a user, opening one via REST
// on first use.
func (b *Bot) openDM(userID int64) (string, error) {
	b.mu.Lock()
	if ch, ok := b.dmCache[userID]; ok {
		b.mu.Unlock()
		return ch, nil
	}
	b.mu.Unlock()
	ch, err := b.sender.OpenDM(itoa(userID))
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.dmCache[userID] = ch
	b.mu.Unlock()
	return ch, nil
}

// sendText chunks and paces one reply to a user's DM.
func (b *Bot) sendText(ctx context.Context, userID int64, text string) {
	if ctx.Err() != nil {
		return
	}
	ch, err := b.openDM(userID)
	if err != nil {
		b.log("open DM for %d: %v", userID, err)
		return
	}
	for _, chunk := range ChunkText(MarkdownToDiscord(text)) {
		if ctx.Err() != nil {
			return
		}
		b.mu.Lock()
		wait := time.Until(b.nextSend[userID])
		b.mu.Unlock()
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		if _, err := b.sender.Send(ch, chunk); err != nil {
			b.log("send to %d: %v", userID, err)
			return
		}
		b.mu.Lock()
		b.nextSend[userID] = time.Now().Add(sendPacing)
		b.mu.Unlock()
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}

// onMessageCreate routes inbound DM text: guild/self/empty drops, commands,
// auth gate, pending clarify answers, run starts.
func (b *Bot) onMessageCreate(_ *discordgo.Session, m *discordgo.MessageCreate) {
	if m == nil || m.Message == nil || m.Author == nil {
		return
	}
	if m.Author.Bot || m.Author.ID == b.sender.Me() {
		return // own messages and other bots
	}
	if m.GuildID != "" {
		b.log("ignoring guild message from %s (DM-only v1)", m.Author.ID)
		return
	}
	text := strings.TrimSpace(m.Content)
	userID, err := parseSnowflake(m.Author.ID)
	if err != nil {
		b.log("dropping message with unparseable author id: %v", err)
		return
	}
	if text == "" {
		if len(m.Attachments) > 0 {
			b.sendText(context.Background(), userID, "I can't look at attachments yet — send your question as text.")
		}
		return
	}
	if strings.HasPrefix(text, "/") {
		username := ""
		if m.Author.GlobalName != "" {
			username = m.Author.GlobalName
		} else {
			username = m.Author.Username
		}
		b.handleCommand(userID, username, text)
		return
	}
	if !b.auth.IsAllowed(userID) {
		if b.auth.DenyReplyAllowed(userID) {
			b.sendText(context.Background(), userID, "🔒 This bot is private. DM `pair CODE` with the code from the server console to link your Discord account.")
		}
		return
	}
	if b.takePendingOther(userID, text) {
		return
	}
	b.startRun(context.Background(), userID, m.ID, text)
}

// handleCommand runs slash-style text commands. `pair` is open (it is the
// pairing mechanism); everything else requires auth.
func (b *Bot) handleCommand(userID int64, username, text string) {
	ctx := context.Background()
	fields := strings.Fields(strings.TrimPrefix(text, "/"))
	if len(fields) == 0 {
		return
	}
	name := strings.ToLower(fields[0])
	args := fields[1:]

	if name == "pair" || name == "start" {
		b.cmdPair(userID, username, args)
		return
	}
	if !b.auth.IsAllowed(userID) {
		if b.auth.DenyReplyAllowed(userID) {
			b.sendText(ctx, userID, "🔒 This bot is private. DM `pair CODE` with the code from the server console to link your Discord account.")
		}
		return
	}
	switch name {
	case "new":
		b.cmdNew(userID)
	case "stop":
		b.cmdStop(userID)
	case "status":
		b.cmdStatus(userID)
	case "sessions":
		b.cmdSessions(userID)
	case "use":
		b.cmdUse(userID, args)
	case "help":
		b.cmdHelp(userID)
	default:
		b.sendText(ctx, userID, "Unknown command. Try /help.")
	}
}

// cmdPair links a Discord user with a pairing code.
func (b *Bot) cmdPair(userID int64, username string, args []string) {
	ctx := context.Background()
	if b.auth.IsAllowed(userID) {
		b.sendText(ctx, userID, "Already paired — send any message to start. Try /help for commands.")
		return
	}
	if len(args) == 0 {
		b.sendText(ctx, userID, "To pair, DM `pair CODE` with the code printed on the server console (`hakase channels pair-code` issues a fresh one).")
		return
	}
	if err := b.auth.TryPair(userID, username, strings.TrimSpace(args[0])); err != nil {
		b.sendText(ctx, userID, "That code didn't work — check it and try again (`pair CODE`).")
		return
	}
	b.sendText(ctx, userID, "Paired. Send any message to start a run. Try /help for commands.")
}

// cmdNew drops the current session binding so the next message starts fresh.
func (b *Bot) cmdNew(userID int64) {
	ctx := context.Background()
	key := state.ChatKey(ChannelName, userID)
	if err := b.store.Update(func(s *state.State) error {
		if ch, ok := s.Chats[key]; ok {
			ch.SessionID = ""
			s.Chats[key] = ch
		}
		return nil
	}); err != nil {
		b.sendText(ctx, userID, "⚠️ Could not reset the session: "+err.Error())
		return
	}
	b.sendText(ctx, userID, "Fresh session ready — send your first message.")
}

// cmdStop cancels the active run for this user.
func (b *Bot) cmdStop(userID int64) {
	if b.runs.Cancel(runKey(userID)) {
		b.sendText(context.Background(), userID, "Run cancelled.")
	} else {
		b.sendText(context.Background(), userID, "No active run.")
	}
}

// cmdStatus reports the run/session state for this user.
func (b *Bot) cmdStatus(userID int64) {
	ctx := context.Background()
	rk := runKey(userID)
	if run, ok := b.runs.Running(rk); ok {
		b.sendText(ctx, userID, "A run is active on session `"+run.SessionID+"`. Send /stop to cancel it.")
		return
	}
	st := b.store.Get()
	if ch, ok := st.Chats[state.ChatKey(ChannelName, userID)]; ok && ch.SessionID != "" {
		b.sendText(ctx, userID, "Idle. Bound session `"+ch.SessionID+"`. Send /new for a fresh one.")
		return
	}
	b.sendText(ctx, userID, "Idle, no session yet. Send any message to start.")
}

// cmdSessions lists recent sessions for rebinding.
func (b *Bot) cmdSessions(userID int64) {
	ctx := context.Background()
	summaries, err := b.sessions.ListSessions()
	if err != nil {
		b.sendText(ctx, userID, "⚠️ Could not list sessions: "+err.Error())
		return
	}
	if len(summaries) == 0 {
		b.sendText(ctx, userID, "No sessions yet.")
		return
	}
	var sb strings.Builder
	sb.WriteString("Recent sessions (`/use <id-prefix>` to rebind):\n")
	for i, s := range summaries {
		if i >= 10 {
			break
		}
		fmt.Fprintf(&sb, "`%s` — %s\n", shortID(s.ID), s.Title)
	}
	b.sendText(ctx, userID, sb.String())
}

// cmdUse rebinds this DM to an existing session by ID prefix.
func (b *Bot) cmdUse(userID int64, args []string) {
	ctx := context.Background()
	if len(args) == 0 {
		b.sendText(ctx, userID, "Usage: /use <session-id-prefix> (see /sessions).")
		return
	}
	summaries, err := b.sessions.ListSessions()
	if err != nil {
		b.sendText(ctx, userID, "⚠️ Could not list sessions: "+err.Error())
		return
	}
	var match string
	for _, s := range summaries {
		if strings.HasPrefix(s.ID, args[0]) {
			if match != "" {
				b.sendText(ctx, userID, "Prefix matches several sessions — be more specific (see /sessions).")
				return
			}
			match = s.ID
		}
	}
	if match == "" {
		b.sendText(ctx, userID, "No session starts with that prefix (see /sessions).")
		return
	}
	key := state.ChatKey(ChannelName, userID)
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		ch := s.Chats[key]
		ch.SessionID = match
		s.Chats[key] = ch
		return nil
	}); err != nil {
		b.sendText(ctx, userID, "⚠️ Could not bind the session: "+err.Error())
		return
	}
	b.sendText(ctx, userID, "Bound to session `"+match+"`. Send your next message.")
}

// cmdHelp lists the v1 command surface.
func (b *Bot) cmdHelp(userID int64) {
	b.sendText(context.Background(), userID,
		"Commands:\n"+
			"`/new` — start a fresh session\n"+
			"`/stop` — cancel the active run\n"+
			"`/status` — run/session state\n"+
			"`/sessions` — recent sessions\n"+
			"`/use <id-prefix>` — rebind to a session\n"+
			"Anything else runs the agent. Gate prompts answer with buttons.")
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// runKey is the one-run-per-user key (DMs have no threads).
func runKey(userID int64) string {
	return state.ChatKey(ChannelName, userID)
}

// takePendingOther answers a waiting clarify prompt with free text.
func (b *Bot) takePendingOther(userID int64, text string) bool {
	b.mu.Lock()
	p, ok := b.pendingOther[userID]
	if ok {
		delete(b.pendingOther, userID)
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	delivered := b.clarify != nil && b.clarify.RespondClarify(p.id, interfaces.ClarifyResponse{Answer: []string{text}})
	if !delivered {
		b.sendText(context.Background(), userID, "That question was already resolved or expired.")
	}
	return true
}

func (b *Bot) setPendingOther(userID int64, id string) {
	b.mu.Lock()
	b.pendingOther[userID] = pendingClarify{id: id, createdAt: time.Now()}
	b.mu.Unlock()
}

// rememberClarifyChoices caches a gate's choices for button callbacks.
func (b *Bot) rememberClarifyChoices(id string, choices []string) {
	b.mu.Lock()
	b.clarifyCtx[id] = clarifyChoice{choices: choices, createdAt: time.Now()}
	b.mu.Unlock()
	go b.clarifyGC()
}

// clarifyGC drops choice/pending entries older than 30 minutes.
func (b *Bot) clarifyGC() {
	cutoff := time.Now().Add(-30 * time.Minute)
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, c := range b.clarifyCtx {
		if c.createdAt.Before(cutoff) {
			delete(b.clarifyCtx, id)
		}
	}
	for u, p := range b.pendingOther {
		if p.createdAt.Before(cutoff) {
			delete(b.pendingOther, u)
		}
	}
}
