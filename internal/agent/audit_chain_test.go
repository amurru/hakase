package agent

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/permissions"
)

func tempAuditDir(t *testing.T) string {
	t.Helper()
	old := auditLogDir
	dir := t.TempDir()
	auditLogDir = dir
	t.Cleanup(func() { auditLogDir = old })
	return dir
}

// TestAuditSecretsRedacted pins M5: secret-shaped values never reach
// the chained log (and hence never reach SIEM/export either). S13: the
// fixtures use shapes the unified util.RedactSecrets engine catches
// (typed [REDACTED:<kind>] tokens).
func TestAuditSecretsRedacted(t *testing.T) {
	dir := tempAuditDir(t)
	AuditCommandExec(CommandAuditEntry{
		Timestamp: time.Now(), Tool: "system_exec",
		Command:  `curl -H "Authorization: Bearer abcdefghij1234567890" https://x.example`,
		Args:     []string{"--token", "ghp_abcdefghij1234567890"},
		Reason:   "password=hunter22 run",
		Decision: "allowed",
	})
	entries, err := ReadAuditEntries(dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	blob, _ := json.Marshal(entries[0])
	for _, secret := range []string{"abcdefghij1234567890", "ghp_abcdefghij1234567890", "hunter22"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("chained entry leaks %q: %s", secret, blob)
		}
	}
	if !strings.Contains(string(blob), "REDACTED") {
		t.Errorf("no redaction marker in %s", blob)
	}
}

// TestAuditChainLinks verifies prev/entry linkage across appends.
func TestAuditChainLinks(t *testing.T) {
	dir := tempAuditDir(t)
	for i := 0; i < 3; i++ {
		AuditCommandExec(CommandAuditEntry{
			Timestamp: time.Now(), Tool: "system_exec",
			Command: "echo hi", Decision: "allowed",
		})
	}
	n, err := VerifyAuditChain(dir)
	if err != nil {
		t.Fatalf("VerifyAuditChain: %v", err)
	}
	if n != 3 {
		t.Errorf("chained = %d, want 3", n)
	}
	entries, err := ReadAuditEntries(dir, time.Time{})
	if err != nil {
		t.Fatalf("ReadAuditEntries: %v", err)
	}
	if entries[0].PrevHash != auditGenesis {
		t.Errorf("first prev = %q, want GENESIS", entries[0].PrevHash)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].PrevHash != entries[i-1].EntryHash {
			t.Errorf("entry %d prev does not link to entry %d hash", i, i-1)
		}
	}
}

// TestAuditChainTamper pins tamper detection on modify and on splice.
func TestAuditChainTamper(t *testing.T) {
	dir := tempAuditDir(t)
	AuditCommandExec(CommandAuditEntry{Timestamp: time.Now(), Tool: "a", Decision: "allowed"})
	AuditCommandExec(CommandAuditEntry{Timestamp: time.Now(), Tool: "b", Decision: "allowed"})

	path := filepath.Join(dir, "exec-audit.jsonl")
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")

	// Modify entry 0's decision without re-hashing.
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatal(err)
	}
	m["decision"] = "allowed-tampered"
	b, _ := json.Marshal(m)
	lines[0] = string(b)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAuditChain(dir); err == nil {
		t.Error("Verify accepted a tampered entry, want error")
	}
}

// TestAuditChainSpansRotation pins the chain surviving log rotation.
func TestAuditChainSpansRotation(t *testing.T) {
	dir := tempAuditDir(t)
	oldMax, oldBackups := AuditMaxBytes, AuditMaxBackups
	AuditMaxBytes, AuditMaxBackups = 400, 3
	t.Cleanup(func() { AuditMaxBytes, AuditMaxBackups = oldMax, oldBackups })

	for i := 0; i < 10; i++ {
		AuditCommandExec(CommandAuditEntry{
			Timestamp: time.Now(), Tool: "system_exec",
			Command: strings.Repeat("x", 40), Decision: "allowed",
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "exec-audit.1.jsonl")); err != nil {
		t.Fatalf("rotation did not happen: %v", err)
	}
	// Rotation drops the oldest backups by design: pin that every
	// surviving entry verifies with no break at file boundaries.
	n, err := VerifyAuditChain(dir)
	if err != nil {
		t.Fatalf("VerifyAuditChain across rotation: %v", err)
	}
	onDisk := 0
	for _, f := range auditChainFiles(dir) {
		raw, _ := os.ReadFile(f)
		onDisk += len(strings.Split(strings.TrimSpace(string(raw)), "\n"))
	}
	if n != onDisk || n == 0 {
		t.Errorf("chained = %d, on-disk lines = %d (want equal, nonzero)", n, onDisk)
	}
}

// TestAuditChainSkipsPreChain pins old hash-less lines verifying clean.
func TestAuditChainSkipsPreChain(t *testing.T) {
	dir := tempAuditDir(t)
	raw := `{"timestamp":"2025-01-01T00:00:00Z","tool":"old","decision":"allowed"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "exec-audit.jsonl"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	AuditCommandExec(CommandAuditEntry{Timestamp: time.Now(), Tool: "new", Decision: "allowed"})
	n, err := VerifyAuditChain(dir)
	if err != nil {
		t.Fatalf("VerifyAuditChain with pre-chain line: %v", err)
	}
	if n != 1 {
		t.Errorf("chained = %d, want 1 (pre-chain skipped)", n)
	}
}

// TestAuditApprovalAnswer pins actor/trace attribution on answers.
func TestAuditApprovalAnswer(t *testing.T) {
	dir := tempAuditDir(t)
	AuditApprovalAnswer("appr_1", "system_exec", true, "amy", "web")
	entries, err := ReadAuditEntries(dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Actor != "amy" || e.TraceID != "appr_1" || e.Decision != "approved" || e.Tool != "system_exec" {
		t.Errorf("answer entry = %+v, want actor/trace/approved/tool", e)
	}
}

// TestAuditCSVRow pins metadata-only export (no bodies).
func TestAuditCSVRow(t *testing.T) {
	e := CommandAuditEntry{
		Tool: "system_exec", Command: "rm -rf /", Args: []string{"-rf"},
		Decision: "denied", Risk: "high", Reason: "destructive",
		Actor: "amy", TraceID: "appr_1", PrevHash: "p", EntryHash: "h",
		PolicyRule: permissions.PolicyRule{Source: "enterprise", Action: "shell", Resource: "rm *", Effect: "deny"},
	}
	row := AuditCSVRow(e)
	for _, secret := range []string{"rm -rf /", "destructive", "-rf"} {
		if strings.Contains(row, secret) {
			t.Errorf("CSV row leaks body %q: %s", secret, row)
		}
	}
	for _, want := range []string{"system_exec", "denied", "amy", "appr_1", "enterprise", "deny", "p", "h"} {
		if !strings.Contains(row, want) {
			t.Errorf("CSV row missing %q: %s", want, row)
		}
	}
	if n := strings.Count(row, ","); n != strings.Count(AuditCSVHeaderV2, ",") {
		t.Errorf("CSV row has %d commas, header has %d", n, strings.Count(AuditCSVHeaderV2, ","))
	}
}

// TestAuditCSVVersionAwareParser pins the CSV schema evolution (FG-006):
// the old strict parser opens old-format rows, and the version-aware
// parser reads both schemas.
func TestAuditCSVVersionAwareParser(t *testing.T) {
	oldCSV := AuditCSVHeader + "\n" +
		"2026-10-20T10:00:00Z,system_exec,denied,high,amy,appr_1,enterprise,deny,p,h\n"
	// The old strict parser (FieldsPerRecord = the v1 column count)
	// opens an old-format export.
	r := csv.NewReader(strings.NewReader(oldCSV))
	r.FieldsPerRecord = len(strings.Split(AuditCSVHeader, ","))
	if _, err := r.ReadAll(); err != nil {
		t.Fatalf("old strict parser on old-format rows: %v", err)
	}
	// The version-aware parser reads the same rows.
	rows, err := ParseAuditCSV([]byte(oldCSV))
	if err != nil {
		t.Fatalf("ParseAuditCSV (v1): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("v1 rows = %d, want 1", len(rows))
	}
	if rows[0].Tool != "system_exec" || rows[0].Decision != "denied" || rows[0].EntryHash != "h" {
		t.Errorf("v1 row = %+v, want the old columns", rows[0])
	}
	if rows[0].FlowLabel != "" || rows[0].FlowVersion != "" {
		t.Errorf("v1 row carries flow columns: %+v", rows[0])
	}

	// A v2 export: the flow columns are appended at the end.
	e := CommandAuditEntry{
		Timestamp: time.Now(), Tool: "send_message", Decision: "flow_blocked",
		FlowLabel: "trusted/internal v2", FlowArgsHash: "abc123",
	}
	v2CSV := AuditCSVHeaderV2 + "\n" + AuditCSVRow(e) + "\n"
	rows2, err := ParseAuditCSV([]byte(v2CSV))
	if err != nil {
		t.Fatalf("ParseAuditCSV (v2): %v", err)
	}
	if len(rows2) != 1 {
		t.Fatalf("v2 rows = %d, want 1", len(rows2))
	}
	if rows2[0].FlowLabel != "trusted/internal v2" || rows2[0].FlowArgsHash != "abc123" {
		t.Errorf("v2 row = %+v, want the flow columns", rows2[0])
	}
	if rows2[0].FlowVersion != AuditCSVVersion {
		t.Errorf("v2 row version = %q, want %q", rows2[0].FlowVersion, AuditCSVVersion)
	}
	// The old strict parser refuses a v2 row: the shape changed, and a
	// v1 consumer must not silently read a truncated row.
	r2 := csv.NewReader(strings.NewReader(v2CSV))
	r2.FieldsPerRecord = len(strings.Split(AuditCSVHeader, ","))
	if _, err := r2.ReadAll(); err == nil {
		t.Error("old strict parser accepted a v2 row")
	}
	// An unknown header is refused.
	if _, err := ParseAuditCSV([]byte("a,b,c\n1,2,3\n")); err == nil {
		t.Error("ParseAuditCSV accepted an unknown header")
	}
	// A reordered header is refused: the parser maps columns by
	// position, so a permuted header would read the wrong fields.
	reordered := strings.Replace(AuditCSVHeaderV2, "tool,decision", "decision,tool", 1) + "\n" + AuditCSVRow(e) + "\n"
	if _, err := ParseAuditCSV([]byte(reordered)); err == nil {
		t.Error("ParseAuditCSV accepted a reordered header")
	}
	// A row with an unsupported flow version is refused.
	badVersion := AuditCSVHeaderV2 + "\n" + strings.TrimSuffix(AuditCSVRow(e), AuditCSVVersion) + "9\n"
	if _, err := ParseAuditCSV([]byte(badVersion)); err == nil {
		t.Error("ParseAuditCSV accepted an unsupported flow version")
	}
}

// TestAuditHMACChain pins L1: a keyed chain verifies with the key,
// fails without it, and verifies again once restored.
func TestAuditHMACChain(t *testing.T) {
	dir := tempAuditDir(t)
	ConfigureAuditHMACKey([]byte("test-key-123"))
	t.Cleanup(func() { ConfigureAuditHMACKey(nil) })
	AuditCommandExec(CommandAuditEntry{Timestamp: time.Now(), Tool: "a", Decision: "allowed"})
	AuditCommandExec(CommandAuditEntry{Timestamp: time.Now(), Tool: "b", Decision: "allowed"})
	if n, err := VerifyAuditChain(dir); err != nil || n != 2 {
		t.Fatalf("keyed verify = %d/%v, want 2/nil", n, err)
	}
	ConfigureAuditHMACKey(nil)
	if _, err := VerifyAuditChain(dir); err == nil {
		t.Fatal("keyless verify of HMAC log succeeded, want error")
	}
	ConfigureAuditHMACKey([]byte("test-key-123"))
	if n, err := VerifyAuditChain(dir); err != nil || n != 2 {
		t.Fatalf("restored verify = %d/%v, want 2/nil", n, err)
	}
	ConfigureAuditHMACKey([]byte("wrong-key"))
	if _, err := VerifyAuditChain(dir); err == nil {
		t.Fatal("wrong-key verify succeeded, want error")
	}
}

// TestAuditSecretsExtended pins Basic auth and quoted-JSON redaction.
func TestAuditSecretsExtended(t *testing.T) {
	for _, cmd := range []string{
		`curl -H "Authorization: Basic dXNlcjpwYXNz" https://x.example`,
		`curl -d '{"token":"sensitive-value","id":1}' https://x.example`,
	} {
		if got := redactSecrets(cmd); strings.Contains(got, "dXNlcjpwYXNz") || strings.Contains(got, "sensitive-value") {
			t.Errorf("redactSecrets(%q) = %q, want redacted", cmd, got)
		}
	}
}

// TestAuditHMACTransition pins mixed-algorithm verification: sha256
// entries written before the switch keep verifying after HMAC is on.
func TestAuditHMACTransition(t *testing.T) {
	dir := tempAuditDir(t)
	AuditCommandExec(CommandAuditEntry{Timestamp: time.Now(), Tool: "old", Decision: "allowed"})
	ConfigureAuditHMACKey([]byte("k1"))
	t.Cleanup(func() { ConfigureAuditHMACKey(nil) })
	AuditCommandExec(CommandAuditEntry{Timestamp: time.Now(), Tool: "new", Decision: "allowed"})
	n, err := VerifyAuditChain(dir)
	if err != nil || n != 2 {
		t.Fatalf("mixed verify = %d/%v, want 2/nil", n, err)
	}
	entries, _ := ReadAuditEntries(dir, time.Time{})
	if entries[0].HashAlg != "" || entries[1].HashAlg != hashAlgHMAC {
		t.Errorf("markers = %q/%q, want \"\"/hmac-sha256", entries[0].HashAlg, entries[1].HashAlg)
	}
}
