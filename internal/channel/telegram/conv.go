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

// effectiveConv is convFromMessage with the topics-mode gate: chats without
// topics mode have no per-thread state, so every message — threaded or not —
// belongs to the root area and the legacy chat binding keeps driving it.
func (b *Bot) effectiveConv(m *models.Message) conv {
	c := convFromMessage(m)
	if c.threadID != 0 && !b.topicsMode(c.chatID) {
		c.threadID = 0
	}
	return c
}

// topicsMode reports whether the chat runs one-topic-per-session conversations.
func (b *Bot) topicsMode(chatID int64) bool {
	return b.store.Get().Chats[chatKey(chatID)].TopicsMode
}

// inLobby reports whether a conversation is a topics-mode root area.
func (b *Bot) inLobby(c conv) bool {
	return c.threadID == 0 && b.topicsMode(c.chatID)
}

// boundSessionID returns the session id this conversation talks to: the
// thread binding in topics mode, the chat binding otherwise.
func (b *Bot) boundSessionID(c conv) string {
	if c.threadID != 0 {
		return b.store.Get().Threads[threadKey(c)].SessionID
	}
	return b.store.Get().Chats[chatKey(c.chatID)].SessionID
}

// bindThread persists a thread's session binding (topics mode).
func (b *Bot) bindThread(c conv, sessionID, title string) error {
	return b.store.Update(func(s *state.State) error {
		if s.Threads == nil {
			s.Threads = map[string]state.Thread{}
		}
		s.Threads[threadKey(c)] = state.Thread{SessionID: sessionID, Title: title}
		return nil
	})
}

// normalizeThread maps absent/root/General topic ids to the root area (0).
func normalizeThread(id int) int {
	if id <= 1 {
		return 0
	}
	return id
}
