package util_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"amurru/hakase/internal/util"
)

// TestPruneDebugLogsBoundsFiles verifies Init-time pruning keeps the newest
// N files and drops aged ones.
func TestPruneDebugLogsBoundsFiles(t *testing.T) {
	oldDir, oldMax, oldAge := util.DebugLogDir, util.DebugLogMaxFiles, util.DebugLogMaxAge
	dir := t.TempDir()
	util.DebugLogDir = dir
	util.DebugLogMaxFiles = 3
	util.DebugLogMaxAge = 0 // count-only for this test
	t.Cleanup(func() { util.DebugLogDir = oldDir; util.DebugLogMaxFiles = oldMax; util.DebugLogMaxAge = oldAge })

	// 6 stale files with increasing mtimes.
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, "hakase-debug-2020010"+string(rune('0'+i))+"-000000.jsonl")
		if err := os.WriteFile(p, []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(time.Duration(i) * time.Hour)
		_ = os.Chtimes(p, mt, mt)
	}
	got := util.InitDebugLogging(true)
	if got == "" {
		t.Fatal("InitDebugLogging returned empty")
	}
	util.CloseDebugLogging()
	entries, _ := os.ReadDir(dir)
	count := 0
	for _, e := range entries {
		if len(e.Name()) > 13 && e.Name()[:13] == "hakase-debug-" {
			count++
		}
	}
	if count > util.DebugLogMaxFiles+1 { // +1 for the just-opened file (excluded from prune)
		t.Fatalf("debug files = %d, want <= %d", count, util.DebugLogMaxFiles+1)
	}
}

// TestPruneDebugLogsDropsAged verifies age-based pruning.
func TestPruneDebugLogsDropsAged(t *testing.T) {
	oldDir, oldMax, oldAge := util.DebugLogDir, util.DebugLogMaxFiles, util.DebugLogMaxAge
	dir := t.TempDir()
	util.DebugLogDir = dir
	util.DebugLogMaxFiles = 100
	util.DebugLogMaxAge = 24 * time.Hour
	t.Cleanup(func() { util.DebugLogDir = oldDir; util.DebugLogMaxFiles = oldMax; util.DebugLogMaxAge = oldAge })

	old := filepath.Join(dir, "hakase-debug-20200101-000000.jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ancient := time.Now().Add(-72 * time.Hour)
	_ = os.Chtimes(old, ancient, ancient)

	got := util.InitDebugLogging(true)
	if got == "" {
		t.Fatal("InitDebugLogging returned empty")
	}
	util.CloseDebugLogging()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("aged debug file should be pruned")
	}
}
