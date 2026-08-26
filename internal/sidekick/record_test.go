package sidekick

import (
	"errors"
	"testing"

	hakasesession "amurru/hakase/internal/session"
)

// fakeStore captures Save calls and can force Load failures.
type fakeStore struct {
	sessions   map[string]*hakasesession.Session
	loadErr    error
	saveCalled int
}

func (f *fakeStore) Load(id string) (*hakasesession.Session, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	s, ok := f.sessions[id]
	if !ok {
		s = hakasesession.NewSession("t")
		f.sessions[id] = s
	}
	return s, nil
}

func (f *fakeStore) Save(sess *hakasesession.Session) error {
	f.saveCalled++
	return nil
}

func kindsOf(s *hakasesession.Session) []string {
	out := make([]string, 0, len(s.Messages))
	for _, m := range s.Messages {
		out = append(out, m.Kind)
	}
	return out
}

func TestRecordQuestionAndAnswer(t *testing.T) {
	fs := &fakeStore{sessions: map[string]*hakasesession.Session{}}

	RecordQuestion(fs, "s1", "what is X?")
	RecordAnswer(fs, "s1", "X is Y")

	s := fs.sessions["s1"]
	if s == nil {
		t.Fatal("session never created")
	}
	if got := len(s.Messages); got != 2 {
		t.Fatalf("messages = %d, want 2", got)
	}

	q := s.Messages[0]
	if q.Role != "user" || q.Kind != hakasesession.MessageKindSidekick || q.Content != "what is X?" {
		t.Fatalf("bad question record: %+v", q)
	}
	a := s.Messages[1]
	if a.Role != Role || a.Kind != hakasesession.MessageKindSidekick || a.Content != "X is Y" {
		t.Fatalf("bad answer record: %+v", a)
	}
	for i, k := range kindsOf(s) {
		if k != "sidekick" {
			t.Fatalf("message %d kind = %q, want %q", i, k, hakasesession.MessageKindSidekick)
		}
	}
	if !s.Messages[0].InContext || !s.Messages[1].InContext {
		t.Fatal("records must be in-context like watchdog notes")
	}
	if fs.saveCalled != 2 {
		t.Fatalf("save calls = %d, want 2", fs.saveCalled)
	}
}

func TestRecordSkipsEmptyInput(t *testing.T) {
	fs := &fakeStore{sessions: map[string]*hakasesession.Session{}}
	RecordQuestion(fs, "", "q")     // no session id
	RecordQuestion(fs, "s1", "   ") // blank question
	RecordAnswer(fs, "s1", "")      // blank answer
	RecordAnswer(nil, "s1", "a")    // nil store
	if fs.saveCalled != 0 {
		t.Fatalf("save calls = %d, want 0", fs.saveCalled)
	}
}

func TestRecordToleratesStoreErrors(t *testing.T) {
	fs := &fakeStore{sessions: map[string]*hakasesession.Session{}, loadErr: errors.New("disk gone")}
	RecordQuestion(fs, "s1", "q") // must not panic
	RecordAnswer(fs, "s1", "a")
	if fs.saveCalled != 0 {
		t.Fatalf("save calls = %d, want 0", fs.saveCalled)
	}
}

// Proves *hakasesession.SessionStore satisfies TranscriptStore.
var _ TranscriptStore = (*hakasesession.SessionStore)(nil)
