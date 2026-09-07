package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// perChatSendInterval paces outbound messages to stay well inside Telegram's
// ~1 msg/s per-chat budget (status edits and pushes share the same limiter).
// A var so tests can zero the pacing.
var perChatSendInterval = 1100 * time.Millisecond

// callback data prefixes (Telegram caps callback_data at 64 bytes; gate IDs
// are "<prefix>_<uuid>" so the encoded forms fit).
const (
	callbackApprove = "a:" // a:<gateID>:<0|1>
	callbackClarify = "c:" // c:<gateID>:<choiceIndex|x for free text>
)

// sendText sends a text message through the pacing limiter and returns the
// sent message (nil on failure). markup may be nil. silent maps to
// DisableNotification — progress/status traffic never buzzes the phone, only
// answers and blocking prompts do. Parse errors on the HTML body fall back to
// plain text so a broken converter can never silence the bot.
func (b *Bot) sendText(ctx context.Context, c conv, text string, markup models.ReplyMarkup, silent bool) *models.Message {
	if ctx.Err() != nil {
		return nil // the run was cancelled while this send was queued
	}
	b.waitTurn(ctx, c)
	msg, err := b.api.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:              c.chatID,
		MessageThreadID:     c.threadID,
		Text:                text,
		ParseMode:           models.ParseModeHTML,
		ReplyMarkup:         markup,
		DisableNotification: silent,
		LinkPreviewOptions:  &models.LinkPreviewOptions{IsDisabled: boolPtr(true)},
	})
	if err == nil {
		return msg
	}
	// HTML parse failure: retry once as plain text.
	if isParseError(err) {
		b.waitTurn(ctx, c)
		msg, err = b.api.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:              c.chatID,
			MessageThreadID:     c.threadID,
			Text:                stripHTML(text),
			DisableNotification: silent,
			LinkPreviewOptions:  &models.LinkPreviewOptions{IsDisabled: boolPtr(true)},
		})
		if err == nil {
			return msg
		}
		b.log("send to %d/%d failed: %v", c.chatID, c.threadID, err)
		return nil
	}
	// Rate-limited: push every conversation's slot out and retry once.
	if ra := retryAfter(err); ra > 0 {
		b.paceForRetryAfter(ra)
		b.waitTurn(ctx, c)
		msg, err = b.api.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:              c.chatID,
			MessageThreadID:     c.threadID,
			Text:                text,
			ParseMode:           models.ParseModeHTML,
			ReplyMarkup:         markup,
			DisableNotification: silent,
			LinkPreviewOptions:  &models.LinkPreviewOptions{IsDisabled: boolPtr(true)},
		})
		if err == nil {
			return msg
		}
	}
	b.log("send to %d/%d failed: %v", c.chatID, c.threadID, err)
	return nil
}

// editText edits a message in place (the Telegram streaming pattern; edits
// never notify). The topic is implicit in the message being edited. Failures
// are logged, never fatal.
func (b *Bot) editText(ctx context.Context, c conv, messageID int, text string, markup models.ReplyMarkup) {
	if ctx.Err() != nil {
		return // the run was cancelled while this edit was queued
	}
	b.waitTurn(ctx, c)
	_, err := b.api.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID:             c.chatID,
		MessageID:          messageID,
		Text:               text,
		ParseMode:          models.ParseModeHTML,
		ReplyMarkup:        markup,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: boolPtr(true)},
	})
	if err != nil && isParseError(err) {
		b.waitTurn(ctx, c)
		_, err = b.api.EditMessageText(ctx, &tgbot.EditMessageTextParams{
			ChatID:    c.chatID,
			MessageID: messageID,
			Text:      stripHTML(text),
		})
	}
	// "message is not modified" means the content already matches — success
	// by definition (the final render re-issuing the last streaming edit).
	if err != nil && isNotModified(err) {
		return
	}
	if err != nil {
		b.log("edit %d/%d failed: %v", c.chatID, messageID, err)
	}
}

// Reaction emoji receipts set on the user's prompt message. 👀 processing is
// on Telegram's bot reaction allowlist; ✅/❌ are NOT (REACTION_INVALID even
// in private chats — field-verified 2026-09-06), so delivery/failure use the
// allowlisted 👍/👎.
const (
	reactionLooking = "👀" // run started
	reactionDone    = "👍" // answer delivered
	reactionFailed  = "👎" // run failed
)

// react sets the receipt reaction on a prompt message, replacing any previous
// one atomically. Best-effort: failures are logged and never surface.
func (b *Bot) react(ctx context.Context, c conv, messageID int, emoji string) {
	if messageID == 0 {
		return
	}
	_, err := b.api.SetMessageReaction(ctx, &tgbot.SetMessageReactionParams{
		ChatID:    c.chatID,
		MessageID: messageID,
		Reaction: []models.ReactionType{{
			Type:              models.ReactionTypeTypeEmoji,
			ReactionTypeEmoji: &models.ReactionTypeEmoji{Emoji: emoji},
		}},
	})
	if err != nil {
		b.log("react %d/%d with %s failed: %v", c.chatID, messageID, emoji, err)
	}
}

// pinMessage silently pins messageID (turn marker when pins is enabled).
func (b *Bot) pinMessage(ctx context.Context, c conv, messageID int) {
	if messageID == 0 {
		return
	}
	if _, err := b.api.PinChatMessage(ctx, &tgbot.PinChatMessageParams{
		ChatID:              c.chatID,
		MessageID:           messageID,
		DisableNotification: true,
	}); err != nil {
		b.log("pin %d/%d failed: %v", c.chatID, messageID, err)
	}
}

// unpinMessage silently unpins messageID. Best-effort.
func (b *Bot) unpinMessage(ctx context.Context, c conv, messageID int) {
	if messageID == 0 {
		return
	}
	if _, err := b.api.UnpinChatMessage(ctx, &tgbot.UnpinChatMessageParams{
		ChatID:    c.chatID,
		MessageID: messageID,
	}); err != nil {
		b.log("unpin %d/%d failed: %v", c.chatID, messageID, err)
	}
}

// renameTopic renames a forum topic (used when a thread binds to a session
// whose title becomes the topic name). Best-effort: bots may lack the rights
// in edge cases; the binding is already persisted, so failures only log.
func (b *Bot) renameTopic(ctx context.Context, c conv, name string) {
	if c.threadID == 0 {
		return
	}
	if _, err := b.api.EditForumTopic(ctx, &tgbot.EditForumTopicParams{
		ChatID:          c.chatID,
		MessageThreadID: c.threadID,
		Name:            name,
	}); err != nil {
		b.log("rename topic %d/%d failed: %v", c.chatID, c.threadID, err)
	}
}

// waitTurn blocks until the conversation's next send slot, sleeping out any
// 429 RetryAfter observed on the way. context cancellation aborts the wait.
func (b *Bot) waitTurn(ctx context.Context, c conv) {
	b.limiterMu.Lock()
	next := b.nextSend[c]
	now := time.Now()
	if next.IsZero() || now.After(next) {
		next = now
	}
	b.nextSend[c] = next.Add(perChatSendInterval)
	b.limiterMu.Unlock()

	// Sleep exactly to the slot reserved above. Re-deriving the wait on every
	// wake-up would re-reserve (pushing the slot another full interval) and
	// leave this and every later sender for the chat chasing the slot forever,
	// silently dropping all output.
	if wait := next.Sub(now); wait > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// paceForRetryAfter pushes every conversation's next slot out by the 429 window.
func (b *Bot) paceForRetryAfter(retryAfter time.Duration) {
	b.limiterMu.Lock()
	defer b.limiterMu.Unlock()
	for c := range b.nextSend {
		b.nextSend[c] = time.Now().Add(retryAfter)
	}
}

// isParseError reports whether err is a Telegram "can't parse entities"
// style 400 (recoverable by resending plain text).
func isParseError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "can't parse entities") ||
		strings.Contains(msg, "Unsupported start tag") ||
		strings.Contains(msg, "Unclosed tag")
}

// isNotModified reports whether err is Telegram's "message is not modified"
// 400: the edit is a no-op because the content already matches.
func isNotModified(err error) bool {
	return err != nil && strings.Contains(err.Error(), "message is not modified")
}

// retryAfter extracts the 429 RetryAfter seconds, if present.
func retryAfter(err error) time.Duration {
	var tooMany *tgbot.TooManyRequestsError
	if errors.As(err, &tooMany) && tooMany.RetryAfter > 0 {
		return time.Duration(tooMany.RetryAfter) * time.Second
	}
	return 0
}

func boolPtr(v bool) *bool { return &v }

// stripHTML removes tags for the plain-text fallback (entities were escaped
// by the converter; only the tags we emitted need stripping).
func stripHTML(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// approvalKeyboard builds the approve/deny inline keyboard.
func approvalKeyboard(gateID string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "✅ Approve", CallbackData: callbackApprove + gateID + ":1"},
			{Text: "❌ Deny", CallbackData: callbackApprove + gateID + ":0"},
		}},
	}
}

// clarifyKeyboard builds one button per choice plus a free-text escape hatch.
func clarifyKeyboard(gateID string, choices []string) models.InlineKeyboardMarkup {
	rows := make([][]models.InlineKeyboardButton, 0, len(choices)+1)
	for i, c := range choices {
		if i >= 8 {
			break
		}
		rows = append(rows, []models.InlineKeyboardButton{{
			Text:         fmt.Sprintf("%d. %s", i+1, truncateRunes(c, 48)),
			CallbackData: fmt.Sprintf("%s%s:%d", callbackClarify, gateID, i),
		}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{
		Text:         "✍️ Other…",
		CallbackData: fmt.Sprintf("%s%s:x", callbackClarify, gateID),
	}})
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
