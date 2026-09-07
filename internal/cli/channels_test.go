package cli

import (
	"strings"
	"testing"

	"amurru/hakase/internal/channel/state"
)

// TestChannelsCLI exercises pair-code/status/revoke against a redirected
// HAKASE_HOME (mirrors the isolateHome convention).
func TestChannelsCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	store, err := state.OpenDefault()
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// pair-code generates and prints a code.
	code := captureStdout(t, func() {
		if rc := RunChannelsCLI([]string{"pair-code"}); rc != 0 {
			t.Fatalf("pair-code rc = %d", rc)
		}
	})
	if !strings.Contains(code, "Pairing code:") {
		t.Fatalf("pair-code output = %q", code)
	}
	var printed string
	for _, f := range strings.Fields(code) {
		if len(f) == 6 && strings.ContainsAny(f, "0123456789") && !strings.Contains(f, ":") {
			printed = f
			break
		}
	}
	if printed == "" {
		t.Fatalf("no 6-digit code in output: %q", code)
	}

	// Simulate a pairing done by the server process.
	err = store.Update(func(st *state.State) error {
		st.PairedUsers = append(st.PairedUsers, state.PairedUser{Channel: "telegram", UserID: 4242, Username: "tester"})
		return nil
	})
	if err != nil {
		t.Fatalf("seed pairing: %v", err)
	}

	status := captureStdout(t, func() {
		if rc := RunChannelsCLI([]string{"status"}); rc != 0 {
			t.Fatalf("status rc = %d", rc)
		}
	})
	if !strings.Contains(status, "telegram:4242") {
		t.Fatalf("status output missing user: %q", status)
	}

	if rc := RunChannelsCLI([]string{"revoke", "4242"}); rc != 0 {
		t.Fatalf("revoke rc = %d", rc)
	}
	// The CLI mutated the file through its own store instance; reopen to
	// verify the on-disk state.
	reopened, err := state.OpenDefault()
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.Get().PairedUsers; len(got) != 0 {
		t.Fatalf("paired users after revoke: %+v", got)
	}

	if rc := RunChannelsCLI([]string{"revoke", "9999"}); rc != 1 {
		t.Fatalf("revoking unknown user rc = %d, want 1", rc)
	}
	if rc := RunChannelsCLI([]string{"bogus"}); rc != 2 {
		t.Fatalf("unknown subcommand rc = %d, want 2", rc)
	}
}
