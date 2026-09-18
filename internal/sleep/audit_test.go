// audit_test.go - SL-040 acceptance: the deny-by-default replay audit is
// carried through the replay context and staged into diagnostics.
package sleep

import (
	"context"
	"strings"
	"testing"
)

func TestReplayAuditRecordsToolAttempts(t *testing.T) {
	var audit ReplayAudit
	_ = withReplayAudit(context.Background(), &audit)
	audit.addToolAttempt("write_file")
	audit.addToolAttempt("write_file")
	audit.addToolAttempt("system_exec")
	audit.addSandboxDenial()
	audit.addSandboxDenial()
	audit.addSandboxDenial()

	snap := audit.snapshot()
	if snap.DeniedToolAttempts["write_file"] != 2 || snap.DeniedToolAttempts["system_exec"] != 1 {
		t.Errorf("denied attempts = %+v", snap.DeniedToolAttempts)
	}
	if snap.SandboxPathDenials != 3 {
		t.Errorf("sandbox denials = %d, want 3", snap.SandboxPathDenials)
	}

	// Nil audit is a no-op (single-shot replay path).
	var nilAudit *ReplayAudit
	nilAudit.addToolAttempt("x")
	nilAudit.addSandboxDenial()
}

func TestReplayAuditCtxRoundTrip(t *testing.T) {
	audit := auditFrom(context.Background())
	if audit != nil {
		t.Fatal("bare ctx must carry no audit")
	}
	var want ReplayAudit
	if got := auditFrom(withReplayAudit(context.Background(), &want)); got != &want {
		t.Fatal("ctx must return the attached audit")
	}
}

func TestSandboxDenialMarkers(t *testing.T) {
	// The markers must match the real sandbox refusal wording; a drift only
	// undercounts, but keep them honest against current phrasing.
	sample := `map[error:path /etc/passwd is a denied sensitive file]`
	hit := false
	for _, m := range sandboxDenialMarkers {
		if strings.Contains(sample, m) {
			hit = true
		}
	}
	if !hit {
		t.Errorf("no marker matches real refusal text: %s", sample)
	}
}
