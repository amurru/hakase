package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
)

// seedAudit writes two chained entries into dir and points the agent
// audit log there.
func seedAudit(t *testing.T, dir string) {
	t.Helper()
	old := hakaseagent.AuditDir()
	hakaseagent.SetAuditDir(dir)
	t.Cleanup(func() { hakaseagent.SetAuditDir(old) })
	hakaseagent.AuditCommandExec(hakaseagent.CommandAuditEntry{
		Timestamp: time.Now().Add(-time.Hour), Tool: "system_exec",
		Command: "ls", Decision: "allowed",
	})
	hakaseagent.AuditCommandExec(hakaseagent.CommandAuditEntry{
		Timestamp: time.Now(), Tool: "system_exec",
		Command: "git push origin", Decision: "denied",
	})
}

func TestAuditVerifyOK(t *testing.T) {
	dir := t.TempDir()
	seedAudit(t, dir)
	if code := RunAuditCLI([]string{"verify", "--dir", dir}); code != 0 {
		t.Errorf("verify exit = %d, want 0", code)
	}
}

func TestAuditVerifyTampered(t *testing.T) {
	dir := t.TempDir()
	seedAudit(t, dir)
	path := filepath.Join(dir, "exec-audit.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the last byte (inside the final hash).
	raw[len(raw)-2] = 'x'
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := RunAuditCLI([]string{"verify", "--dir", dir}); code == 0 {
		t.Error("verify exit = 0 on tampered log, want nonzero")
	}
}

func TestAuditExportBadFlags(t *testing.T) {
	if code := RunAuditCLI([]string{"export", "--format", "xml", "--dir", t.TempDir()}); code == 0 {
		t.Error("export --format xml exit = 0, want nonzero")
	}
	if code := RunAuditCLI([]string{"bogus"}); code == 0 {
		t.Error("unknown subcommand exit = 0, want nonzero")
	}
}
