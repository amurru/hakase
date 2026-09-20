package sandbox

import (
	"errors"
	"strings"
	"testing"
)

// forceBwrapMissing simulates a machine without bubblewrap by poisoning the
// bwrapPath cache. Restores the prior cache state on cleanup so tests stay
// hermetic whether or not bwrap is installed on the dev machine.
func forceBwrapMissing(t *testing.T) {
	t.Helper()
	savedPath, savedErr := bwrapCachedPath, bwrapCachedErr
	bwrapCachedPath = ""
	bwrapCachedErr = errors.New("forced missing for test: bubblewrap not found on PATH")
	t.Cleanup(func() {
		bwrapCachedPath, bwrapCachedErr = savedPath, savedErr
	})
}

// TestValidateSandboxConfigRefusesLandlock pins issue #14's core guarantee:
// landlock mode is never enforced, so it must be refused at config load.
func TestValidateSandboxConfigRefusesLandlock(t *testing.T) {
	if err := ValidateSandboxConfig(nil); err != nil {
		t.Errorf("ValidateSandboxConfig(nil) = %v, want nil", err)
	}
	for _, mode := range []SandboxMode{SandboxModeOff, SandboxModePaths, SandboxModeBubblewrap} {
		sb := &SandboxConfig{Mode: mode}
		if err := ValidateSandboxConfig(sb); err != nil {
			t.Errorf("ValidateSandboxConfig(%q) = %v, want nil", mode, err)
		}
	}
	sb := LoadSandboxConfig(&SandboxJSON{Mode: "landlock"})
	if sb.Mode != SandboxModeLandlock {
		t.Fatalf("LoadSandboxConfig(landlock).Mode = %q, want landlock preserved for refusal", sb.Mode)
	}
	if err := ValidateSandboxConfig(sb); !errors.Is(err, ErrLandlockNotImplemented) {
		t.Fatalf("ValidateSandboxConfig(landlock) = %v, want ErrLandlockNotImplemented", err)
	}
}

// TestBuildExecCommandRefusesLandlock verifies the runtime backstop: even a
// directly-constructed landlock config (bypassing LoadConfig validation) fails
// closed instead of silently degrading to path-auditing-only exec.
func TestBuildExecCommandRefusesLandlock(t *testing.T) {
	savedGate := EvaluateCommandFunc
	EvaluateCommandFunc = func(sb *SandboxConfig, command string, args []string) GateDecision {
		return GateDecision{Action: ActionAllow, Risk: RiskLow}
	}
	t.Cleanup(func() { EvaluateCommandFunc = savedGate })

	sb := &SandboxConfig{
		Mode:           SandboxModeLandlock,
		WorkspaceRoots: []string{t.TempDir()},
		ReadRoots:      []string{t.TempDir()},
	}
	saved := CurrentSandbox
	CurrentSandbox = sb
	t.Cleanup(func() { CurrentSandbox = saved })

	if _, err := BuildExecCommand("echo hi", nil, "", nil); !errors.Is(err, ErrLandlockNotImplemented) {
		t.Fatalf("BuildExecCommand(landlock) = %v, want ErrLandlockNotImplemented", err)
	}
}

// TestBuildExecCommandBwrapFallbackAuditsAndNotifies verifies the fail-loud
// half of issue #14: with AllowFallback=true and bwrap missing, the degraded
// exec emits an audit entry (Decision sandbox_fallback) and calls the UI
// notice hook - never debug-logs only.
func TestBuildExecCommandBwrapFallbackAuditsAndNotifies(t *testing.T) {
	forceBwrapMissing(t)

	savedGate := EvaluateCommandFunc
	EvaluateCommandFunc = func(sb *SandboxConfig, command string, args []string) GateDecision {
		return GateDecision{Action: ActionAllow, Risk: RiskLow}
	}
	t.Cleanup(func() { EvaluateCommandFunc = savedGate })

	var audits []CommandAuditEntry
	savedAudit := AuditCommandFunc
	AuditCommandFunc = func(entry CommandAuditEntry) { audits = append(audits, entry) }
	t.Cleanup(func() { AuditCommandFunc = savedAudit })

	var notices []string
	var noticeSession []string
	savedNotice := SandboxNoticeFunc
	SandboxNoticeFunc = func(sessionID, msg string) {
		notices = append(notices, msg)
		noticeSession = append(noticeSession, sessionID)
	}
	t.Cleanup(func() { SandboxNoticeFunc = savedNotice })

	dir := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModeBubblewrap,
		WorkspaceRoots: []string{dir},
		ReadRoots:      []string{dir},
		AllowFallback:  true,
	}
	saved := CurrentSandbox
	CurrentSandbox = sb
	t.Cleanup(func() { CurrentSandbox = saved })

	cmd, err := BuildExecCommandFor(WithConfig(nil, sb), "echo hi", nil, "", nil)
	if err != nil {
		t.Fatalf("fallback BuildExecCommand: %v", err)
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd on fallback")
	}

	found := false
	for _, a := range audits {
		if a.Decision == "sandbox_fallback" {
			found = true
			if a.SandboxMode != string(SandboxModeBubblewrap) {
				t.Errorf("fallback audit SandboxMode = %q, want bubblewrap", a.SandboxMode)
			}
			if !strings.Contains(a.Reason, "path-confinement only") {
				t.Errorf("fallback audit Reason missing isolation note: %q", a.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("no sandbox_fallback audit entry; got %+v", audits)
	}
	if len(notices) == 0 {
		t.Fatal("SandboxNoticeFunc was not called on fallback")
	}
	if !strings.Contains(notices[0], "Sandbox fallback") {
		t.Errorf("notice text = %q, want Sandbox fallback prefix", notices[0])
	}
	_ = noticeSession
}

// TestBuildExecCommandBwrapFailClosedKeepsDefault verifies AllowFallback=false
// (the default) refuses when bwrap is missing.
func TestBuildExecCommandBwrapFailClosedKeepsDefault(t *testing.T) {
	forceBwrapMissing(t)

	savedGate := EvaluateCommandFunc
	EvaluateCommandFunc = func(sb *SandboxConfig, command string, args []string) GateDecision {
		return GateDecision{Action: ActionAllow, Risk: RiskLow}
	}
	t.Cleanup(func() { EvaluateCommandFunc = savedGate })
	savedAudit := AuditCommandFunc
	AuditCommandFunc = func(entry CommandAuditEntry) {}
	t.Cleanup(func() { AuditCommandFunc = savedAudit })

	dir := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModeBubblewrap,
		WorkspaceRoots: []string{dir},
		ReadRoots:      []string{dir},
		AllowFallback:  false,
	}
	saved := CurrentSandbox
	CurrentSandbox = sb
	t.Cleanup(func() { CurrentSandbox = saved })

	if _, err := BuildExecCommand("echo hi", nil, "", nil); err == nil ||
		!strings.Contains(err.Error(), "allow_fallback") {
		t.Fatalf("expected allow_fallback fail-closed error, got %v", err)
	}
}

// TestSandboxStartupWarning covers the startup-visible half of issue #14.
func TestSandboxStartupWarning(t *testing.T) {
	if w := SandboxStartupWarning(nil); w != "" {
		t.Errorf("nil sandbox warning = %q, want empty", w)
	}
	if w := SandboxStartupWarning(&SandboxConfig{Mode: SandboxModeOff}); w != "" {
		t.Errorf("off warning = %q, want empty", w)
	}
	if w := SandboxStartupWarning(&SandboxConfig{Mode: SandboxModePaths}); w != "" {
		t.Errorf("paths warning = %q, want empty", w)
	}
	if w := SandboxStartupWarning(&SandboxConfig{Mode: SandboxModeLandlock}); w == "" {
		t.Error("landlock warning empty, want non-empty")
	}

	forceBwrapMissing(t)
	if w := SandboxStartupWarning(&SandboxConfig{Mode: SandboxModeBubblewrap, AllowFallback: true}); !strings.Contains(w, "path-confinement only") {
		t.Errorf("bubblewrap+fallback warning = %q, want path-confinement note", w)
	}
	if w := SandboxStartupWarning(&SandboxConfig{Mode: SandboxModeBubblewrap}); !strings.Contains(w, "refused") {
		t.Errorf("bubblewrap fail-closed warning = %q, want refused note", w)
	}
}
