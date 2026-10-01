package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTrustRoundTrip(t *testing.T) {
	s := OpenTrustStore(filepath.Join(t.TempDir(), "hooks-trust.json"))
	if s.Trusted("sha256:abc") {
		t.Error("empty store must trust nothing")
	}
	e := TrustEntry{Fingerprint: "sha256:abc", Name: "g", Event: EventPreToolUse, Command: []string{"/bin/x"}}
	if err := s.Trust(e); err != nil {
		t.Fatalf("trust: %v", err)
	}
	if !s.Trusted("sha256:abc") {
		t.Error("trusted fingerprint must read back")
	}
	if got := s.List(); len(got) != 1 || got[0].Name != "g" || got[0].TrustedAt == "" {
		t.Errorf("list = %+v, want the entry with a timestamp", got)
	}
	removed, err := s.Untrust("sha256:abc")
	if err != nil || !removed {
		t.Fatalf("untrust = (%v, %v), want (true, nil)", removed, err)
	}
	if s.Trusted("sha256:abc") {
		t.Error("untrusted fingerprint must not read back")
	}
	if removed, _ := s.Untrust("sha256:abc"); removed {
		t.Error("second untrust must report nothing removed")
	}
}

func TestTrustPersistsAcrossHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks-trust.json")
	a := OpenTrustStore(path)
	if err := a.Trust(TrustEntry{Fingerprint: "sha256:one"}); err != nil {
		t.Fatal(err)
	}
	b := OpenTrustStore(path)
	if !b.Trusted("sha256:one") {
		t.Error("second handle must see the first handle's trust")
	}
}

func TestTrustReloadsExternalWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks-trust.json")
	a := OpenTrustStore(path)
	b := OpenTrustStore(path)
	if a.Trusted("sha256:ext") {
		t.Fatal("must start untrusted")
	}
	if err := b.Trust(TrustEntry{Fingerprint: "sha256:ext"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !a.Trusted("sha256:ext") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !a.Trusted("sha256:ext") {
		t.Error("mtime-changed store must reload (mid-session trust without restart)")
	}
}

func TestTrustFilePerms0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows ACLs have no Unix mode bits (Go reports 0666/0444);
		// the 0600 intent is covered by the sensitiveFilePaths deny
		// list instead. Nothing to assert here.
		t.Skip("unix permission bits do not exist on windows")
	}
	path := filepath.Join(t.TempDir(), "hooks-trust.json")
	s := OpenTrustStore(path)
	if err := s.Trust(TrustEntry{Fingerprint: "sha256:perm"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("trust store perms = %o, want 600", fi.Mode().Perm())
	}
}

func TestTrustInvalidJSONIsFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks-trust.json")
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := OpenTrustStore(path)
	if s.Trusted("sha256:anything") {
		t.Error("corrupt store must trust nothing")
	}
	if err := s.Trust(TrustEntry{Fingerprint: "sha256:fix"}); err == nil || !strings.Contains(err.Error(), "invalid trust store") {
		t.Errorf("trust over corrupt store = %v, want invalid-store error", err)
	}
}

func TestTrustEmptyPath(t *testing.T) {
	s := OpenTrustStore("")
	if s.Trusted("sha256:x") {
		t.Error("empty-path store must trust nothing")
	}
	if err := s.Trust(TrustEntry{Fingerprint: "sha256:x"}); err == nil {
		t.Error("empty-path trust must fail loudly")
	}
}

func TestTrustFindByPrefix(t *testing.T) {
	s := OpenTrustStore(filepath.Join(t.TempDir(), "hooks-trust.json"))
	for _, fp := range []string{"sha256:aaa111", "sha256:aaa222", "sha256:bbb333"} {
		if err := s.Trust(TrustEntry{Fingerprint: fp}); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.FindByPrefix("sha256:aaa"); len(got) != 2 {
		t.Errorf("prefix aaa = %d, want 2", len(got))
	}
	if got := s.FindByPrefix("sha256:bbb333"); len(got) != 1 {
		t.Errorf("full fp = %d, want 1", len(got))
	}
	if got := s.FindByPrefix("sha256:zzz"); len(got) != 0 {
		t.Errorf("no-match = %d, want 0", len(got))
	}
}
