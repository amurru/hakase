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
func (b *Bot) handleCommand(ctx context.Context, m *models.Message) {
	first := strings.Fields(m.Text)[0]
	cmd := strings.TrimPrefix(first, "/")
	if i := strings.IndexByte(cmd, '@'); i > 0 {
		cmd = cmd[:i]
	}
	args := strings.TrimSpace(strings.TrimPrefix(m.Text, first))

	// Pairing gate: /start is the only auth path; /id is harmless.
	if cmd != "start" && cmd != "id" && !b.auth.IsAllowed(m.From.ID) {
		if b.auth.DenyReplyAllowed(m.From.ID) {
			b.sendText(ctx, m.Chat.ID, "🔒 Unauthorized. Pair with <code>/start &lt;code&gt;</code> — the code is printed on the server console.", nil)
		}
		return
	}

	switch cmd {
	case "start":
		b.cmdStart(ctx, m, args)
	case "help":
		b.cmdHelp(ctx, m)
	case "id":
		b.cmdID(ctx, m)
	case "new":
		b.cmdNew(ctx, m, args)
	case "sessions":
		b.cmdSessions(ctx, m)
	case "use":
		b.cmdUse(ctx, m, args)
	case "status":
		b.cmdStatus(ctx, m)
	case "stop":
		b.cmdStop(ctx, m)
	case "tasks":
		b.cmdTasks(ctx, m, args)
	case "cron":
		b.cmdCron(ctx, m, args)
	case "notify":
		b.cmdNotify(ctx, m, args)
	default:
		b.sendText(ctx, m.Chat.ID, "Unknown command — /help lists what I can do.", nil)
	}
}

func (b *Bot) cmdStart(ctx context.Context, m *models.Message, args string) {
	if b.auth.IsAllowed(m.From.ID) {
		b.sendText(ctx, m.Chat.ID, greeting, nil)
		return
	}
	if strings.TrimSpace(args) == "" {
		b.sendText(ctx, m.Chat.ID, "Send <code>/start &lt;code&gt;</code> with the pairing code from the server console to pair this account.", nil)
		return
	}
	if err := b.auth.TryPair(m.From.ID, m.From.Username, args); err != nil {
		b.sendText(ctx, m.Chat.ID, "❌ "+err.Error()+" — generate a fresh one with <code>hakase channels pair-code</code> on the server.", nil)
		return
	}
	b.sendText(ctx, m.Chat.ID, "✅ Paired! You can now talk to hakase from here.\n\n"+greeting, nil)
}

const greeting = "I'm hakase, your agent. Send me a prompt (or a photo with a caption) and I'll run it with live progress. " +
	"Approvals and questions arrive as buttons here. /help lists the commands."

func (b *Bot) cmdHelp(ctx context.Context, m *models.Message) {
	b.sendText(ctx, m.Chat.ID, helpText, nil)
}

const helpText = `<b>hakase over Telegram</b>

Send any text as a prompt for the current session; a photo with a caption sends the image to the agent. While a run is active you'll see one live status message; <code>/stop</code> cancels.

<b>Commands</b>
/new [title] — start a fresh session
/sessions — list recent sessions
/use &lt;id-prefix&gt; — switch this chat to a session
/status — current session, run state, open tasks
/tasks [filter] — task board ("open", "completed", …)
/cron — list jobs; /cron run|pause|resume &lt;name&gt;
/stop — cancel the running turn
/notify on|off — push notifications for cron/task completions
/id — show your Telegram ids
/start &lt;code&gt; — pair this account`

func (b *Bot) cmdID(ctx context.Context, m *models.Message) {
	b.sendText(ctx, m.Chat.ID, fmt.Sprintf("user id: <code>%d</code>\nchat id: <code>%d</code>", m.From.ID, m.Chat.ID), nil)
}

func (b *Bot) cmdNew(ctx context.Context, m *models.Message, args string) {
	ck := chatKey(m.Chat.ID)
	if _, running := b.runs.Running(ck); running {
		b.sendText(ctx, m.Chat.ID, "⏳ A run is active — /stop it before switching sessions.", nil)
		return
	}
	title := strings.TrimSpace(args)
	if title == "" {
		title = "Telegram · " + time.Now().Format("Jan 2 15:04")
	}
	sess, err := b.sessions.CreateSession(title)
	if err != nil {
		b.sendText(ctx, m.Chat.ID, "⚠️ "+err.Error(), nil)
		return
	}
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		s.Chats[ck] = state.Chat{SessionID: sess.ID}
		return nil
	}); err != nil {
		b.log("telegram: cannot persist chat binding: %v", err)
	}
	b.sendText(ctx, m.Chat.ID, fmt.Sprintf("🆕 Session <b>%s</b> created and bound here.\n<code>%s</code>", sess.Title, sess.ID), nil)
}

func (b *Bot) cmdSessions(ctx context.Context, m *models.Message) {
	summaries, err := b.sessions.ListSessions()
	if err != nil {
		b.sendText(ctx, m.Chat.ID, "⚠️ "+err.Error(), nil)
		return
	}
	ck := chatKey(m.Chat.ID)
	bound := b.store.Get().Chats[ck].SessionID
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
	b.sendText(ctx, m.Chat.ID, strings.TrimRight(txt.String(), "\n"), nil)
}

func (b *Bot) cmdUse(ctx context.Context, m *models.Message, args string) {
	prefix := strings.TrimSpace(args)
	if len(prefix) < 4 {
		b.sendText(ctx, m.Chat.ID, "Usage: /use &lt;id-prefix&gt; (at least 4 chars — see /sessions)", nil)
		return
	}
	if _, running := b.runs.Running(chatKey(m.Chat.ID)); running {
		b.sendText(ctx, m.Chat.ID, "⏳ A run is active — /stop it before switching sessions.", nil)
		return
	}
	summaries, err := b.sessions.ListSessions()
	if err != nil {
		b.sendText(ctx, m.Chat.ID, "⚠️ "+err.Error(), nil)
		return
	}
	var matches []string
	for _, s := range summaries {
		if strings.HasPrefix(s.ID, prefix) {
			matches = append(matches, s.ID)
		}
	}
	switch {
	case len(matches) == 0:
		b.sendText(ctx, m.Chat.ID, "No session matches <code>"+prefix+"</code> — see /sessions.", nil)
	case len(matches) > 1:
		b.sendText(ctx, m.Chat.ID, "Prefix is ambiguous ("+fmt.Sprint(len(matches))+" matches) — use more characters.", nil)
	default:
		id := matches[0]
		if err := b.store.Update(func(s *state.State) error {
			if s.Chats == nil {
				s.Chats = map[string]state.Chat{}
			}
			chat := s.Chats[chatKey(m.Chat.ID)]
			chat.SessionID = id
			s.Chats[chatKey(m.Chat.ID)] = chat
			return nil
		}); err != nil {
			b.sendText(ctx, m.Chat.ID, "⚠️ "+err.Error(), nil)
			return
		}
		b.sendText(ctx, m.Chat.ID, "🔗 This chat now talks to session <code>"+id+"</code>.", nil)
	}
}

func (b *Bot) cmdStatus(ctx context.Context, m *models.Message) {
	ck := chatKey(m.Chat.ID)
	var txt strings.Builder
	if run, ok := b.runs.Running(ck); ok {
		txt.WriteString(fmt.Sprintf("🏃 Run active on <code>%s</code> for %s — /stop to cancel.\n", shortID(run.SessionID), time.Since(run.StartedAt).Round(time.Second)))
	} else {
		txt.WriteString("💤 No active run.\n")
	}
	if chat := b.store.Get().Chats[ck]; chat.SessionID != "" {
		if sess, err := b.sessions.Store().Load(chat.SessionID); err == nil {
			txt.WriteString(fmt.Sprintf("💬 Session: %s\n   <code>%s</code>\n", sess.Title, sess.ID))
		}
	} else {
		txt.WriteString("💬 No session bound yet (one is created on your first prompt).\n")
	}
	if tasks, err := hakaseagent.ListTasks(hakaseagent.ListTasksInput{Status: []hakaseagent.TaskStatus{hakaseagent.TaskStatusInProgress}}); err == nil && len(tasks) > 0 {
		txt.WriteString(fmt.Sprintf("📋 %d task(s) in progress.\n", len(tasks)))
	}
	notify := "off"
	if b.store.Get().Chats[ck].Notify {
		notify = "on"
	}
	txt.WriteString("🔔 Notifications: " + notify)
	b.sendText(ctx, m.Chat.ID, strings.TrimRight(txt.String(), "\n"), nil)
}

func (b *Bot) cmdStop(ctx context.Context, m *models.Message) {
	if b.runs.Cancel(chatKey(m.Chat.ID)) {
		b.sendText(ctx, m.Chat.ID, "🛑 Stopping the run…", nil)
	} else {
		b.sendText(ctx, m.Chat.ID, "Nothing is running in this chat.", nil)
	}
}

func (b *Bot) cmdTasks(ctx context.Context, m *models.Message, args string) {
	b.sendText(ctx, m.Chat.ID, channel.TasksText(args), nil)
}

func (b *Bot) cmdCron(ctx context.Context, m *models.Message, args string) {
	action, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "":
		b.sendText(ctx, m.Chat.ID, channel.CronText(), nil)
	case "run", "pause", "resume":
		b.sendText(ctx, m.Chat.ID, channel.CronActionText(strings.ToLower(strings.TrimSpace(action)), rest), nil)
	default:
		b.sendText(ctx, m.Chat.ID, "Usage: /cron [run|pause|resume &lt;name-or-id&gt;]", nil)
	}
}

func (b *Bot) cmdNotify(ctx context.Context, m *models.Message, args string) {
	ck := chatKey(m.Chat.ID)
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
		b.sendText(ctx, m.Chat.ID, "Usage: /notify on|off", nil)
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
		b.sendText(ctx, m.Chat.ID, "⚠️ "+err.Error(), nil)
		return
	}
	if enable {
		b.sendText(ctx, m.Chat.ID, "🔔 Notifications on: cron/task completions and failures will be pushed here. Approvals and clarifications always arrive.", nil)
	} else {
		b.sendText(ctx, m.Chat.ID, "🔕 Notifications off. (Approvals and clarifications still arrive.)", nil)
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
