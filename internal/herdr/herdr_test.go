package herdr

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if !r.flush(5 * time.Second) {
		t.Fatal("flush timed out after first report")
	}
	r.Report(StateWorking, "doing things", "sess-123")
	if !r.flush(5 * time.Second) {
		t.Fatal("flush timed out after second report")
	}

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
	r.Report(StateIdle, "", "") // session + state
	if !r.flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}
	r.Report(StateIdle, "", "")  // identical -> suppressed
	r.Report(StateIdle, "x", "") // message differs -> session + state
	r.Report(StateIdle, "x", "")
	if !r.flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}

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

// writeSlowBin creates an executable script that sleeps before appending its
// arguments, simulating a stalled Herdr daemon.
func writeSlowBin(t *testing.T, outPath string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "slow-herdr")
	script := "#!/bin/sh\nsleep 0.4\nprintf '%s\\n' \"$*\" >> \"" + outPath + "\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write slow bin: %v", err)
	}
	return bin
}

func TestReportCoalescesAndNeverBlocks(t *testing.T) {
	out := filepath.Join(t.TempDir(), "calls.txt")
	bin := writeSlowBin(t, out)

	t.Setenv(envEnabled, "1")
	t.Setenv(envBinPath, bin)
	t.Setenv(envPaneID, "w1:p1")

	r := NewReporter()

	// The first report pair is picked up by the worker and stalls on the
	// slow binary, keeping the worker busy while the reports below queue.
	r.Report(StateIdle, "", "") // seq 1
	time.Sleep(100 * time.Millisecond)

	// These pairs are queued while the worker is busy; each supersedes the
	// previous one, so only the last should survive coalescing.
	for i, s := range []string{StateWorking, StateBlocked, StateWorking} { // seq 2-4
		start := time.Now()
		r.Report(s, "", "")
		if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
			t.Fatalf("Report #%d blocked for %v; submission must never wait on the worker", i+2, elapsed)
		}
	}

	if !r.flush(10 * time.Second) {
		t.Fatal("flush timed out")
	}

	data, _ := os.ReadFile(out)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	// Exactly the mid-flight pair (seq 1) plus the coalesced final pair
	// (seq 4): superseded pending pairs were dropped.
	if len(lines) != 4 {
		t.Fatalf("expected 4 calls after coalescing, got %d: %q", len(lines), string(data))
	}
	if !strings.Contains(lines[0], "report-agent-session") || !strings.Contains(lines[0], "--seq 1") {
		t.Errorf("session command of mid-flight pair must run first, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "--state idle") || !strings.Contains(lines[1], "--seq 1") {
		t.Errorf("state command of mid-flight pair must run second, got %q", lines[1])
	}
	if !strings.Contains(lines[2], "report-agent-session") || !strings.Contains(lines[2], "--seq 4") {
		t.Errorf("session command of final pair must run third, got %q", lines[2])
	}
	if !strings.Contains(lines[3], "--state working") || !strings.Contains(lines[3], "--seq 4") {
		t.Errorf("final state command must run last with latest seq, got %q", lines[3])
	}
}

func TestReleaseWaitsForCompletion(t *testing.T) {
	out := filepath.Join(t.TempDir(), "calls.txt")
	bin := writeSlowBin(t, out)

	t.Setenv(envEnabled, "1")
	t.Setenv(envBinPath, bin)
	t.Setenv(envPaneID, "w1:p1")

	r := NewReporter()
	r.Report(StateWorking, "", "") // worker picks this up and stalls

	start := time.Now()
	r.Release()
	// The release command runs only after the mid-flight report pair's two
	// slow commands finish (>= 0.8s), so a prompt return here means Release
	// did not wait for its command.
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("Release returned after %v; it must wait for the queued release command", elapsed)
	}

	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), "release-agent") {
		t.Fatalf("release command had not run by the time Release returned: %q", string(data))
	}
}
