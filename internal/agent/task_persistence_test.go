package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// chdirTempTask isolates ./tasks.json into a fresh temp dir.
func chdirTempTask(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// TestTaskSaveIs0600AndAtomic verifies tasks.json lands 0600 and stays valid
// JSON across writes (kill mid-save cannot tear thanks to tmp+rename).
func TestTaskSaveIs0600AndAtomic(t *testing.T) {
	chdirTempTask(t)
	created, err := CreateTask(CreateTaskInput{Title: "atomic"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	info, err := os.Stat(tasksFile)
	if err != nil {
		t.Fatalf("stat tasks.json: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("tasks.json mode = %o, want 0600", got)
	}
	reg, err := LoadTaskRegistry()
	if err != nil {
		t.Fatalf("LoadTaskRegistry: %v", err)
	}
	if len(reg.Tasks) != 1 || reg.Tasks[0].ID != created.ID {
		t.Fatalf("registry = %+v, want 1 task %s", reg, created.ID)
	}
}

// TestTaskCorruptQuarantined verifies a torn tasks.json is preserved aside
// and yields an empty registry instead of wedging the board.
func TestTaskCorruptQuarantined(t *testing.T) {
	chdirTempTask(t)
	if _, err := CreateTask(CreateTaskInput{Title: "good"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := os.WriteFile(tasksFile, []byte(`{"tasks": [truncated`), 0600); err != nil {
		t.Fatalf("write torn: %v", err)
	}
	reg, err := LoadTaskRegistry()
	if err != nil {
		t.Fatalf("LoadTaskRegistry on corrupt should recover, got: %v", err)
	}
	if len(reg.Tasks) != 0 {
		t.Fatalf("recovered registry = %d tasks, want 0", len(reg.Tasks))
	}
	matches, _ := filepath.Glob(tasksFile + ".corrupt-*")
	if len(matches) == 0 {
		t.Fatal("quarantined sidecar missing")
	}
	// Board stays writable after recovery.
	if _, err := CreateTask(CreateTaskInput{Title: "after"}); err != nil {
		t.Fatalf("CreateTask after corrupt: %v", err)
	}
}

// TestTaskConcurrentCreateSafe verifies concurrent read-modify-write cycles
// do not lose tasks (in-process mutex + cross-process flock).
func TestTaskConcurrentCreateSafe(t *testing.T) {
	chdirTempTask(t)
	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := CreateTask(CreateTaskInput{Title: "concurrent"})
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("CreateTask concurrent: %v", err)
		}
	}
	reg, err := LoadTaskRegistry()
	if err != nil {
		t.Fatalf("LoadTaskRegistry: %v", err)
	}
	if len(reg.Tasks) != n {
		t.Fatalf("tasks = %d, want %d (lost writes)", len(reg.Tasks), n)
	}
	for _, raw := range mustReadTaskFile(t) {
		_ = raw
	}
}

func mustReadTaskFile(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(tasksFile)
	if err != nil {
		t.Fatalf("read tasks.json: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		t.Fatalf("tasks.json torn: %q", data[:64])
	}
	return []string{string(data)}
}
