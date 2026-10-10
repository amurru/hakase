package agent

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Hash-chained audit trail (docs/permissions/ PM-004).
//
// Every entry links to the previous entry's hash:
// entry_hash = sha256hex(prev_hash + "\n" + canonical entry JSON) where
// the canonical view zeroes both hash fields. The first entry links to
// auditGenesis. The link is captured before rotation, so the chain spans
// rotated backups; VerifyAuditChain re-hashes oldest to newest.

// auditGenesis is the prev_hash of the first chained entry. Pre-chain
// entries (written before this shipped) carry no hashes and are skipped
// by verification, never failed.
const auditGenesis = "GENESIS"

// chainView zeroes the hash fields for canonical hashing.
func chainView(e CommandAuditEntry) CommandAuditEntry {
	e.PrevHash = ""
	e.EntryHash = ""
	return e
}

// auditHMACKey, when set, turns the chain into an HMAC chain: only a
// holder of the key can extend or rewrite history undetectably. Plain
// sha256 otherwise (self-consistency only, L1). Configured via
// audit.hmac_key_file at startup; the verify CLI takes --hmac-key-file.
var auditHMACKey []byte

// ConfigureAuditHMACKey sets the chain HMAC key (nil/empty disables).
func ConfigureAuditHMACKey(key []byte) {
	cp := append([]byte(nil), key...)
	if len(cp) == 0 {
		cp = nil
	}
	auditHMACKey = cp
}

// hashWithKey computes the entry hash over prev + canonical bytes
// (HMAC when key is set, plain sha256 otherwise).
func hashWithKey(prev string, canonical, key []byte) string {
	if len(key) > 0 {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(prev + "\n"))
		mac.Write(canonical)
		return hex.EncodeToString(mac.Sum(nil))
	}
	sum := sha256.Sum256([]byte(prev + "\n" + string(canonical)))
	return hex.EncodeToString(sum[:])
}

// chainEntryHash computes the entry hash with the active chain key.
func chainEntryHash(prev string, canonical []byte) string {
	return hashWithKey(prev, canonical, auditHMACKey)
}

// tailEntryHash returns the EntryHash of the last parseable line in path
// (scanning at most the trailing 64KB), or auditGenesis when the file is
// missing, empty, or has no chained entries yet. A torn trailing line
// from a crashed writer is skipped, never trusted.
func tailEntryHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return auditGenesis
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return auditGenesis
	}
	const window = 64 * 1024
	off := fi.Size() - window
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return auditGenesis
	}
	tail, err := io.ReadAll(io.LimitReader(f, window+1))
	if err != nil {
		return auditGenesis
	}
	lines := strings.Split(string(tail), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var e CommandAuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.EntryHash != "" {
			return e.EntryHash
		}
	}
	return auditGenesis
}

// auditChainFiles lists existing chain files oldest first: numbered
// backups descending (exec-audit.N.jsonl is the oldest) then the live file.
func auditChainFiles(dir string) []string {
	var out []string
	var nums []int
	for i := 1; i <= AuditMaxBackups; i++ {
		p := filepath.Join(dir, fmt.Sprintf("exec-audit.%d.jsonl", i))
		if _, err := os.Stat(p); err == nil {
			nums = append(nums, i)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(nums)))
	for _, i := range nums {
		out = append(out, filepath.Join(dir, fmt.Sprintf("exec-audit.%d.jsonl", i)))
	}
	if _, err := os.Stat(filepath.Join(dir, "exec-audit.jsonl")); err == nil {
		out = append(out, filepath.Join(dir, "exec-audit.jsonl"))
	}
	return out
}

// VerifyAuditChain re-hashes every chained entry oldest to newest and
// checks each prev link. Pre-chain entries (no hashes) are skipped.
// It returns the entry count and the first break found, if any.
func VerifyAuditChain(dir string) (int, error) {
	want := ""
	n := 0
	for _, path := range auditChainFiles(dir) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return n, fmt.Errorf("audit verify: %s: %w", path, err)
		}
		for ln, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e CommandAuditEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				return n, fmt.Errorf("audit verify: %s:%d: unparseable: %w", path, ln+1, err)
			}
			if e.EntryHash == "" {
				continue // pre-chain entry: skip, never fail
			}
			prev := e.PrevHash
			if prev == "" {
				prev = auditGenesis
			}
			canonical, err := json.Marshal(chainView(e))
			if err != nil {
				return n, fmt.Errorf("audit verify: %s:%d: %w", path, ln+1, err)
			}
			// Per-entry algorithm: HMAC entries need the active key,
			// plain entries verify with sha256 (pre-HMAC history keeps
			// verifying after the switch).
			var key []byte
			switch e.HashAlg {
			case "":
			case hashAlgHMAC:
				if len(auditHMACKey) == 0 {
					return n, fmt.Errorf("audit verify: %s:%d: HMAC entry but no key configured (--hmac-key-file)", path, ln+1)
				}
				key = auditHMACKey
			default:
				return n, fmt.Errorf("audit verify: %s:%d: unknown hash_alg %q", path, ln+1, e.HashAlg)
			}
			if got := hashWithKey(prev, canonical, key); got != e.EntryHash {
				return n, fmt.Errorf("audit verify: %s:%d: entry hash mismatch (tampered?)", path, ln+1)
			}
			if want != "" && prev != want {
				return n, fmt.Errorf("audit verify: %s:%d: chain break (prev link mismatch)", path, ln+1)
			}
			want = e.EntryHash
			n++
		}
	}
	return n, nil
}

// ReadAuditEntries streams chained (and pre-chain) entries oldest first,
// filtered to Timestamp >= since (zero since = all).
func ReadAuditEntries(dir string, since time.Time) ([]CommandAuditEntry, error) {
	var out []CommandAuditEntry
	for _, path := range auditChainFiles(dir) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("audit read: %s: %w", path, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e CommandAuditEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				return nil, fmt.Errorf("audit read: %s: unparseable line: %w", path, err)
			}
			if since.IsZero() || !e.Timestamp.Before(since) {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// AuditCSVHeader is the metadata-only export header: no command bodies,
// args, or reasons (PM-004 "CSV drops body/lineage").
const AuditCSVHeader = "timestamp,tool,decision,risk,actor,trace_id,policy_source,policy_effect,prev_hash,entry_hash"

// AuditCSVRow renders one entry's metadata-only CSV row.
func AuditCSVRow(e CommandAuditEntry) string {
	q := func(s string) string {
		if strings.ContainsAny(s, ",\"\n\r") {
			return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
		}
		return s
	}
	return strings.Join([]string{
		e.Timestamp.UTC().Format(time.RFC3339),
		q(e.Tool), q(e.Decision), q(e.Risk), q(e.Actor), q(e.TraceID),
		q(e.PolicyRule.Source), q(e.PolicyRule.Effect),
		q(e.PrevHash), q(e.EntryHash),
	}, ",")
}

// AuditApprovalAnswer records who answered a gate prompt: the actor is
// the web username or channel user ID at the answering transport, and the
// trace is the approval (gate) ID. Decision is approved or denied.
func AuditApprovalAnswer(gateID, tool string, approved bool, actor, transport string) {
	decision := "denied"
	if approved {
		decision = "approved"
	}
	if tool == "" {
		tool = "approval"
	}
	AuditCommandExec(CommandAuditEntry{
		Timestamp: time.Now(),
		Tool:      tool,
		Command:   "answer:" + gateID,
		SessionID: "",
		Decision:  decision,
		Reason:    "answered via " + transport,
		Actor:     actor,
		TraceID:   gateID,
	})
}

// noRedirectClient refuses to follow HTTP redirects (L3): the SIEM
// endpoint stays exactly where configured.
func noRedirectClient(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// SIEM forwarding (PM-004 audit.forward): best-effort POST of each entry
// JSON line. Configured once at startup; failures are swallowed (a sick
// SIEM must never break the agent).

var (
	forwardMu     sync.Mutex
	forwardURL    string
	forwardFormat string
)

// ConfigureAuditForward sets the SIEM endpoint ("" disables). Format is
// jsonl (ndjson body) or json; anything else falls back to jsonl.
func ConfigureAuditForward(url, format string) {
	forwardMu.Lock()
	defer forwardMu.Unlock()
	forwardURL, forwardFormat = url, format
}

func forwardAuditEntry(line []byte) {
	forwardMu.Lock()
	url, format := forwardURL, forwardFormat
	forwardMu.Unlock()
	if url == "" {
		return
	}
	ct := "application/x-ndjson"
	if format == "json" {
		ct = "application/json"
	}
	go func() {
		client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: noRedirectClient}
		req, err := http.NewRequest("POST", url, bytes.NewReader(append([]byte(nil), line...)))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", ct)
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
}
