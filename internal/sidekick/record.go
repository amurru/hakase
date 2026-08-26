package sidekick

import (
	"strings"

	hakasesession "amurru/hakase/internal/session"
)

// Role is the persisted message role for sidekick-authored content.
// context.MessageToContent maps it to user-role model context (with
// untrusted-data wrapping) so resumed sessions treat sidekick output as
// advisory material rather than the primary assistant's own words.
const Role = "sidekick"

// TranscriptStore is the minimal session-persistence surface needed to
// record explicit sidekick interactions. *hakasesession.SessionStore
// satisfies it.
type TranscriptStore interface {
	Load(id string) (*hakasesession.Session, error)
	Save(*hakasesession.Session) error
}

// RecordQuestion appends the user's explicit /sidekick question to the
// session transcript under MessageKindSidekick. It is recorded BEFORE the
// Ask runs so an interrupted or failed ask still leaves an auditable trace;
// the answer (or its absence) is what distinguishes outcomes later. Best
// effort: any store failure is swallowed - recording must never break the
// ask itself.
func RecordQuestion(store TranscriptStore, sessionID, question string) {
	if store == nil || sessionID == "" || strings.TrimSpace(question) == "" {
		return
	}
	sess, err := store.Load(sessionID)
	if err != nil || sess == nil {
		return
	}
	sess.AddMessageWithMeta("user", question, "", 0, hakasesession.MessageKindSidekick)
	_ = store.Save(sess)
}

// RecordAnswer appends the sidekick's answer to the session transcript with
// role "sidekick" and kind MessageKindSidekick, mirroring how watchdog
// advisory notes are persisted (context.go). Call only after a successful
// Ask; failures surface over SSE instead.
func RecordAnswer(store TranscriptStore, sessionID, answer string) {
	if store == nil || sessionID == "" || strings.TrimSpace(answer) == "" {
		return
	}
	sess, err := store.Load(sessionID)
	if err != nil || sess == nil {
		return
	}
	sess.AddMessageWithMeta(Role, answer, "", 0, hakasesession.MessageKindSidekick)
	_ = store.Save(sess)
}
