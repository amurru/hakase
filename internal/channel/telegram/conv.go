package telegram

import (
	"amurru/hakase/internal/channel/state"

	"github.com/go-telegram/bot/models"
)

// conv is one conversation: a chat plus its forum topic. threadID 0 is the
// DM's root area — also the legacy path when topics mode is off. Every
// chat-scoped runtime map (pacing, pending clarifies) and the RunManager are
// keyed by conv, so parallel runs across topics of one chat fall out
// naturally.
type conv struct {
	chatID   int64
	threadID int
}

// rootConv returns the conversation for a chat's root area.
func rootConv(chatID int64) conv { return conv{chatID: chatID} }

// chatKey returns the chat-scoped state key (state.Chats bindings).
func chatKey(chatID int64) string { return state.ChatKey(ChannelName, chatID) }

// threadKey returns the conversation-scoped key (state.Threads bindings and
// per-conversation runtime state like active runs).
func threadKey(c conv) string { return state.ThreadKey(ChannelName, c.chatID, c.threadID) }

// convFromMessage derives the conversation of an inbound message. Absent ids,
// zero, and the General topic (whose thread id is always 1) map to the root
// area, so legacy clients that never send message_thread_id keep working.
func convFromMessage(m *models.Message) conv {
	return conv{chatID: m.Chat.ID, threadID: normalizeThread(m.MessageThreadID)}
}

// normalizeThread maps absent/root/General topic ids to the root area (0).
func normalizeThread(id int) int {
	if id <= 1 {
		return 0
	}
	return id
}
