package agent

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
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

// AuditCSVHeader is the v1 metadata-only export header: no command bodies,
// args, or reasons (PM-004 "CSV drops body/lineage"). Kept so a strict
// parser of an older export still recognizes its shape.
const AuditCSVHeader = "timestamp,tool,decision,risk,actor,trace_id,policy_source,policy_effect,prev_hash,entry_hash"

// AuditCSVHeaderV2 appends the Flow Guard metadata columns (FG-006/M2):
// the label snapshot, the args hash and the row schema version. Columns
// are appended at the end only, so a v1 export keeps its shape for a v1
// strict parser while a version-aware parser reads both schemas.
const AuditCSVHeaderV2 = AuditCSVHeader + ",flow_label,flow_args_hash,flow_version"

// AuditCSVVersion is the flow metadata schema version carried in the
// flow_version column of a v2 row.
const AuditCSVVersion = "2"

// audit CSV schema versions.
const (
	auditCSVSchemaV1 = 1
	auditCSVSchemaV2 = 2
)

// AuditCSVRecord is one parsed metadata-only audit row.
type AuditCSVRecord struct {
	Timestamp    string
	Tool         string
	Decision     string
	Risk         string
	Actor        string
	TraceID      string
	PolicySource string
	PolicyEffect string
	PrevHash     string
	EntryHash    string
	// Flow columns (v2 only, empty in a v1 row).
	FlowLabel    string
	FlowArgsHash string
	FlowVersion  string
}

// AuditCSVRow renders one entry's metadata-only CSV row (v2 shape: the
// flow columns are appended at the end and stay empty for entries
// without a flow decision).
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
		q(e.FlowLabel), q(e.FlowArgsHash), AuditCSVVersion,
	}, ",")
}

// auditCSVColumnsV1 and auditCSVColumnsV2 are the expected column
// names, in order. The parser maps positions, so a header is only
// accepted when every name matches its position.
var (
	auditCSVColumnsV1 = strings.Split(AuditCSVHeader, ",")
	auditCSVColumnsV2 = strings.Split(AuditCSVHeaderV2, ",")
)

// auditCSVHeaderMatches reports whether every column name matches the
// expected schema at its position.
func auditCSVHeaderMatches(header, want []string) bool {
	if len(header) != len(want) {
		return false
	}
	for i := range want {
		if header[i] != want[i] {
			return false
		}
	}
	return true
}

// auditCSVSchema returns the schema version of an audit CSV header. The
// column names AND their positions must match: a reordered or renamed
// header is refused rather than parsed positionally into the wrong
// fields.
func auditCSVSchema(header []string) (int, error) {
	switch {
	case auditCSVHeaderMatches(header, auditCSVColumnsV1):
		return auditCSVSchemaV1, nil
	case auditCSVHeaderMatches(header, auditCSVColumnsV2):
		return auditCSVSchemaV2, nil
	}
	return 0, fmt.Errorf("audit csv: unknown header %q", strings.Join(header, ","))
}

// ParseAuditCSV parses a metadata-only audit CSV export. The header
// selects the schema: a v1 export (10 columns) and a v2 export (13
// columns) are both accepted, and every row must match the header
// (a strict field count per row, like the old parser).
func ParseAuditCSV(data []byte) ([]AuditCSVRecord, error) {
	r := csv.NewReader(bytes.NewReader(data))
	// The field count is checked per row below, against the header.
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("audit csv: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	header := records[0]
	version, err := auditCSVSchema(header)
	if err != nil {
		return nil, err
	}
	out := make([]AuditCSVRecord, 0, len(records)-1)
	for i, rec := range records[1:] {
		if len(rec) != len(header) {
			return nil, fmt.Errorf("audit csv: row %d has %d fields, want %d", i+2, len(rec), len(header))
		}
		row := AuditCSVRecord{
			Timestamp:    rec[0],
			Tool:         rec[1],
			Decision:     rec[2],
			Risk:         rec[3],
			Actor:        rec[4],
			TraceID:      rec[5],
			PolicySource: rec[6],
			PolicyEffect: rec[7],
			PrevHash:     rec[8],
			EntryHash:    rec[9],
		}
		if version == auditCSVSchemaV2 {
			// The row declares its own schema version: an empty or
			// unknown one is refused rather than half-read.
			if rec[12] != AuditCSVVersion {
				return nil, fmt.Errorf("audit csv: row %d has flow version %q, want %q", i+2, rec[12], AuditCSVVersion)
			}
			row.FlowLabel = rec[10]
			row.FlowArgsHash = rec[11]
			row.FlowVersion = rec[12]
		}
		out = append(out, row)
	}
	return out, nil
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
