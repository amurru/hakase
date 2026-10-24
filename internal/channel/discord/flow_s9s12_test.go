// flow_s9s12_test.go - S10 approval prompt redaction + S12 push body
// redaction with Telegram parity (highs fix-up lane, epic #124).
package discord

import (
	"strings"
	"testing"
)

// S10: the approval prompt still delivers (redact-only, no withhold),
// carries no secret-shaped bytes, and truncates long fields (parity
// with the Telegram 512-rune bound).
func TestApprovalPromptRedactsSecrets(t *testing.T) {
	b, sender, _ := newTestBot(t, []int64{100})
	secretReason := "needs password=hunter22hunter now"
	secretCommand := "curl -H 'Authorization: Bearer abcdefghij1234567890' https://x.example"
	b.ApprovalPrompt("sess-unknown", "gate-redact", "system_exec", "high", secretReason, secretCommand)

	sender.mu.Lock()
	msgs := append([]string(nil), sender.buttons...)
	sender.mu.Unlock()
	if len(msgs) == 0 {
		t.Fatal("approval prompt delivered no message (redact must not withhold)")
	}
	joined := strings.Join(msgs, "\n")
	for _, leak := range []string{"hunter22hunter", "abcdefghij1234567890"} {
		if strings.Contains(joined, leak) {
			t.Errorf("approval prompt leaks %q: %q", leak, joined)
		}
	}
	if !strings.Contains(joined, "REDACTED") {
		t.Errorf("approval prompt carries no redaction token: %q", joined)
	}

	// Parity: a 2000-rune reason is truncated like Telegram.
	b.ApprovalPrompt("sess-unknown", "gate-long", "system_exec", "high", strings.Repeat("r", 2000), "echo hi")
	sender.mu.Lock()
	msgs = append([]string(nil), sender.buttons...)
	sender.mu.Unlock()
	last := msgs[len(msgs)-1]
	if n := len([]rune(last)); n > 1200 {
		t.Errorf("long approval prompt = %d runes, want truncated (~512+chrome)", n)
	}
}

// S12: cron/task/delegation push bodies redact secret-shaped content
// and still deliver (redact-only, no withhold, no binding).
func TestPushBodiesRedactSecrets(t *testing.T) {
	b, sender, _ := newTestBot(t, []int64{100})
	bindUserSession(t, b, 100)

	b.CronEvent("", "completed", "j1", "nightly", "report token=supersecret123456 done", "/tmp/out password=hunter22hunter")
	b.TaskEvent("", "completed", "t1", "job title", "done")
	b.DelegationEvent("", "completed", "task-1", "helper", "Bearer abcdefghij1234567890 result")

	sender.mu.Lock()
	msgs := append([]string(nil), sender.sends...)
	sender.mu.Unlock()
	if len(msgs) == 0 {
		t.Fatal("redacted pushes delivered nothing (redact must not withhold)")
	}
	joined := strings.Join(msgs, "\n")
	for _, leak := range []string{"supersecret123456", "hunter22hunter", "abcdefghij1234567890"} {
		if strings.Contains(joined, leak) {
			t.Errorf("push body leaks %q: %q", leak, joined)
		}
	}
	if !strings.Contains(joined, "REDACTED") {
		t.Errorf("push bodies carry no redaction token: %q", joined)
	}
}

// H7 clarify redaction: drive the real ClarifyPrompt transport and
// inspect the delivered body (never a local re-implementation of the
// redact-then-truncate order).
func TestClarifyPromptRedactsSecrets(t *testing.T) {
	b, sender, _ := newTestBot(t, []int64{100})
	bindUserSession(t, b, 100)
	q := `deploy with token=supersecretvalue123456?`
	choices := []string{`ghp_abcdefghij1234567890choice`, "plain"}
	b.ClarifyPrompt("sess-clarify-redact", "clar_redact", q, choices, false)
	sender.mu.Lock()
	msgs := append([]string(nil), sender.sends...)
	btns := append([]string(nil), sender.buttons...)
	sender.mu.Unlock()
	joined := strings.Join(msgs, "\n") + "\n" + strings.Join(btns, "\n")
	if len(msgs)+len(btns) == 0 {
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
