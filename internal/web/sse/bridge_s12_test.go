// bridge_s12_test.go - S12 bridge push bodies redact secret-shaped
// content and still publish (redact-only, no gating, no binding).
package sse

import (
	"encoding/json"
	"strings"
	"testing"
)

// S12: cron payloads redact secrets but still publish.
func TestEventBridgeCronRedactsSecrets(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.SendCron("sess-1", "job-1", "nightly", "completed",
		"report token=supersecret123456 done", "/tmp/out password=hunter22hunter")

	select {
	case data := <-ch:
		validateSSE(t, data, "cron", func(payload map[string]any) {
			raw, _ := json.Marshal(payload)
			joined := string(raw)
			for _, leak := range []string{"supersecret123456", "hunter22hunter"} {
				if strings.Contains(joined, leak) {
					t.Errorf("cron payload leaks %q: %s", leak, joined)
				}
			}
			if !strings.Contains(joined, "REDACTED") {
				t.Errorf("cron payload carries no redaction token: %s", joined)
			}
			if payload["job_id"] != "job-1" || payload["status"] != "completed" {
				t.Errorf("redaction must not drop routing fields: %v", payload)
			}
		})
	default:
		t.Fatal("expected event on channel (redact must not withhold)")
	}
}

// S12: delegation payloads redact secrets but still publish.
func TestEventBridgeDelegationRedactsSecrets(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.SendDelegation("sess-1", "t-1", "helper", "completed",
		"Bearer abcdefghij1234567890 result")

	select {
	case data := <-ch:
		validateSSE(t, data, "delegation", func(payload map[string]any) {
			raw, _ := json.Marshal(payload)
			joined := string(raw)
			if strings.Contains(joined, "abcdefghij1234567890") {
				t.Errorf("delegation payload leaks secret: %s", joined)
			}
			if !strings.Contains(joined, "REDACTED") {
				t.Errorf("delegation payload carries no redaction token: %s", joined)
			}
		})
	default:
		t.Fatal("expected event on channel (redact must not withhold)")
	}
}
