package herdr

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeBin creates an executable script that appends its arguments, one
// space-separated line per invocation, to the file at outPath.
func writeFakeBin(t *testing.T, outPath string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-herdr")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + outPath + "\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	return bin
}

func TestNewReporterOutsideHerdr(t *testing.T) {
	t.Setenv(envEnabled, "")
	t.Setenv(envBinPath, "")
	t.Setenv(envPaneID, "")
	if r := NewReporter(); r != nil {
		t.Fatalf("NewReporter should be nil outside Herdr, got %#v", r)
	}
}

func TestNewReporterMissingPane(t *testing.T) {
	t.Setenv(envEnabled, "1")
	// Env says we are inside Herdr but no pane id is advertised.
	t.Setenv(envPaneID, "")
	if r := NewReporter(); r != nil {
		t.Fatalf("NewReporter should be nil when pane id missing, got %#v", r)
	}
}

func TestResolveHerdrBin(t *testing.T) {
	t.Run("hint", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "calls.txt")
		bin := writeFakeBin(t, out)
		t.Setenv(envBinPath, bin)
		if got := resolveHerdrBin(); got != bin {
			t.Fatalf("resolveHerdrBin hint = %q, want %q", got, bin)
		}
	})

	t.Run("path-fallback", func(t *testing.T) {
		t.Setenv(envBinPath, "")
		real, err := exec.LookPath("herdr")
		if err != nil {
			t.Skip("herdr not on PATH; cannot test fallback")
		}
		if got := resolveHerdrBin(); got != real {
			t.Fatalf("resolveHerdrBin path fallback = %q, want %q", got, real)
		}
	})
}

func TestReportNoopWhenNil(t *testing.T) {
	// Must not panic or exec anything.
	var r *Reporter
	r.Report(StateWorking, "", "")
	r.Release()
}

func TestReportArgsAndSeq(t *testing.T) {
	out := filepath.Join(t.TempDir(), "calls.txt")
	bin := writeFakeBin(t, out)

	t.Setenv(envEnabled, "1")
	t.Setenv(envBinPath, bin)
	t.Setenv(envPaneID, "w1:p1")

	r := NewReporter()
	if r == nil {
		t.Fatal("NewReporter should succeed inside Herdr")
	}

	r.Report(StateIdle, "", "")
	r.Report(StateWorking, "doing things", "sess-123")

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read calls: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 calls (session + state per report), got %d: %q", len(lines), string(data))
	}

	want1 := "pane report-agent-session w1:p1 --source custom:hakase --agent hakase --seq 1"
	if lines[0] != want1 {
		t.Errorf("session call 1 =\n%q\nwant\n%q", lines[0], want1)
	}
	want2 := "pane report-agent w1:p1 --source custom:hakase --agent hakase --state idle --seq 1"
	if lines[1] != want2 {
		t.Errorf("state call 1 =\n%q\nwant\n%q", lines[1], want2)
	}
	want3 := "pane report-agent-session w1:p1 --source custom:hakase --agent hakase --seq 2 --agent-session-id sess-123"
	if lines[2] != want3 {
		t.Errorf("session call 2 =\n%q\nwant\n%q", lines[2], want3)
	}
	want4 := "pane report-agent w1:p1 --source custom:hakase --agent hakase --state working --seq 2 --message doing things --agent-session-id sess-123"
	if lines[3] != want4 {
		t.Errorf("state call 2 =\n%q\nwant\n%q", lines[3], want4)
	}
}

func TestReportSuppressesDuplicate(t *testing.T) {
	out := filepath.Join(t.TempDir(), "calls.txt")
	bin := writeFakeBin(t, out)

	t.Setenv(envEnabled, "1")
	t.Setenv(envBinPath, bin)
	t.Setenv(envPaneID, "w1:p1")

	r := NewReporter()
	r.Report(StateIdle, "", "")  // session + state
	r.Report(StateIdle, "", "")  // identical -> suppressed
	r.Report(StateIdle, "x", "") // message differs -> session + state
	r.Report(StateIdle, "x", "")

	data, _ := os.ReadFile(out)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 calls (duplicate suppressed), got %d: %q", len(lines), string(data))
	}
	if !strings.Contains(string(data), "--message x") {
		t.Errorf("message variant missing from calls: %q", string(data))
	}
}

func TestRelease(t *testing.T) {
	out := filepath.Join(t.TempDir(), "calls.txt")
	bin := writeFakeBin(t, out)

	t.Setenv(envEnabled, "1")
	t.Setenv(envBinPath, bin)
	t.Setenv(envPaneID, "w1:p1")

	r := NewReporter()
	r.Release()

	data, _ := os.ReadFile(out)
	want := "pane release-agent w1:p1 --source custom:hakase --agent hakase --seq 1"
	if strings.TrimSpace(string(data)) != want {
		t.Errorf("release call =\n%q\nwant\n%q", string(data), want)
	}
}
