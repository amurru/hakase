// harvest_test.go - SL-020 acceptance: fixture sessions harvest with zero
// secret-shaped survivors, walk filters (archived/project/checkpoint/cap),
// best-effort audit join, and the attachment no-copy rule (SL-011c).
package sleep

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/session"
	"amurru/hakase/internal/util"
)

// harvestTestSession builds and saves one session with explicit timestamps
// (the AddMessage helpers stamp time.Now, which checkpoint tests cannot use).
func harvestTestSession(t *testing.T, dir string, s *session.Session) {
	t.Helper()
	store, err := session.NewSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
}

func newDigestSession(id, title, project string, archived bool, created, updated time.Time) *session.Session {
	return &session.Session{
		ID: id, Title: title, ProjectID: project, Archived: archived,
		CreatedAt: created, UpdatedAt: updated,
	}
}

func addTurn(s *session.Session, ts time.Time, user, agent string) {
	if user != "" {
		s.Messages = append(s.Messages, session.Message{
			Role: "user", Content: user, Timestamp: ts, InContext: true, Kind: session.MessageKindText,
		})
	}
	if agent != "" {
		s.Messages = append(s.Messages, session.Message{
			Role: "agent", Content: agent, Timestamp: ts.Add(time.Second), InContext: true, Kind: session.MessageKindText,
		})
	}
}

func harvestForTest(t *testing.T, opts HarvestOpts) HarvestFile {
	t.Helper()
	file, err := Harvest(opts)
	if err != nil {
		t.Fatalf("harvest: %v", err)
	}
	return file
}

func TestHarvest_RedactsAllSecretShapes(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	s := newDigestSession("sess_secrets", "leaky session", "", false, now.Add(-time.Hour), now.Add(-time.Minute))
	// Every oracle sample lands in a prompt or a final, verbatim.
	samples := util.RedactedPatterns()
	for i, p := range samples {
		if i%2 == 0 {
			addTurn(s, now.Add(-30*time.Minute), "please use this key: "+p, "")
		} else {
			addTurn(s, now.Add(-29*time.Minute), "", "done. the value was "+p+" as requested.")
		}
	}
	harvestTestSession(t, dir, s)

	file := harvestForTest(t, HarvestOpts{SessionsDir: dir, Now: func() time.Time { return now }})
	if len(file.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(file.Sessions))
	}
	blob, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	out := string(blob)
	// Oracle 1: no raw sample survives.
	for _, p := range samples {
		if strings.Contains(out, p) {
			t.Errorf("secret-shaped survivor in output: %q", p[:12]+"...")
		}
	}
	// Oracle 2: re-running redaction over the output must be a no-op.
	if redacted, changed := util.RedactSecrets(out); changed {
		t.Errorf("output still secret-shaped after redaction pass: %.80s", redacted)
	}
}

func TestHarvest_WalkFilters(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	old := now.Add(-200 * time.Hour)

	active := newDigestSession("sess_active", "active", "proj-a", false, now.Add(-3*time.Hour), now.Add(-time.Hour))
	addTurn(active, now.Add(-2*time.Hour), "old turn before checkpoint", "old final")
	addTurn(active, now.Add(-30*time.Minute), "new turn with load_markdown_skill with name 'pdf-tools'", "new final")
	harvestTestSession(t, dir, active)

	archived := newDigestSession("sess_arch", "archived", "proj-a", true, old, now.Add(-time.Minute))
	addTurn(archived, now.Add(-time.Minute), "archived turn", "")
	harvestTestSession(t, dir, archived)

	other := newDigestSession("sess_other", "other project", "proj-b", false, old, now.Add(-2*time.Hour))
	addTurn(other, now.Add(-2*time.Hour), "other project turn", "")
	harvestTestSession(t, dir, other)

	// Checkpoint harvest: only the post-checkpoint turn of sess_active.
	file := harvestForTest(t, HarvestOpts{
		SessionsDir: dir,
		Checkpoint:  now.Add(-time.Hour),
		Now:         func() time.Time { return now },
	})
	if len(file.Sessions) != 1 || file.Sessions[0].SessionID != "sess_active" {
		t.Fatalf("checkpoint harvest sessions = %+v, want only sess_active", file.Sessions)
	}
	d := file.Sessions[0]
	if len(d.Prompts) != 1 || !strings.Contains(d.Prompts[0], "new turn") {
		t.Errorf("prompts = %v, want only the post-checkpoint turn", d.Prompts)
	}
	if len(d.SkillsUsed) != 1 || d.SkillsUsed[0] != "pdf-tools" {
		t.Errorf("skills_used = %v, want [pdf-tools]", d.SkillsUsed)
	}

	// Default lookback with archived included and a project filter.
	file = harvestForTest(t, HarvestOpts{
		SessionsDir: dir, IncludeArchived: true, ProjectID: "proj-a",
		Now: func() time.Time { return now },
	})
	if len(file.Sessions) != 2 {
		t.Fatalf("project+archived harvest = %d sessions, want 2", len(file.Sessions))
	}

	// Cap keeps the newest sessions only.
	file = harvestForTest(t, HarvestOpts{
		SessionsDir: dir, IncludeArchived: true, MaxSessions: 1,
		Now: func() time.Time { return now },
	})
	if len(file.Sessions) != 1 || file.Sessions[0].SessionID != "sess_arch" {
		t.Fatalf("capped harvest = %+v, want only the newest (sess_arch)", file.Sessions)
	}
}

func TestHarvest_TurnShapeAndAttachments(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	s := newDigestSession("sess_shape", "shape", "", false, now.Add(-time.Hour), now)
	addTurn(s, now.Add(-30*time.Minute), "first prompt", "first final")
	// Summary messages count as finals; sidekick and tool rows do not count.
	s.Messages = append(s.Messages,
		session.Message{Role: "agent", Content: "summary of history", Timestamp: now.Add(-20 * time.Minute), Kind: session.MessageKindSummary, InContext: true},
		session.Message{Role: "agent", Content: "watchdog note", Timestamp: now.Add(-19 * time.Minute), Kind: session.MessageKindSidekick, InContext: true},
		session.Message{Role: "agent", Content: "tool payload", Timestamp: now.Add(-18 * time.Minute), Kind: session.MessageKindToolResult, InContext: true},
	)
	addTurn(s, now.Add(-10*time.Minute), "second prompt, that works now", "second final")
	// Attachments: path and name must never appear in the digest (SL-011c).
	s.Messages[0].Attachments = []session.AttachmentRef{
		{Name: "id_rsa", Path: "/home/user/.ssh/id_rsa", MIME: "application/octet-stream"},
	}
	harvestTestSession(t, dir, s)

	file := harvestForTest(t, HarvestOpts{SessionsDir: dir, Now: func() time.Time { return now }})
	if len(file.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(file.Sessions))
	}
	d := file.Sessions[0]
	if d.TurnCount != 2 {
		t.Errorf("turn_count = %d, want 2", d.TurnCount)
	}
	if len(d.Prompts) != 2 || len(d.Finals) != 3 {
		t.Errorf("prompts=%d finals=%d, want 2 prompts (3 finals incl. summary)", len(d.Prompts), len(d.Finals))
	}
	if len(d.FeedbackSignals) == 0 {
		t.Error("feedback_signals empty, want the works lexicon hit")
	}
	blob, _ := json.Marshal(file)
	out := string(blob)
	if strings.Contains(out, "/home/user/.ssh/id_rsa") || strings.Contains(out, "id_rsa") {
		t.Error("attachment path/name leaked into digest")
	}
}

func TestHarvest_AuditJoin(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	logPath := filepath.Join(dir, "exec-audit.jsonl")
	entry := func(ts time.Time, tool, cmd string) string {
		b, _ := json.Marshal(harvestAuditLine{Timestamp: ts, Tool: tool, Command: cmd, Args: []string{"--secret-flag", "argvalue"}, Decision: "allowed"})
		return string(b)
	}
	lines := strings.Join([]string{
		entry(now.Add(-30*time.Minute), "system_exec", "git"),
		entry(now.Add(-48*time.Hour), "python_interpreter", "outside-window"),
		"{corrupt json",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newDigestSession("sess_audit", "audit", "", false, now.Add(-time.Hour), now)
	addTurn(s, now.Add(-time.Hour), "run git", "ran git")
	harvestTestSession(t, dir, s)

	// Default: tool names always, args never.
	file := harvestForTest(t, HarvestOpts{SessionsDir: dir, AuditLogPath: logPath, Now: func() time.Time { return now }})
	d := file.Sessions[0]
	if len(d.ToolsUsed) != 1 || d.ToolsUsed[0] != "system_exec" {
		t.Errorf("tools_used = %v, want [system_exec] (outside-window and corrupt skipped)", d.ToolsUsed)
	}
	if len(d.AuditArgs) != 0 {
		t.Errorf("audit_args present by default: %v", d.AuditArgs)
	}

	// Opt-in args.
	file = harvestForTest(t, HarvestOpts{SessionsDir: dir, AuditLogPath: logPath, IncludeAuditArgs: true, Now: func() time.Time { return now }})
	d = file.Sessions[0]
	if len(d.AuditArgs) != 1 || !strings.Contains(d.AuditArgs[0], "git") {
		t.Errorf("audit_args = %v, want the joined command line", d.AuditArgs)
	}
}

func TestHarvest_WriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	s := newDigestSession("sess_rt", "roundtrip", "", false, now.Add(-time.Hour), now)
	addTurn(s, now.Add(-time.Minute), "prompt", "final")
	harvestTestSession(t, dir, s)

	file := harvestForTest(t, HarvestOpts{SessionsDir: dir, Now: func() time.Time { return now }})
	out := filepath.Join(dir, "nested", "digests.json")
	if err := WriteHarvestFile(out, file); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var back HarvestFile
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if len(back.Sessions) != 1 || back.Sessions[0].SessionID != "sess_rt" {
		t.Errorf("round-trip sessions = %+v", back.Sessions)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("digest file mode = %o, want 600", info.Mode().Perm())
	}
	// No temp residue.
	entries, _ := os.ReadDir(filepath.Dir(out))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".sleep-") {
			t.Errorf("temp file residue: %s", e.Name())
		}
	}
}
