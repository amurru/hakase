// sleep_test.go - `sleep` CLI acceptance (SL-020): harvest end-to-end from
// fixture sessions (zero secret survivors), redaction truth-table refusal,
// review flow, and evolve-md's M4 gate on machine-generated task files.
package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/session"
	"amurru/hakase/internal/util"
)

// captureStderr runs fn while capturing stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string)
	go func() {
		var buf strings.Builder
		_, _ = copyBuf(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	return <-done
}

func copyBuf(dst *strings.Builder, src *os.File) (int64, error) {
	buf := make([]byte, 4096)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			dst.Write(buf[:n])
			total += int64(n)
		}
		if err != nil {
			return total, nil
		}
	}
}

func TestSleepHarvest_EndToEnd(t *testing.T) {
	cronTestEnv(t)
	now := time.Now().UTC()
	store, err := session.NewSessionStore("sessions")
	if err != nil {
		t.Fatal(err)
	}
	s := session.NewSession("leaky")
	s.CreatedAt = now.Add(-time.Hour)
	s.UpdatedAt = now.Add(-time.Minute)
	s.Messages = append(s.Messages,
		session.Message{Role: "user", Content: "my key is ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD", Timestamp: now.Add(-30 * time.Minute), Kind: session.MessageKindText, InContext: true},
		session.Message{Role: "agent", Content: "understood, using [the key]", Timestamp: now.Add(-29 * time.Minute), Kind: session.MessageKindText, InContext: true},
	)
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}

	var code int
	stderr := captureStderr(t, func() {
		code = RunSleepCLI([]string{"harvest", "--out", "digests.json"})
	})
	if code != 0 {
		t.Fatalf("harvest exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	data, err := os.ReadFile("digests.json")
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat("digests.json"); info.Mode().Perm() != 0o600 {
		t.Errorf("digest file mode = %o, want 600", info.Mode().Perm())
	}
	out := string(data)
	if !strings.Contains(out, "using [the key]") {
		t.Error("expected the redacted final to survive")
	}
	if strings.Contains(out, "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD") {
		t.Error("secret survived harvest")
	}
	if redacted, changed := util.RedactSecrets(out); changed {
		t.Errorf("output still secret-shaped: %.80s", redacted)
	}
	var file struct {
		Sessions []struct {
			TurnCount int `json:"turn_count"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Sessions) != 1 || file.Sessions[0].TurnCount != 1 {
		t.Errorf("unexpected digest: %s", out)
	}
}

func TestSleepHarvest_RedactionRefusal(t *testing.T) {
	cronTestEnv(t)
	var code int
	stderr := captureStderr(t, func() {
		code = RunSleepCLI([]string{"harvest", "--redact-secrets=false"})
	})
	if code != 1 {
		t.Errorf("redact-secrets=false without flag exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "refusing") {
		t.Errorf("expected the hard-error message, got: %s", stderr)
	}
}

func TestSleepReview_Flow(t *testing.T) {
	cronTestEnv(t)
	gen := time.Now().UTC().Add(-time.Hour)
	if err := os.WriteFile("tasks.json", []byte(`{"generated_at":"`+gen.Format(time.RFC3339)+`","tasks":[{"id":"t1","intent":"q"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var code int
	code = RunSleepCLI([]string{"review", "--tasks", "tasks.json"})
	if code != 2 {
		t.Errorf("missing reviewer exit = %d, want 2", code)
	}
	code = RunSleepCLI([]string{"review", "--tasks", "tasks.json", "--reviewer", "alice"})
	if code != 0 {
		t.Errorf("review exit = %d, want 0", code)
	}
	if _, err := os.Stat("tasks.review.json"); err != nil {
		t.Fatalf("sidecar missing: %v", err)
	}
}

func TestSleepRun_DryRunAndStatus(t *testing.T) {
	cronTestEnv(t)
	now := time.Now().UTC()
	store, err := session.NewSessionStore("sessions")
	if err != nil {
		t.Fatal(err)
	}
	s := session.NewSession("counts")
	s.CreatedAt = now.Add(-time.Hour)
	s.UpdatedAt = now.Add(-time.Minute)
	s.Messages = append(s.Messages,
		session.Message{Role: "user", Content: "hello there", Timestamp: now.Add(-30 * time.Minute), Kind: session.MessageKindText, InContext: true},
		session.Message{Role: "agent", Content: "hi", Timestamp: now.Add(-29 * time.Minute), Kind: session.MessageKindText, InContext: true},
	)
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}

	var code int
	stderr := captureStderr(t, func() {
		code = RunSleepCLI([]string{"dry-run"})
	})
	if code != 0 {
		t.Fatalf("dry-run exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if _, err := os.Stat(".hakase/sleep-state.json"); !os.IsNotExist(err) {
		t.Error("dry-run must not write state")
	}

	code = RunSleepCLI([]string{"status"})
	if code != 0 {
		t.Errorf("status exit = %d, want 0", code)
	}
}

func TestSleepRun_RequiresModel(t *testing.T) {
	cronTestEnv(t)
	for _, k := range []string{
		"HAKASE_API_KEY", "HAKASE_PROVIDER", "HAKASE_MODEL",
		"HAKASE_BASE_URL", "HAKASE_SUMMARY_MODEL",
	} {
		t.Setenv(k, "")
	}
	var code int
	stderr := captureStderr(t, func() {
		code = RunSleepCLI([]string{"run"})
	})
	if code != 1 {
		t.Errorf("no-model run exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "configured model") {
		t.Errorf("expected the bootstrap failure, got: %s", stderr)
	}
}

func TestSleepAdoptAndUsage(t *testing.T) {
	cronTestEnv(t)
	if code := RunSleepCLI([]string{"adopt"}); code != 2 {
		t.Errorf("adopt without --dir = %d, want 2", code)
	}
	if code := RunSleepCLI([]string{"adopt", "--dir", "missing-dir"}); code != 1 {
		t.Errorf("adopt of missing dir = %d, want 1", code)
	}
	if code := RunSleepCLI([]string{"bogus"}); code != 2 {
		t.Errorf("unknown verb = %d, want 2", code)
	}
}

func TestSkillEvolveMD_MachineGeneratedRequiresReview(t *testing.T) {
	tasks := evolveMDTestEnv(t)
	gen := time.Now().UTC().Add(-time.Hour)
	if err := os.WriteFile(tasks, []byte(`{"generated_at":"`+gen.Format(time.RFC3339)+`","tasks":[
		{"id":"t1","intent":"q","reference_kind":"exact","reference":"yes","split":"train"},
		{"id":"v1","intent":"q","reference_kind":"exact","reference":"yes","split":"val"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var code int
	stderr := captureStderr(t, func() {
		code = runSkillEvolveMD([]string{"--skill", "demo", "--tasks", tasks, "--dry-run"})
	})
	if code != 1 {
		t.Errorf("unreviewed machine-generated exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "review sidecar") {
		t.Errorf("expected the review-gate message, got: %s", stderr)
	}

	// Reviewing unblocks the M4 gate; the run then proceeds and fails later
	// at model bootstrap (no model configured in tests), still exit 1 but
	// with the bootstrap message instead.
	if c := RunSleepCLI([]string{"review", "--tasks", tasks, "--reviewer", "alice"}); c != 0 {
		t.Fatalf("review exit = %d, want 0", c)
	}
	stderr = captureStderr(t, func() {
		code = runSkillEvolveMD([]string{"--skill", "demo", "--tasks", tasks, "--dry-run"})
	})
	if code != 1 {
		t.Errorf("post-review no-model exit = %d, want 1", code)
	}
	if strings.Contains(stderr, "review sidecar") || !strings.Contains(stderr, "configured model") {
		t.Errorf("expected the model-bootstrap failure, got: %s", stderr)
	}
}
