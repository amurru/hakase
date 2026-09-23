// snapshot_test.go - snapshot store + pre-turn hook tests
// (docs/session-rewind/spec.md, issue #21).
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newSnapshotTestStore(t *testing.T, max int) (*SessionStore, *SessionService) {
	t.Helper()
	store, err := NewSessionStoreWithSnapshotLimit(filepath.Join(t.TempDir(), "sessions"), max)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	svc, err := NewSessionService(store)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	return store, svc
}

func TestSaveSnapshotRingAndOrder(t *testing.T) {
	store, _ := newSnapshotTestStore(t, 3)
	if err := store.Save(&Session{ID: "task_s1", Title: "t"}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Take 5 snapshots; only the newest 3 survive the ring.
	var lastName string
	for i := 0; i < 5; i++ {
		s := &Session{ID: "task_s1", Title: fmt.Sprintf("t%d", i)}
		s.AddMessage("user", fmt.Sprintf("msg-%d", i), "")
		name, err := store.SaveSnapshot(s, SnapshotTriggerPre)
		if err != nil {
			t.Fatalf("SaveSnapshot %d: %v", i, err)
		}
		lastName = name
		time.Sleep(2 * time.Millisecond) // distinct unix-nano names
	}

	list, err := store.ListSnapshots("task_s1")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("ring kept %d snapshots, want 3", len(list))
	}
	if list[0].Name != lastName {
		t.Errorf("newest-first order broken: first=%q want %q", list[0].Name, lastName)
	}
	want := "msg-4"
	if list[0].Preview != want {
		t.Errorf("preview %q, want %q", list[0].Preview, want)
	}
	if list[0].Messages != 1 || list[0].Trigger != SnapshotTriggerPre {
		t.Errorf("info fields wrong: %+v", list[0])
	}
}

func TestSnapshotPerms0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no POSIX permission bits; os.Chmod only toggles the
		// read-only flag, so a 0600 assertion is meaningless there.
		t.Skip("POSIX perms not applicable on windows")
	}
	st, _ := newSnapshotTestStore(t, 5)
	sess := &Session{ID: "task_perm"}
	sess.AddMessage("user", "hello", "")
	if _, err := st.SaveSnapshot(sess, SnapshotTriggerPre); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(st.sessionsDir, snapshotDirName, "task_perm", "*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("glob: %v (%d matches)", err, len(matches))
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("snapshot mode %v, want 0600", info.Mode().Perm())
	}
}

func TestSnapshotNameValidation(t *testing.T) {
	store, _ := newSnapshotTestStore(t, 5)
	if _, err := store.LoadSnapshot("task_v", "../../etc/passwd"); err == nil {
		t.Fatal("traversal name accepted")
	}
	if _, err := store.LoadSnapshot("task_v", "not-a-valid-name.json"); err == nil {
		t.Fatal("malformed name accepted")
	}
	if err := store.DeleteSnapshots("../x"); err == nil {
		t.Fatal("traversal session id accepted for delete")
	}
}

func TestSnapshotsInvisibleToSessionList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	store, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Save(&Session{ID: "task_vis"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	sess := &Session{ID: "task_vis"}
	sess.AddMessage("user", "hi", "")
	if _, err := store.SaveSnapshot(sess, SnapshotTriggerPre); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	ids, err := store.listSessionIDs()
	if err != nil {
		t.Fatalf("listSessionIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "task_vis" {
		t.Fatalf("ids %v; the .snapshots dir must be invisible to the index", ids)
	}
}

func TestDeleteSnapshots(t *testing.T) {
	store, _ := newSnapshotTestStore(t, 5)
	sess := &Session{ID: "task_del"}
	sess.AddMessage("user", "hi", "")
	if _, err := store.SaveSnapshot(sess, SnapshotTriggerPre); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := store.DeleteSnapshots("task_del"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err := store.ListSnapshots("task_del")
	if err != nil || len(list) != 0 {
		t.Fatalf("list after delete: %v (%d)", err, len(list))
	}
}

func TestPreTurnHookSnapshotsUserMessagesOnly(t *testing.T) {
	store, svc := newSnapshotTestStore(t, 10)

	if err := store.Save(&Session{ID: "task_hook"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First user message: nothing to roll back to, no snapshot.
	if err := svc.RecordUsageInSession("task_hook", "user", "q1", "", 0, nil); err != nil {
		t.Fatalf("record 1: %v", err)
	}
	snaps, _ := store.ListSnapshots("task_hook")
	if len(snaps) != 0 {
		t.Fatalf("first user turn must not snapshot, got %d", len(snaps))
	}

	// Agent reply: no snapshot.
	if err := svc.RecordUsageInSession("task_hook", "agent", "a1", "", 0, nil); err != nil {
		t.Fatalf("record 2: %v", err)
	}
	snaps, _ = store.ListSnapshots("task_hook")
	if len(snaps) != 0 {
		t.Fatalf("agent reply must not snapshot, got %d", len(snaps))
	}

	// Second user turn: exactly one pre snapshot capturing q1 only.
	if err := svc.RecordUsageInSession("task_hook", "user", "q2", "", 0, nil); err != nil {
		t.Fatalf("record 3: %v", err)
	}
	snaps, _ = store.ListSnapshots("task_hook")
	if len(snaps) != 1 || snaps[0].Trigger != SnapshotTriggerPre {
		t.Fatalf("expected 1 pre snapshot, got %+v", snaps)
	}
	restored, err := store.LoadSnapshot("task_hook", snaps[0].Name)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	// State before q2 = the q1 prompt + its agent reply.
	if len(restored.Messages) != 2 || restored.Messages[0].Content != "q1" || restored.Messages[1].Content != "a1" {
		t.Fatalf("snapshot content wrong: %d messages", len(restored.Messages))
	}

	// Disabled switch: no further snapshots.
	svc.SetSnapshotsEnabled(false)
	if err := svc.RecordUsageInSession("task_hook", "user", "q3", "", 0, nil); err != nil {
		t.Fatalf("record 4: %v", err)
	}
	snaps, _ = store.ListSnapshots("task_hook")
	if len(snaps) != 1 {
		t.Fatalf("disabled switch still snapshotted: %d", len(snaps))
	}
}

func TestDeleteSessionRemovesSnapshots(t *testing.T) {
	store, svc := newSnapshotTestStore(t, 5)
	if err := store.Save(&Session{ID: "task_gone"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.RecordUsageInSession("task_gone", "user", "q1", "", 0, nil); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := svc.RecordUsageInSession("task_gone", "user", "q2", "", 0, nil); err != nil {
		t.Fatalf("record 2: %v", err)
	}
	if err := svc.DeleteSession("task_gone"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	snaps, err := store.ListSnapshots("task_gone")
	if err != nil || len(snaps) != 0 {
		t.Fatalf("snapshots must die with the session: %v (%d)", err, len(snaps))
	}
	if _, err := os.Stat(filepath.Join(store.sessionsDir, snapshotDirName, "task_gone")); !os.IsNotExist(err) {
		t.Fatal("snapshot dir should be removed")
	}
}

func TestSnapshotNameForeignFilesIgnored(t *testing.T) {
	store, svc := newSnapshotTestStore(t, 5)
	if err := store.Save(&Session{ID: "task_f"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.RecordUsageInSession("task_f", "user", "q1", "", 0, nil); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := svc.RecordUsageInSession("task_f", "user", "q2", "", 0, nil); err != nil {
		t.Fatalf("record 2: %v", err)
	}
	// Drop a foreign file into the snapshot dir; listings must ignore it.
	foreign := filepath.Join(store.sessionsDir, snapshotDirName, "task_f", "123-not-a-trigger.json")
	if err := os.WriteFile(foreign, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed foreign: %v", err)
	}
	snaps, err := store.ListSnapshots("task_f")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("foreign file leaked into listing: %d entries", len(snaps))
	}
	if !strings.HasSuffix(snaps[0].Name, "-pre.json") {
		t.Errorf("unexpected snapshot name %q", snaps[0].Name)
	}
}
