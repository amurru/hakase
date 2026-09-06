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

// ApprovalPrompt implements channel.PushHandler.
func (b *Bot) ApprovalPrompt(id, tool, risk, reason, command string) {
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
	for _, c := range b.promptDestinations() {
		b.sendText(context.Background(), c, body, kb, false)
	}
}

// ClarifyPrompt implements channel.PushHandler.
func (b *Bot) ClarifyPrompt(id, question string, choices []string, multiSelect bool) {
	if len(choices) > 0 {
		b.rememberClarifyChoices(id, choices)
	}
	var txt strings.Builder
	txt.WriteString("❓ <b>Question</b>\n")
	txt.WriteString(esc(truncateRunes(question, 1500)))
	kb := clarifyKeyboard(id, choices)
	for _, c := range b.promptDestinations() {
		b.sendText(context.Background(), c, strings.TrimRight(txt.String(), "\n"), kb, false)
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
