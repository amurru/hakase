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
// sent message (nil on failure). markup may be nil. Parse errors on the HTML
// body fall back to plain text so a broken converter can never silence the
// bot.
func (b *Bot) sendText(ctx context.Context, chatID int64, text string, markup models.ReplyMarkup) *models.Message {
	b.waitTurn(ctx, chatID)
	msg, err := b.api.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:             chatID,
		Text:               text,
		ParseMode:          models.ParseModeHTML,
		ReplyMarkup:        markup,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: boolPtr(true)},
	})
	if err == nil {
		return msg
	}
	// HTML parse failure: retry once as plain text.
	if isParseError(err) {
		b.waitTurn(ctx, chatID)
		msg, err = b.api.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:             chatID,
			Text:               stripHTML(text),
			LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: boolPtr(true)},
		})
		if err == nil {
			return msg
		}
		b.log("send to %d failed: %v", chatID, err)
		return nil
	}
	// Rate-limited: push every chat's slot out and retry once.
	if ra := retryAfter(err); ra > 0 {
		b.paceForRetryAfter(ra)
		b.waitTurn(ctx, chatID)
		msg, err = b.api.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:             chatID,
			Text:               text,
			ParseMode:          models.ParseModeHTML,
			ReplyMarkup:        markup,
			LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: boolPtr(true)},
		})
		if err == nil {
			return msg
		}
	}
	b.log("send to %d failed: %v", chatID, err)
	return nil
}

// editText edits the status/prompt message in place (the Telegram streaming
// pattern). Failures are logged, never fatal.
func (b *Bot) editText(ctx context.Context, chatID int64, messageID int, text string, markup models.ReplyMarkup) {
	b.waitTurn(ctx, chatID)
	_, err := b.api.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID:             chatID,
		MessageID:          messageID,
		Text:               text,
		ParseMode:          models.ParseModeHTML,
		ReplyMarkup:        markup,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: boolPtr(true)},
	})
	if err != nil && isParseError(err) {
		b.waitTurn(ctx, chatID)
		_, err = b.api.EditMessageText(ctx, &tgbot.EditMessageTextParams{
			ChatID:    chatID,
			MessageID: messageID,
			Text:      stripHTML(text),
		})
	}
	if err != nil {
		b.log("edit %d/%d failed: %v", chatID, messageID, err)
	}
}

// waitTurn blocks until the chat's next send slot, sleeping out any 429
// RetryAfter observed on the way. context cancellation aborts the wait.
func (b *Bot) waitTurn(ctx context.Context, chatID int64) {
	for {
		b.limiterMu.Lock()
		next := b.nextSend[chatID]
		now := time.Now()
		if next.IsZero() || now.After(next) {
			b.nextSend[chatID] = now.Add(perChatSendInterval)
			b.limiterMu.Unlock()
			return
		}
		wait := next.Sub(now)
		b.nextSend[chatID] = next.Add(perChatSendInterval)
		b.limiterMu.Unlock()

		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// paceForRetryAfter pushes every chat's next slot out by the 429 window.
func (b *Bot) paceForRetryAfter(retryAfter time.Duration) {
	b.limiterMu.Lock()
	defer b.limiterMu.Unlock()
	for id := range b.nextSend {
		b.nextSend[id] = time.Now().Add(retryAfter)
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
