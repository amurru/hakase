package state

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	err = store.Update(func(st *State) error {
		st.PairedUsers = append(st.PairedUsers, PairedUser{
			Channel:  "telegram",
			UserID:   42,
			Username: "tester",
			PairedAt: time.Now().UTC(),
		})
		st.Chats = map[string]Chat{"telegram:42": {SessionID: "sess_1", Notify: true}}
		return nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	// A fresh store instance (like the CLI process) must see the same state.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.Get()
	if len(got.PairedUsers) != 1 || got.PairedUsers[0].UserID != 42 {
		t.Errorf("paired users = %+v, want one user 42", got.PairedUsers)
	}
	if c := got.Chats["telegram:42"]; c.SessionID != "sess_1" || !c.Notify {
		t.Errorf("chat = %+v, want sess_1+notify", c)
	}
}

func TestUpdateRejectsMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")
	store, _ := Open(path)
	err := store.Update(func(st *State) error {
		return errNope{}
	})
	if err == nil {
		t.Fatal("expected error to abort the update")
	}
	if got := store.Get(); len(got.PairedUsers) != 0 {
		t.Errorf("rejected update still mutated state: %+v", got)
	}
}

type errNope struct{}

func (errNope) Error() string { return "nope" }

func TestGetReturnsCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")
	store, _ := Open(path)
	_ = store.Update(func(st *State) error {
		st.Chats = map[string]Chat{"telegram:1": {SessionID: "a"}}
		return nil
	})
	got := store.Get()
	got.Chats["telegram:1"] = Chat{SessionID: "mutated"}
	if c := store.Get().Chats["telegram:1"]; c.SessionID != "a" {
		t.Errorf("Get() exposed the cache to mutation: %+v", c)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")
	store, _ := Open(path)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.Update(func(st *State) error {
				st.PairedUsers = append(st.PairedUsers, PairedUser{Channel: "telegram", UserID: int64(len(st.PairedUsers))})
				return nil
			})
		}()
	}
	wg.Wait()
	if got := len(store.Get().PairedUsers); got != 20 {
		t.Errorf("paired users = %d, want 20", got)
	}
}

func TestGenerateCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		code := GenerateCode()
		if len(code) != 6 || strings.ContainsAny(code, "abcdefghijklmnopqrstuvwxyz") {
			t.Fatalf("code %q is not 6 digits", code)
		}
		seen[code] = true
	}
	if len(seen) < 45 {
		t.Errorf("suspiciously few unique codes: %d/50", len(seen))
	}
}

func TestChatKey(t *testing.T) {
	if got := ChatKey("telegram", -1001234); got != "telegram:-1001234" {
		t.Errorf("ChatKey = %q", got)
	}
}
