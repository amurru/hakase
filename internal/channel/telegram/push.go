package telegram

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
)

// esc HTML-escapes agent-derived text interpolated into HTML bodies (the
// plain-text send fallback covers anything that still slips through).
func esc(s string) string { return html.EscapeString(s) }

// Destination model (mirrors Hermes' "important vs all" tiers):
//   - approvals/clarifications go to every allowed user's DM (they block a
//     run, so they must reach the owner even with /notify off);
//   - cron/task completions go only to chats with /notify on;
//   - delegation outcomes go only to chats currently watching a run.

// promptDestinations lists the root-area conversations that should receive
// blocking prompts: paired users plus statically allowlisted IDs (DM chat id
// == user id). Gate routing may override this with the run's bound topic.
func (b *Bot) promptDestinations() []conv {
	seen := map[int64]bool{}
	var out []conv
	for _, u := range b.store.Get().PairedUsers {
		if u.Channel == ChannelName && !seen[u.UserID] {
			seen[u.UserID] = true
			out = append(out, rootConv(u.UserID))
		}
	}
	for _, id := range b.auth.AllowedIDs() {
		if !seen[id] {
			seen[id] = true
			out = append(out, rootConv(id))
		}
	}
	return out
}

// notifyDestinations lists root-area conversations with /notify on.
func (b *Bot) notifyDestinations() []conv {
	var out []conv
	prefix := ChannelName + ":"
	for key, chat := range b.store.Get().Chats {
		if !chat.Notify || !strings.HasPrefix(key, prefix) {
			continue
		}
		if id, err := strconv.ParseInt(strings.TrimPrefix(key, prefix), 10, 64); err == nil {
			out = append(out, rootConv(id))
		}
	}
	return out
}

// runningDestinations lists root-area conversations with an in-flight run in
// that chat. Run keys are thread-scoped ("telegram:<chatID>:<threadID>",
// formerly "telegram:<chatID>"), and several topics of one chat may run in
// parallel — dedupe so delegation pushes arrive once per chat.
func (b *Bot) runningDestinations() []conv {
	seen := map[int64]bool{}
	var out []conv
	prefix := ChannelName + ":"
	for _, key := range b.runs.ActiveChatKeys() {
		rest := strings.TrimPrefix(key, prefix)
		chatPart, _, _ := strings.Cut(rest, ":")
		id, err := strconv.ParseInt(chatPart, 10, 64)
		if err != nil || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, rootConv(id))
	}
	return out
}

// gateConv resolves the conversation a gate prompt should go to: the one
// whose thread (client-created or topics-mode) or root area is bound to the
// gate's session. ok is false when the session is unknown or unbound, so the
// caller falls back to the fan-out.
func (b *Bot) gateConv(sessionID string) (conv, bool) {
	if sessionID == "" {
		return conv{}, false
	}
	st := b.store.Get()
	for key, th := range st.Threads {
		if th.SessionID != sessionID {
			continue
		}
		c, ok := parseThreadKey(key)
		if !ok || c.threadID == 0 {
			continue
		}
		return c, true
	}
	for key, chat := range st.Chats {
		if chat.SessionID != sessionID {
			continue
		}
		rest, ok := strings.CutPrefix(key, ChannelName+":")
		if !ok {
			continue
		}
		if id, err := strconv.ParseInt(rest, 10, 64); err == nil {
			return rootConv(id), true
		}
	}
	return conv{}, false
}

// parseThreadKey parses "<channel>:<chatID>:<threadID>" back into a conv.
func parseThreadKey(key string) (conv, bool) {
	rest, ok := strings.CutPrefix(key, ChannelName+":")
	if !ok {
		return conv{}, false
	}
	chatStr, threadStr, found := strings.Cut(rest, ":")
	if !found {
		return conv{}, false
	}
	chatID, err := strconv.ParseInt(chatStr, 10, 64)
	if err != nil {
		return conv{}, false
	}
	threadID, err := strconv.Atoi(threadStr)
	if err != nil {
		return conv{}, false
	}
	return conv{chatID: chatID, threadID: threadID}, true
}

// ApprovalPrompt implements channel.PushHandler: the prompt goes to the
// conversation that started the run (notification on — it blocks), with the
// paired-users fan-out as the fallback for unknown sessions.
func (b *Bot) ApprovalPrompt(sessionID, id, tool, risk, reason, command string) {
	kb := approvalKeyboard(id)
	var txt strings.Builder
	txt.WriteString("🔒 <b>Approval needed</b>\n")
	fmt.Fprintf(&txt, "tool: <code>%s</code>", esc(tool))
	if risk != "" {
		fmt.Fprintf(&txt, " · risk: %s", esc(risk))
	}
	txt.WriteString("\n")
	if command != "" {
		txt.WriteString("<pre>" + esc(truncateRunes(command, 512)) + "</pre>\n")
	}
	if reason != "" {
		txt.WriteString(esc(truncateRunes(reason, 512)) + "\n")
	}
	body := strings.TrimRight(txt.String(), "\n")
	if c, ok := b.gateConv(sessionID); ok {
		b.sendText(context.Background(), c, body, kb, false)
		return
	}
	for _, c := range b.promptDestinations() {
		b.sendText(context.Background(), c, body, kb, false)
	}
}

// ClarifyPrompt implements channel.PushHandler; routing mirrors ApprovalPrompt.
func (b *Bot) ClarifyPrompt(sessionID, id, question string, choices []string, multiSelect bool) {
	if len(choices) > 0 {
		b.rememberClarifyChoices(id, choices)
	}
	var txt strings.Builder
	txt.WriteString("❓ <b>Question</b>\n")
	txt.WriteString(esc(truncateRunes(question, 1500)))
	kb := clarifyKeyboard(id, choices)
	body := strings.TrimRight(txt.String(), "\n")
	if c, ok := b.gateConv(sessionID); ok {
		b.sendText(context.Background(), c, body, kb, false)
		return
	}
	for _, c := range b.promptDestinations() {
		b.sendText(context.Background(), c, body, kb, false)
	}
}

// CronEvent implements channel.PushHandler.
func (b *Bot) CronEvent(status, jobID, name, summary, outputPath string) {
	icon := map[string]string{"completed": "✅", "failed": "❌", "silent": "🔇"}[status]
	if icon == "" {
		return
	}
	label := name
	if label == "" {
		label = jobID
	}
	body := fmt.Sprintf("%s cron <b>%s</b> %s", icon, esc(label), status)
	if summary != "" {
		body += "\n" + esc(truncateRunes(summary, 800))
	}
	if outputPath != "" {
		body += "\n<code>" + outputPath + "</code>"
	}
	for _, c := range b.notifyDestinations() {
		b.sendText(context.Background(), c, body, nil, false)
	}
}

// TaskEvent implements channel.PushHandler.
func (b *Bot) TaskEvent(action string, id, title, status string) {
	if action != "completed" && action != "failed" {
		return
	}
	icon := "✅"
	if action == "failed" {
		icon = "❌"
	}
	body := fmt.Sprintf("%s task <b>%s</b> is now %s\n<code>%s</code>", icon, esc(title), status, id)
	for _, c := range b.notifyDestinations() {
		b.sendText(context.Background(), c, body, nil, false)
	}
}

// DelegationEvent implements channel.PushHandler: finished delegations are
// only interesting to chats already watching a run.
func (b *Bot) DelegationEvent(status, taskID, agent, message string) {
	chats := b.runningDestinations()
	if len(chats) == 0 {
		return
	}
	icon := "📦"
	switch status {
	case "failed":
		icon = "❌"
	case "timed_out":
		icon = "⏱️"
	}
	body := fmt.Sprintf("%s sub-agent <b>%s</b> %s", icon, esc(agent), status)
	if message != "" {
		body += "\n" + esc(truncateRunes(message, 400))
	}
	for _, c := range chats {
		b.sendText(context.Background(), c, body, nil, false)
	}
}
