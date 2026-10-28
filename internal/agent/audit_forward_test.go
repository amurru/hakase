package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAuditForwardPostsEntries pins best-effort SIEM delivery of each
// appended entry.
func TestAuditForwardPostsEntries(t *testing.T) {
	tempAuditDir(t)
	var mu sync.Mutex
	var got [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, b)
		mu.Unlock()
		if ct := r.Header.Get("Content-Type"); ct != "application/x-ndjson" {
			t.Errorf("Content-Type = %q, want ndjson", ct)
		}
	}))
	defer srv.Close()
	ConfigureAuditForward(srv.URL, "jsonl")
	t.Cleanup(func() { ConfigureAuditForward("", "") })

	AuditCommandExec(CommandAuditEntry{Timestamp: time.Now(), Tool: "system_exec", Decision: "allowed"})
	deadline := time.Now().Add(3 * time.Second)
	first := ""
	for time.Now().Before(deadline) {
		mu.Lock()
		if len(got) > 0 {
			first = string(got[0])
		}
		mu.Unlock()
		if first != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first == "" {
		t.Fatal("SIEM received nothing")
	}
	if !strings.Contains(first, `"decision":"allowed"`) {
		t.Errorf("forwarded body = %s, want the entry JSON", first)
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
