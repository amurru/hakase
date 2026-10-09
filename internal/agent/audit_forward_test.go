package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAuditForwardPostsEntries pins best-effort SIEM delivery of each
// appended entry.
func TestAuditForwardPostsEntries(t *testing.T) {
	tempAuditDir(t)
	var got [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, b)
		if ct := r.Header.Get("Content-Type"); ct != "application/x-ndjson" {
			t.Errorf("Content-Type = %q, want ndjson", ct)
		}
	}))
	defer srv.Close()
	ConfigureAuditForward(srv.URL, "jsonl")
	t.Cleanup(func() { ConfigureAuditForward("", "") })

	AuditCommandExec(CommandAuditEntry{Timestamp: time.Now(), Tool: "system_exec", Decision: "allowed"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(got) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) == 0 {
		t.Fatal("SIEM received nothing")
	}
	if !strings.Contains(string(got[0]), `"decision":"allowed"`) {
		t.Errorf("forwarded body = %s, want the entry JSON", got[0])
	}
}

// TestAuditForwardDisabledSendsNothing pins "" disabling delivery.
func TestAuditForwardDisabledSendsNothing(t *testing.T) {
	tempAuditDir(t)
	ConfigureAuditForward("", "")
	AuditCommandExec(CommandAuditEntry{Timestamp: time.Now(), Tool: "x", Decision: "allowed"})
	// No server, no panic, nothing to assert beyond survival: delivery
	// is fire-and-forget and URL-gated.
}
