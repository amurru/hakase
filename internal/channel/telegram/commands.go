package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/channel"
	"amurru/hakase/internal/channel/state"

	"github.com/go-telegram/bot/models"
)

// handleCommand dispatches /commands. Only /start and /id work for
// unauthenticated users (pairing entry points); everything else is gated.
// Commands act on the conversation they arrived in (the topic, or the root
// area).
func (b *Bot) handleCommand(ctx context.Context, c conv, m *models.Message) {
	first := strings.Fields(m.Text)[0]
	cmd := strings.TrimPrefix(first, "/")
	if i := strings.IndexByte(cmd, '@'); i > 0 {
		cmd = cmd[:i]
	}
	args := strings.TrimSpace(strings.TrimPrefix(m.Text, first))

	// Pairing gate: /start is the only auth path; /id is harmless.
	if cmd != "start" && cmd != "id" && !b.auth.IsAllowed(m.From.ID) {
		if b.auth.DenyReplyAllowed(m.From.ID) {
			b.sendText(ctx, c, "🔒 Unauthorized. Pair with <code>/start &lt;code&gt;</code> — the code is printed on the server console.", nil, false)
		}
		return
	}

	switch cmd {
	case "start":
		b.cmdStart(ctx, c, m, args)
	case "help":
		b.cmdHelp(ctx, c)
	case "id":
		b.cmdID(ctx, c, m)
	case "new":
		b.cmdNew(ctx, c, m, args)
	case "sessions":
		b.cmdSessions(ctx, c, m)
	case "use":
		b.cmdUse(ctx, c, m, args)
	case "topic":
		b.cmdTopic(ctx, c, m, args)
	case "status":
		b.cmdStatus(ctx, c, m)
	case "stop":
		b.cmdStop(ctx, c)
	case "tasks":
		b.cmdTasks(ctx, c, args)
	case "cron":
		b.cmdCron(ctx, c, args)
	case "notify":
		b.cmdNotify(ctx, c, m, args)
	default:
		b.sendText(ctx, c, "Unknown command — /help lists what I can do.", nil, false)
	}
}

func (b *Bot) cmdStart(ctx context.Context, c conv, m *models.Message, args string) {
	if b.auth.IsAllowed(m.From.ID) {
		b.sendText(ctx, c, greeting, nil, false)
		return
	}
	if strings.TrimSpace(args) == "" {
		b.sendText(ctx, c, "Send <code>/start &lt;code&gt;</code> with the pairing code from the server console to pair this account.", nil, false)
		return
	}
	if err := b.auth.TryPair(m.From.ID, m.From.Username, args); err != nil {
		b.log("pairing failed for user %d (%s): %v", m.From.ID, m.From.Username, err)
		b.sendText(ctx, c, "❌ "+err.Error()+" — generate a fresh one with <code>hakase channels pair-code</code> on the server.", nil, false)
		return
	}
	b.log("user %d (%s) paired successfully", m.From.ID, m.From.Username)
	b.sendText(ctx, c, "✅ Paired! You can now talk to hakase from here.\n\n"+greeting, nil, false)
}

const greeting = "I'm hakase, your agent. Send me a prompt (or a photo with a caption) and I'll run it with live progress. " +
	"Approvals and questions arrive as buttons here. /help lists the commands."

func (b *Bot) cmdHelp(ctx context.Context, c conv) {
	b.sendText(ctx, c, helpText, nil, false)
}

const helpText = `<b>hakase over Telegram</b>

Send any text as a prompt for the current session; a photo with a caption sends the image to the agent. The answer streams into one message with a quiet status line; <code>/stop</code> cancels.

<b>Topics</b>
/topic — turn the DM into topic conversations (one topic = one session)
/topic &lt;id-prefix&gt; — bind the current topic to a session
/topic off — back to a single conversation (bindings kept)
In topics mode, open topics with the ✚ composer button; the root area is a lobby.

<b>Commands</b>
/new [title] — start a fresh session (in a topic: resets that topic)
/sessions — list recent sessions
/use &lt;id-prefix&gt; — switch this chat or topic to a session
/status — current session, run state, open tasks
/tasks [filter] — task board ("open", "completed", …)
/cron — list jobs; /cron run|pause|resume &lt;name&gt;
/stop — cancel the running turn
/notify on|off — push notifications for cron/task completions
/id — show your Telegram ids
/start &lt;code&gt; — pair this account`

func (b *Bot) cmdID(ctx context.Context, c conv, m *models.Message) {
	b.sendText(ctx, c, fmt.Sprintf("user id: <code>%d</code>\nchat id: <code>%d</code>", m.From.ID, c.chatID), nil, false)
}

// cmdTopic manages topics mode: bare /topic enables it, /topic off returns
// to the legacy single conversation, and /topic &lt;session-id-prefix&gt; binds
// the current topic to an existing session (re-titling the topic).
func (b *Bot) cmdTopic(ctx context.Context, c conv, m *models.Message, args string) {
	ck := chatKey(c.chatID)
	arg := strings.TrimSpace(args)

	setTopics := func(on bool) {
		if err := b.store.Update(func(s *state.State) error {
			if s.Chats == nil {
				s.Chats = map[string]state.Chat{}
			}
			chat := s.Chats[ck]
			chat.TopicsMode = on
			s.Chats[ck] = chat
			return nil
		}); err != nil {
			b.sendText(ctx, c, "⚠️ "+err.Error(), nil, false)
		}
	}

	switch {
	case arg == "":
		if b.topicsMode(c.chatID) {
			b.sendText(ctx, c, "💬 Topics mode is already on. Open a topic with the ✚ composer button and prompt there; <code>/topic off</code> switches back.", nil, false)
			return
		}
		setTopics(true)
		b.sendText(ctx, c, "💬 Topics mode is on — every topic is its own conversation with its own session. Tap the ✚ / “All Messages” composer button to open a topic and prompt there. This root area is now a lobby (commands still work); <code>/topic off</code> returns to a single chat.", nil, false)
	case strings.EqualFold(arg, "off"):
		setTopics(false)
		b.sendText(ctx, c, "💬 Topics mode is off — the root area is your chat again. Topic bindings are kept for when you re-enable it.", nil, false)
	default:
		if c.threadID == 0 {
			b.sendText(ctx, c, "Usage: <code>/topic &lt;session-id-prefix&gt;</code> inside a topic (see /sessions). Bare <code>/topic</code> enables topics mode.", nil, false)
			return
		}
		if _, running := b.runs.Running(threadKey(c)); running {
			b.sendText(ctx, c, "⏳ A run is active — /stop it before switching sessions.", nil, false)
			return
		}
		id, failMsg := b.sessionByPrefix(arg)
		if id == "" {
			b.sendText(ctx, c, failMsg, nil, false)
			return
		}
		sess, err := b.sessions.Store().Load(id)
		if err != nil {
			b.sendText(ctx, c, "⚠️ "+err.Error(), nil, false)
			return
		}
		if err := b.bindThread(c, id, sess.Title); err != nil {
			b.sendText(ctx, c, "⚠️ "+err.Error(), nil, false)
			return
		}
		b.renameTopic(ctx, c, sess.Title)
		b.sendText(ctx, c, "🔗 This topic now talks to session <b>"+esc(sess.Title)+"</b>\n<code>"+id+"</code>", nil, false)
	}
}

// cmdNew starts a fresh session: in a topic it resets that topic's session
// (re-titling it), in a lobby it points at the ✚ button, and without topics
// mode it rebinds the chat exactly as before.
func (b *Bot) cmdNew(ctx context.Context, c conv, m *models.Message, args string) {
	if b.inLobby(c) {
		b.sendText(ctx, c, "💬 Create a topic with the ✚ composer button and run <code>/new</code> there — the root area is a lobby. (<code>/topic off</code> returns to a single chat.)", nil, false)
		return
	}
	if c.threadID != 0 {
		if _, running := b.runs.Running(threadKey(c)); running {
			b.sendText(ctx, c, "⏳ A run is active — /stop it before switching sessions.", nil, false)
			return
		}
		title := strings.TrimSpace(args)
		if title == "" {
			title = "Telegram · " + time.Now().Format("Jan 2 15:04")
		}
		sess, err := b.sessions.CreateSession(title)
		if err != nil {
			b.sendText(ctx, c, "⚠️ "+err.Error(), nil, false)
			return
		}
		if err := b.bindThread(c, sess.ID, sess.Title); err != nil {
			b.log("cannot persist thread binding: %v", err)
		}
		b.renameTopic(ctx, c, sess.Title)
		b.sendText(ctx, c, fmt.Sprintf("🆕 Session <b>%s</b> created and bound to this topic.\n<code>%s</code>", sess.Title, sess.ID), nil, false)
		return
	}

	ck := chatKey(c.chatID)
	if _, running := b.runs.Running(threadKey(c)); running {
		b.sendText(ctx, c, "⏳ A run is active — /stop it before switching sessions.", nil, false)
		return
	}
	title := strings.TrimSpace(args)
	if title == "" {
		title = "Telegram · " + time.Now().Format("Jan 2 15:04")
	}
	sess, err := b.sessions.CreateSession(title)
	if err != nil {
		b.sendText(ctx, c, "⚠️ "+err.Error(), nil, false)
		return
	}
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		s.Chats[ck] = state.Chat{SessionID: sess.ID}
		return nil
	}); err != nil {
		b.log("cannot persist chat binding: %v", err)
	}
	b.sendText(ctx, c, fmt.Sprintf("🆕 Session <b>%s</b> created and bound here.\n<code>%s</code>", sess.Title, sess.ID), nil, false)
}

func (b *Bot) cmdSessions(ctx context.Context, c conv, m *models.Message) {
	summaries, err := b.sessions.ListSessions()
	if err != nil {
		b.sendText(ctx, c, "⚠️ "+err.Error(), nil, false)
		return
	}
	bound := b.boundSessionID(c)
	n := len(summaries)
	if n > 8 {
		n = 8
	}
	var txt strings.Builder
	txt.WriteString(fmt.Sprintf("📚 Recent sessions (%d total)\n", len(summaries)))
	for _, s := range summaries[:n] {
		marker := "  "
		if s.ID == bound {
			marker = "▶️"
		}
		txt.WriteString(fmt.Sprintf("%s %s\n   <code>%s</code> · %s\n", marker, s.Title, shortID(s.ID), age(s.UpdatedAt)))
	}
	b.sendText(ctx, c, strings.TrimRight(txt.String(), "\n"), nil, false)
}

// cmdUse switches the conversation (topic in topics mode, chat otherwise) to
// an existing session by id prefix.
func (b *Bot) cmdUse(ctx context.Context, c conv, m *models.Message, args string) {
	prefix := strings.TrimSpace(args)
	if len(prefix) < 4 {
		b.sendText(ctx, c, "Usage: /use &lt;id-prefix&gt; (at least 4 chars — see /sessions)", nil, false)
		return
	}
	if _, running := b.runs.Running(threadKey(c)); running {
		b.sendText(ctx, c, "⏳ A run is active — /stop it before switching sessions.", nil, false)
		return
	}
	id, failMsg := b.sessionByPrefix(prefix)
	if id == "" {
		b.sendText(ctx, c, failMsg, nil, false)
		return
	}

	if c.threadID != 0 {
		sess, err := b.sessions.Store().Load(id)
		if err != nil {
			b.sendText(ctx, c, "⚠️ "+err.Error(), nil, false)
			return
		}
		if err := b.bindThread(c, id, sess.Title); err != nil {
			b.sendText(ctx, c, "⚠️ "+err.Error(), nil, false)
			return
		}
		b.renameTopic(ctx, c, sess.Title)
		b.sendText(ctx, c, "🔗 This topic now talks to session <code>"+id+"</code>.", nil, false)
		return
	}

	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		chat := s.Chats[chatKey(c.chatID)]
		chat.SessionID = id
		s.Chats[chatKey(c.chatID)] = chat
		return nil
	}); err != nil {
		b.sendText(ctx, c, "⚠️ "+err.Error(), nil, false)
		return
	}
	b.sendText(ctx, c, "🔗 This chat now talks to session <code>"+id+"</code>.", nil, false)
}

// sessionByPrefix resolves a session id prefix with /use's rules: at least 4
// chars, exactly one match. On failure it returns a user-facing explanation.
func (b *Bot) sessionByPrefix(prefix string) (string, string) {
	summaries, err := b.sessions.ListSessions()
	if err != nil {
		return "", "⚠️ " + err.Error()
	}
	var matches []string
	for _, s := range summaries {
		if strings.HasPrefix(s.ID, prefix) {
			matches = append(matches, s.ID)
		}
	}
	switch {
	case len(matches) == 0:
		return "", "No session matches <code>" + prefix + "</code> — see /sessions."
	case len(matches) > 1:
		return "", "Prefix is ambiguous (" + fmt.Sprint(len(matches)) + " matches) — use more characters."
	}
	return matches[0], ""
}

func (b *Bot) cmdStatus(ctx context.Context, c conv, m *models.Message) {
	var txt strings.Builder
	if run, ok := b.runs.Running(threadKey(c)); ok {
		txt.WriteString(fmt.Sprintf("🏃 Run active on <code>%s</code> for %s — /stop to cancel.\n", shortID(run.SessionID), time.Since(run.StartedAt).Round(time.Second)))
	} else {
		txt.WriteString("💤 No active run.\n")
	}
	if id := b.boundSessionID(c); id != "" {
		if sess, err := b.sessions.Store().Load(id); err == nil {
			txt.WriteString(fmt.Sprintf("💬 Session: %s\n   <code>%s</code>\n", sess.Title, sess.ID))
		}
	} else if b.inLobby(c) {
		txt.WriteString("💬 Topics mode is on — open a topic; each topic gets its own session.\n")
	} else {
		txt.WriteString("💬 No session bound yet (one is created on your first prompt).\n")
	}
	if tasks, err := hakaseagent.ListTasks(hakaseagent.ListTasksInput{Status: []hakaseagent.TaskStatus{hakaseagent.TaskStatusInProgress}}); err == nil && len(tasks) > 0 {
		txt.WriteString(fmt.Sprintf("📋 %d task(s) in progress.\n", len(tasks)))
	}
	notify := "off"
	if b.store.Get().Chats[chatKey(c.chatID)].Notify {
		notify = "on"
	}
	txt.WriteString("🔔 Notifications: " + notify)
	b.sendText(ctx, c, strings.TrimRight(txt.String(), "\n"), nil, false)
}

func (b *Bot) cmdStop(ctx context.Context, c conv) {
	if b.runs.Cancel(threadKey(c)) {
		b.sendText(ctx, c, "🛑 Stopping the run…", nil, false)
	} else {
		b.sendText(ctx, c, "Nothing is running in this chat.", nil, false)
	}
}

func (b *Bot) cmdTasks(ctx context.Context, c conv, args string) {
	b.sendText(ctx, c, channel.TasksText(args), nil, false)
}

func (b *Bot) cmdCron(ctx context.Context, c conv, args string) {
	action, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "":
		b.sendText(ctx, c, channel.CronText(), nil, false)
	case "run", "pause", "resume":
		b.sendText(ctx, c, channel.CronActionText(strings.ToLower(strings.TrimSpace(action)), rest), nil, false)
	default:
		b.sendText(ctx, c, "Usage: /cron [run|pause|resume &lt;name-or-id&gt;]", nil, false)
	}
}

func (b *Bot) cmdNotify(ctx context.Context, c conv, m *models.Message, args string) {
	ck := chatKey(c.chatID)
	enable := true
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "on":
		enable = true
	case "off":
		enable = false
	case "":
		// toggle
		enable = !b.store.Get().Chats[ck].Notify
	default:
		b.sendText(ctx, c, "Usage: /notify on|off", nil, false)
		return
	}
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		chat := s.Chats[ck]
		chat.Notify = enable
		s.Chats[ck] = chat
		return nil
	}); err != nil {
		b.sendText(ctx, c, "⚠️ "+err.Error(), nil, false)
		return
	}
	if enable {
		b.sendText(ctx, c, "🔔 Notifications on: cron/task completions and failures will be pushed here. Approvals and clarifications always arrive.", nil, false)
	} else {
		b.sendText(ctx, c, "🔕 Notifications off. (Approvals and clarifications still arrive.)", nil, false)
	}
}

func shortID(id string) string {
	if len(id) > 14 {
		return id[:14] + "…"
	}
	return id
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
