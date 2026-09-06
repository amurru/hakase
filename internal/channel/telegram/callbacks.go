package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"amurru/hakase/internal/interfaces"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// handleCallback answers inline-keyboard presses for approvals and
// clarifications, resolving the pending gate via the injected responders
// (first responder wins against the web UI).
func (b *Bot) handleCallback(ctx context.Context, cq *models.CallbackQuery) {
	answer := func(text string) {
		if _, err := b.api.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{
			CallbackQueryID: cq.ID,
			Text:            text,
		}); err != nil {
			b.log("answer callback failed: %v", err)
		}
	}

	// Callbacks must come from an allowed user (the keyboard is only ever
	// sent to allowed chats, but Telegram replays can be spoofed).
	if !b.auth.IsAllowed(cq.From.ID) {
		answer("Unauthorized.")
		return
	}

	msg := cq.Message.Message
	if msg == nil {
		answer("Message is gone.")
		return
	}
	chatID, msgID := msg.Chat.ID, msg.ID
	data := cq.Data

	switch {
	case strings.HasPrefix(data, callbackApprove):
		rest := strings.TrimPrefix(data, callbackApprove)
		gateID, sel, found := strings.Cut(rest, ":")
		if !found {
			answer("Malformed response.")
			return
		}
		approved := sel == "1"
		delivered := b.approval != nil && b.approval.RespondApproval(gateID, approved)
		if delivered {
			answer("Done.")
			b.editText(ctx, chatID, msgID, approvalOutcome(msg.Text, approved), nil)
		} else {
			answer("Already resolved or expired.")
			b.editText(ctx, chatID, msgID, msg.Text+"\n\n<i>(already resolved elsewhere)</i>", nil)
		}

	case strings.HasPrefix(data, callbackClarify):
		rest := strings.TrimPrefix(data, callbackClarify)
		gateID, sel, found := strings.Cut(rest, ":")
		if !found {
			answer("Malformed response.")
			return
		}
		if sel == "x" {
			// Free-text answer: the next message in this chat is consumed.
			b.setPendingOther(chatID, gateID)
			answer("Send your answer as the next message.")
			b.editText(ctx, chatID, msgID, msg.Text+"\n\n<i>(✍️ reply with your answer…)</i>", nil)
			return
		}
		idx, err := strconv.Atoi(sel)
		if err != nil {
			answer("Malformed choice.")
			return
		}
		choice, ok := b.takeClarifyChoice(gateID, idx)
		if !ok {
			answer("This question has expired — answer as text in the chat.")
			return
		}
		b.respondClarify(ctx, chatID, gateID, []string{choice}, msg)

	default:
		answer("Unknown action.")
	}
}

// respondClarify delivers a clarify answer and marks the prompt resolved.
func (b *Bot) respondClarify(ctx context.Context, chatID int64, gateID string, answer []string, promptMsg *models.Message) {
	delivered := b.clarify != nil && b.clarify.RespondClarify(gateID, interfaces.ClarifyResponse{Answer: answer})
	if !delivered {
		b.sendText(ctx, chatID, "That question was already resolved or expired.", nil)
		return
	}
	if promptMsg != nil {
		b.editText(ctx, chatID, promptMsg.ID, promptMsg.Text+"\n\n<i>✔ "+truncateRunes(strings.Join(answer, ", "), 200)+"</i>", nil)
	}
}

// approvalOutcome renders the approval prompt's resolved text. The prompt
// text is the original HTML body; appending the verdict keeps provenance.
func approvalOutcome(promptText string, approved bool) string {
	verdict := "✅ Approved"
	if !approved {
		verdict = "❌ Denied"
	}
	return promptText + "\n\n<b>" + verdict + "</b>"
}

// rememberClarifyChoices stores choices for later callback resolution.
func (b *Bot) rememberClarifyChoices(gateID string, choices []string) {
	b.clarifyMu.Lock()
	defer b.clarifyMu.Unlock()
	// Opportunistic GC of stale entries (prompts expire in minutes).
	for id, c := range b.clarifyCtx {
		if time.Since(c.createdAt) > 30*time.Minute {
			delete(b.clarifyCtx, id)
		}
	}
	b.clarifyCtx[gateID] = clarifyChoice{choices: choices, createdAt: time.Now()}
}

// takeClarifyChoice resolves one choice by index.
func (b *Bot) takeClarifyChoice(gateID string, idx int) (string, bool) {
	b.clarifyMu.Lock()
	defer b.clarifyMu.Unlock()
	c, ok := b.clarifyCtx[gateID]
	if !ok || idx < 0 || idx >= len(c.choices) {
		return "", false
	}
	delete(b.clarifyCtx, gateID)
	return fmt.Sprintf("%d. %s", idx+1, c.choices[idx]), true
}
