// push.go - PushHandler: gate prompts, cron/task/delegation events to DMs.
package discord

import (
	"context"
	"fmt"
	"strings"
)

// pairedUsers returns every DM-able user: static allowlist + paired users.
func (b *Bot) pairedUsers() []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range b.auth.AllowedIDs() {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, u := range b.store.Get().PairedUsers {
		if u.Channel == ChannelName && !seen[u.UserID] {
			seen[u.UserID] = true
			out = append(out, u.UserID)
		}
	}
	return out
}

// chatUserID parses a "discord:<id>" chat key back to its user.
func chatUserID(key string) (int64, bool) {
	rest, ok := strings.CutPrefix(key, ChannelName+":")
	if !ok || strings.Contains(rest, ":") {
		return 0, false // threads or foreign transports
	}
	id, err := parseSnowflake(rest)
	if err != nil {
		return 0, false
	}
	return id, true
}

// gateUser routes a gate prompt to the DM that started the run (via the
// session binding); empty when the origin is unknown.
func (b *Bot) gateUser(sessionID string) (int64, bool) {
	if sessionID == "" {
		return 0, false
	}
	for key, ch := range b.store.Get().Chats {
		if ch.SessionID != sessionID {
			continue
		}
		if id, ok := chatUserID(key); ok {
			return id, true
		}
	}
	return 0, false
}

// notifyUsers returns chats with Notify on.
func (b *Bot) notifyUsers() []int64 {
	var out []int64
	for key, ch := range b.store.Get().Chats {
		if !ch.Notify {
			continue
		}
		if id, ok := chatUserID(key); ok {
			out = append(out, id)
		}
	}
	return out
}

// runningUsers returns users with an active run.
func (b *Bot) runningUsers() []int64 {
	var out []int64
	for _, u := range b.pairedUsers() {
		if _, ok := b.runs.Running(runKey(u)); ok {
			out = append(out, u)
		}
	}
	return out
}

// ApprovalPrompt implements channel.PushHandler.
func (b *Bot) ApprovalPrompt(sessionID, id, tool, risk, reason, command string) {
	text := fmt.Sprintf("Approve `%s` (%s)?\n%s\n```\n%s\n```", tool, risk, reason, command)
	buttons := []Button{
		{Label: "Approve", Style: "primary", CustomID: "dc:a:" + id + ":1"},
		{Label: "Deny", Style: "danger", CustomID: "dc:a:" + id + ":0"},
	}
	if userID, ok := b.gateUser(sessionID); ok {
		b.sendButtons(userID, text, buttons)
		return
	}
	for _, userID := range b.pairedUsers() {
		b.sendButtons(userID, text, buttons)
	}
}

// ClarifyPrompt implements channel.PushHandler.
func (b *Bot) ClarifyPrompt(sessionID, id, question string, choices []string, multiSelect bool) {
	b.rememberClarifyChoices(id, choices)
	var buttons []Button
	for i, c := range choices {
		if i >= 24 {
			break // one row kept for Other; 5x5 grid max
		}
		label := c
		if len([]rune(label)) > 80 {
			label = string([]rune(label)[:77]) + "…"
		}
		buttons = append(buttons, Button{Label: fmt.Sprintf("%d. %s", i+1, label), Style: "secondary", CustomID: fmt.Sprintf("dc:c:%s:%d", id, i)})
	}
	if !multiSelect {
		buttons = append(buttons, Button{Label: "Other (reply as text)", Style: "secondary", CustomID: "dc:c:" + id + ":other"})
	}
	text := question
	if multiSelect {
		text += "\n\n(Multi-select is not supported over buttons — reply with your answers as text.)"
	}
	if userID, ok := b.gateUser(sessionID); ok {
		if multiSelect {
			b.setPendingOther(userID, id)
		}
		b.sendButtons(userID, text, buttons)
		return
	}
	for _, userID := range b.pairedUsers() {
		if multiSelect {
			b.setPendingOther(userID, id)
		}
		b.sendButtons(userID, text, buttons)
	}
}

// CronEvent implements channel.PushHandler.
func (b *Bot) CronEvent(status, jobID, name, summary, outputPath string) {
	text := fmt.Sprintf("Cron `%s` %s: %s", name, status, summary)
	if outputPath != "" {
		text += "\n" + outputPath
	}
	for _, userID := range b.notifyUsers() {
		b.sendText(context.Background(), userID, text)
	}
}

// TaskEvent implements channel.PushHandler.
func (b *Bot) TaskEvent(action, id, title, status string) {
	text := fmt.Sprintf("Task %s `%s` (%s): %s", action, id, title, status)
	for _, userID := range b.notifyUsers() {
		b.sendText(context.Background(), userID, text)
	}
}

// DelegationEvent implements channel.PushHandler.
func (b *Bot) DelegationEvent(status, taskID, agent, message string) {
	text := fmt.Sprintf("Delegation %s `%s` (%s): %s", status, taskID, agent, message)
	for _, userID := range b.runningUsers() {
		b.sendText(context.Background(), userID, text)
	}
}

// sendButtons posts text with buttons to a user's DM.
func (b *Bot) sendButtons(userID int64, text string, buttons []Button) {
	ch, err := b.openDM(userID)
	if err != nil {
		b.log("open DM for %d: %v", userID, err)
		return
	}
	if _, err := b.sender.SendButtons(ch, text, buttons); err != nil {
		b.log("send buttons to %d: %v", userID, err)
	}
}
