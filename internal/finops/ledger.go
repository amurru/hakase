// ledger.go - local JSONL usage ledger and live recording (FO-003).
//
// The ledger is metadata-only: per-turn usage counts plus the computed cost.
// Never prompt text. One JSON object per line at ~/.hakase/usage.jsonl
// (0600, flock, 5MBx5 rotation mirroring the audit trail), aggregated by
// scanning. Recording is opt-in via Configure (default off, following the
// hybrid_search default-off precedent): when disabled, Record is a no-op and
// behavior is byte-identical to before FinOps.
package finops

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"amurru/hakase/internal/util"
)

// LedgerMaxBytes triggers a rotation when the next write would exceed it.
var LedgerMaxBytes int64 = 5 * 1024 * 1024 // 5MB

// LedgerMaxBackups caps rotated files (usage.1.jsonl .. .N.jsonl).
var LedgerMaxBackups = 5

// LedgerFileName is the ledger file base name under the hakase home dir.
const LedgerFileName = "usage.jsonl"

// Settings tunes live recording. Zero value = disabled.
type Settings struct {
	Enabled   bool
	Path      string // ledger file; "" uses DefaultPath
	PerTool   bool   // persist per-tool deltas (default true when enabled)
	Table     PriceTable
	Overrides map[string]PriceEntry
	Budgets   BudgetCaps
}

var current atomic.Pointer[Settings]

// ModelNameFunc reports the active model name for attribution when an event
// carries no model label (the main turn loop). Set once at startup alongside
// hctx.CurrentModelFunc; nil means unknown (tokens-only).
var ModelNameFunc func() string

// Configure installs the live recording settings. Called once at startup
// from config load; tests install their own.
func Configure(s Settings) {
	if s.Table.Version == "" && s.Table.Entries == nil {
		s.Table = DefaultPriceTable()
	}
	if s.Path == "" {
		s.Path = DefaultPath()
	}
	current.Store(&s)
}

// Active returns the current settings, or nil when recording is disabled.
func Active() *Settings {
	s := current.Load()
	if s == nil || !s.Enabled {
		return nil
	}
	return s
}

// DefaultPath returns the ledger file path: $HAKASE_HOME/usage.jsonl,
// else ~/.hakase/usage.jsonl, else the relative file name.
func DefaultPath() string {
	if h := os.Getenv("HAKASE_HOME"); h != "" {
		return filepath.Join(h, LedgerFileName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return LedgerFileName
	}
	return filepath.Join(home, ".hakase", LedgerFileName)
}

// LedgerEntry is one recorded turn. Usage is kept whole so costs can be
// re-estimated when the price table updates (zero-cost re-estimate).
type LedgerEntry struct {
	Timestamp time.Time   `json:"timestamp"`
	SessionID string      `json:"session_id,omitempty"`
	Model     string      `json:"model,omitempty"`
	Provider  string      `json:"provider,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	Usage     UsageRecord `json:"usage"`
	Tools     []ToolDelta `json:"tools,omitempty"`
	CostUSD   float64     `json:"cost_usd"`
	// CostEstimated marks tokens-only rows (unknown model at write time).
	CostEstimated bool   `json:"cost_estimated,omitempty"`
	PriceTable    string `json:"price_table"`
}

var recordMu sync.Mutex

// Record appends one turn to the ledger. No-op when unconfigured/disabled
// or the record is zero. Best-effort: never breaks the turn.
func Record(sessionID string, rec UsageRecord, tools []ToolDelta) {
	s := Active()
	if s == nil || rec.IsZero() {
		return
	}
	cost, unknown := Cost(rec, rec.Model, s.Table, s.Overrides)
	entry := LedgerEntry{
		Timestamp:     time.Now().UTC(),
		SessionID:     sessionID,
		Model:         rec.Model,
		Provider:      rec.Provider,
		Reason:        rec.Reason,
		Usage:         rec,
		CostUSD:       cost,
		CostEstimated: unknown,
		PriceTable:    s.Table.Version,
	}
	if s.PerTool && len(tools) > 0 {
		entry.Tools = tools
	}
	appendEntry(s.Path, entry)
}

// appendEntry writes one JSON line with cross-process flock + rotation.
func appendEntry(path string, entry LedgerEntry) {
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	recordMu.Lock()
	defer recordMu.Unlock()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	lock, err := os.OpenFile(filepath.Join(dir, "usage.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		if ferr := util.FlockExclusive(lock); ferr == nil {
			defer func() {
				_ = util.FlockUnlock(lock)
				_ = lock.Close()
			}()
		} else {
			_ = lock.Close()
		}
	}

	rotateIfNeeded(path, int64(len(b)+1))

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = os.Chmod(path, 0o600)
	_, _ = f.Write(b)
	_, _ = f.Write([]byte("\n"))
}

// rotateIfNeeded renames usage.jsonl -> .1.jsonl (shifting older backups up,
// dropping beyond LedgerMaxBackups) when the next write would exceed
// LedgerMaxBytes. Caller must hold recordMu and the ledger flock.
func rotateIfNeeded(path string, nextWrite int64) {
	if LedgerMaxBytes <= 0 || LedgerMaxBackups <= 0 {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if fi.Size()+nextWrite <= LedgerMaxBytes {
		return
	}
	dir, base, ext := filepath.Dir(path), "usage", ".jsonl"
	oldest := filepath.Join(dir, base+"."+strconv.Itoa(LedgerMaxBackups)+ext)
	_ = os.Remove(oldest)
	for i := LedgerMaxBackups - 1; i >= 1; i-- {
		src := filepath.Join(dir, base+"."+strconv.Itoa(i)+ext)
		dst := filepath.Join(dir, base+"."+strconv.Itoa(i+1)+ext)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	_ = os.Rename(path, filepath.Join(dir, base+".1"+ext))
}

// Filter scopes a ledger read. Zero value reads everything.
type Filter struct {
	Since     time.Time
	SessionID string
	Model     string
	Reason    string
}

// Read scans the ledger file plus rotated backups, skipping malformed lines.
func Read(path string, f Filter) ([]LedgerEntry, error) {
	files := []string{path}
	dir := filepath.Dir(path)
	for i := 1; i <= LedgerMaxBackups; i++ {
		files = append(files, filepath.Join(dir, "usage."+strconv.Itoa(i)+".jsonl"))
	}
	var out []LedgerEntry
	for _, fp := range files {
		entries, err := readFile(fp, f)
		if err != nil {
			continue // missing backup is normal
		}
		out = append(out, entries...)
	}
	return out, nil
}

func readFile(path string, f Filter) ([]LedgerEntry, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var out []LedgerEntry
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 256*1024), 4*1024*1024)
	for sc.Scan() {
		var e LedgerEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		if !f.Since.IsZero() && e.Timestamp.Before(f.Since) {
			continue
		}
		if f.SessionID != "" && e.SessionID != f.SessionID {
			continue
		}
		if f.Model != "" && normalizeModel(e.Model) != normalizeModel(f.Model) {
			continue
		}
		if f.Reason != "" && e.Reason != f.Reason {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// Summary aggregates ledger rows for `hakase stats` (FO-004 surfaces read
// this; Phase 2 only provides the computation).
type Summary struct {
	Turns      int
	Tokens     int64
	Prompt     int64
	Candidates int64
	Cached     int64
	Thoughts   int64
	ToolUse    int64
	CostUSD    float64
	Estimated  int // rows with CostEstimated (tokens-only)
	ByModel    map[string]*Summary
	BySession  map[string]*Summary
	ByDay      map[string]*Summary
}

// Summarize folds entries. Nested maps stay nil-keyed simple: only the top
// level carries breakdowns (nested summaries have empty breakdown maps).
func Summarize(entries []LedgerEntry) Summary {
	sum := Summary{ByModel: map[string]*Summary{}, BySession: map[string]*Summary{}, ByDay: map[string]*Summary{}}
	at := func(m map[string]*Summary, k string) *Summary {
		s, ok := m[k]
		if !ok {
			s = &Summary{}
			m[k] = s
		}
		return s
	}
	add := func(s *Summary, e LedgerEntry, tokens int64) {
		s.Turns++
		s.Tokens += tokens
		s.Prompt += e.Usage.Prompt
		s.Candidates += e.Usage.Candidates
		s.Cached += e.Usage.Cached
		s.Thoughts += e.Usage.Thoughts
		s.ToolUse += e.Usage.ToolUse
		s.CostUSD += e.CostUSD
		if e.CostEstimated {
			s.Estimated++
		}
	}
	for _, e := range entries {
		tokens := e.Usage.TotalTokens()
		add(&sum, e, tokens)
		add(at(sum.ByModel, e.Model), e, tokens)
		add(at(sum.BySession, e.SessionID), e, tokens)
		add(at(sum.ByDay, e.Timestamp.Format("2006-01-02")), e, tokens)
	}
	return sum
}

// Reestimate recomputes an entry's cost against the current table (for
// zero-cost rows recorded under an unknown model). Returns the new cost and
// whether it is still estimated.
func Reestimate(e LedgerEntry, table PriceTable, overrides map[string]PriceEntry) (float64, bool) {
	return Cost(e.Usage, e.Model, table, overrides)
}
