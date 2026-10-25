// flow_s9s12_test.go - S10 approval prompt redaction + S12 push body
// redaction (highs fix-up lane, epic #124).
package telegram

import (
	"strings"
	"testing"
)

const flowSecretReason = "needs password=hunter22hunter now"
const flowSecretCommand = "curl -H 'Authorization: Bearer abcdefghij1234567890' https://x.example"

// S10: the approval prompt still delivers (redact-only, no withhold)
// and carries no secret-shaped bytes.
func TestApprovalPromptRedactsSecrets(t *testing.T) {
	b, api := newRunTestBot(t)
	b.ApprovalPrompt("sess-unknown", "gate-redact", "system_exec", "high", flowSecretReason, flowSecretCommand)

	var bodies []string
	for _, s := range api.sends() {
		bodies = append(bodies, s.text)
	}
	if len(bodies) == 0 {
		t.Fatal("approval prompt delivered no message (redact must not withhold)")
	}
	joined := strings.Join(bodies, "\n")
	for _, leak := range []string{"hunter22hunter", "abcdefghij1234567890"} {
		if strings.Contains(joined, leak) {
			t.Errorf("approval prompt leaks %q: %q", leak, joined)
		}
	}
	if !strings.Contains(joined, "REDACTED") {
		t.Errorf("approval prompt carries no redaction token: %q", joined)
	}
}

// S12: cron/task/delegation push bodies redact secret-shaped content
// and still deliver (redact-only, no withhold, no binding).
func TestPushBodiesRedactSecrets(t *testing.T) {
	b, api := newRunTestBot(t)
	chat := bindChatSession(t, b, 100, true)
	_ = chat

	b.CronEvent("", "completed", "j1", "nightly", "report token=supersecret123456 done", "/tmp/out password=hunter22hunter")
	b.TaskEvent("", "completed", "t1", "job title", "done")
	b.DelegationEvent("", "completed", "task-1", "helper", "Bearer abcdefghij1234567890 result")

	var joined string
	for _, s := range api.sends() {
		joined += "\n" + s.text
	}
	for _, leak := range []string{"supersecret123456", "hunter22hunter", "abcdefghij1234567890"} {
		if strings.Contains(joined, leak) {
			t.Errorf("push body leaks %q: %q", leak, joined)
		}
	}
	if !strings.Contains(joined, "REDACTED") {
		t.Errorf("push bodies carry no redaction token: %q", joined)
	}
	if !strings.Contains(joined, "nightly") || !strings.Contains(joined, "task") {
		t.Errorf("redacted pushes must still deliver: %q", joined)
	}
}

// H7 clarify redaction: drive the real ClarifyPrompt transport and
// inspect the delivered body (never a local re-implementation of the
// redact-then-truncate order).
func TestClarifyPromptRedactsSecrets(t *testing.T) {
	b, api := newRunTestBot(t)
	q := `deploy with token=supersecretvalue123456?`
	choices := []string{`ghp_abcdefghij1234567890choice`, "plain"}
	b.ClarifyPrompt("sess-clarify-redact", "clar_redact", q, choices, false)
	var joined string
	for _, s := range api.sends() {
		joined += "\n" + s.text
	}
	if len(api.sends()) == 0 {
		t.Fatal("clarify prompt delivered no message (redact must not withhold)")
	}
	for _, leak := range []string{"supersecretvalue123456", "ghp_abcdefghij1234567890"} {
		if strings.Contains(joined, leak) {
			t.Errorf("clarify prompt leaks %q: %q", leak, joined)
		}
	}
	if !strings.Contains(joined, "REDACTED") {
		t.Errorf("clarify prompt carries no redaction token: %q", joined)
	}
}
