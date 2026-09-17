// harvest.go - session harvester for the SkillOpt-Sleep loop (plan Phase 2,
// SL-020): walk persisted sessions, extract a privacy-preserving digest per
// session (prompts, finals, skills used, feedback signals, audit-log tool
// names), redact and truncate everything, and write the digest file for the
// miner.
//
// Data boundary (plan section 5): harvest is local read-only. Nothing here
// calls a provider. Every string is redacted BEFORE truncation (SL-004
// ordering), attachment bytes/names are never extracted (SL-011c: not even
// the path is copied into digests), and audit-log arguments are joined only
// behind explicit flags (default false).
package sleep

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"amurru/hakase/internal/session"
	"amurru/hakase/internal/util"
)

// Per-field truncation tiers (plan: 200-4000 chars by tier). Applied after
// redaction, so a cut can never re-expose a secret.
const (
	TruncateTitle    = 200
	TruncatePrompt   = 2000
	TruncateFinal    = 4000
	TruncateSkillRef = 64
	TruncateSignal   = 64
	TruncateAuditArg = 200
)

// Session harvest defaults (SL-021 caps max_sessions_per_night:120; the
// harvester applies the same ceiling so the digest file is bounded).
const (
	DefaultLookbackHours = 72
	DefaultMaxSessions   = 120
)

// SessionDigest is the privacy-preserving summary of one harvested session.
// It is the miner's (SL-021) input unit: prompts carry user intent, finals
// carry the agent's per-turn outcome, skills_used feeds skill clustering,
// and feedback_signals mark turns where the user corrected or approved.
type SessionDigest struct {
	SessionID       string    `json:"session_id"`
	Title           string    `json:"title,omitempty"`
	ProjectID       string    `json:"project_id,omitempty"`
	Archived        bool      `json:"archived,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	TurnCount       int       `json:"turn_count"`
	Prompts         []string  `json:"prompts"`
	Finals          []string  `json:"finals,omitempty"`
	SkillsUsed      []string  `json:"skills_used,omitempty"`
	FeedbackSignals []string  `json:"feedback_signals,omitempty"`
	ToolsUsed       []string  `json:"tools_used,omitempty"`
	// AuditArgs carry redacted "command args..." strings from the exec
	// audit log and exist only when HarvestOpts.IncludeAuditArgs is set.
	AuditArgs []string `json:"audit_args,omitempty"`
}

// HarvestFile is the on-disk shape of a harvest output.
type HarvestFile struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Cutoff      time.Time       `json:"cutoff"`
	SessionsDir string          `json:"sessions_dir"`
	Sessions    []SessionDigest `json:"sessions"`
}

// HarvestOpts tunes one harvest pass. Zero values mean the documented
// defaults; Unredacted is the resolved SL-004 opt-out (never derive it from
// config directly - go through ResolveRedaction).
type HarvestOpts struct {
	// SessionsDir is the session store directory (usually ./sessions).
	SessionsDir string
	// AuditLogPath is the exec audit log (logs/exec-audit.jsonl). Empty
	// disables the join.
	AuditLogPath string
	// ProjectID restricts the walk to one registered project. Empty = all.
	ProjectID string
	// IncludeArchived also walks archived sessions (default: skip them).
	IncludeArchived bool
	// LookbackHours is the first-run window (default 72). Ignored when
	// Checkpoint is set.
	LookbackHours int
	// Checkpoint (last_harvest) overrides LookbackHours when non-zero:
	// only turns at or after this instant are harvested.
	Checkpoint time.Time
	// IncludeAuditArgs joins redacted command lines from the audit log.
	// Default false (SL-004): tool names always, args only on request.
	IncludeAuditArgs bool
	// IncludeAuditOutputs reserves the config surface. logs/exec-audit.jsonl
	// records commands and exit codes, not captured outputs, so nothing is
	// joined today; the flag exists so enabling a future capture does not
	// change the config schema.
	IncludeAuditOutputs bool
	// Unredacted disables secret redaction (SL-004 opt-out). Harvest is a
	// local write, but digests feed prompts downstream, so callers must
	// resolve this through ResolveRedaction, not hand-set it.
	Unredacted bool
	// MaxSessions caps the walk (newest first). Zero = DefaultMaxSessions.
	MaxSessions int
	// Now pins "today" for tests.
	Now func() time.Time
}

// harvestAuditLine mirrors agent.CommandAuditEntry without importing
// internal/agent (import-direction rule: sleep never imports agent).
type harvestAuditLine struct {
	Timestamp time.Time `json:"timestamp"`
	Tool      string    `json:"tool"`
	Command   string    `json:"command"`
	Args      []string  `json:"args"`
	Decision  string    `json:"decision"`
}

// skillMentionRe extracts a skill name from narrative or JSON-shaped
// load_markdown_skill mentions (agent texts quote the call in both forms).
// A bare mention with no extractable name is dropped: the miner treats
// unnamed usage as the managed catch-all anyway.
var skillMentionRe = regexp.MustCompile(`(?i)load_markdown_skill[\s\S]{0,160}?name['"]?\s*[:=]?\s*['"]([a-z0-9][a-z0-9-]{0,63})['"]`)

// feedbackLexicon is the user-feedback lexicon (best-effort signal, not
// ground truth). Patterns are checked case-insensitively against the raw
// prompt; matched pattern names are recorded in FeedbackSignals.
var feedbackLexicon = []struct {
	name string
	re   *regexp.Regexp
}{
	{"wrong", regexp.MustCompile(`(?i)\bwrong\b`)},
	{"incorrect", regexp.MustCompile(`(?i)\bincorrect\b`)},
	{"not-what-i-asked", regexp.MustCompile(`(?i)\bnot what i\b`)},
	{"thats-not", regexp.MustCompile(`(?i)that'?s not\b`)},
	{"didnt-work", regexp.MustCompile(`(?i)\b(didn'?t|did not|doesn'?t|does not|never)\s+work`)},
	{"still-failing", regexp.MustCompile(`(?i)\bstill\s+(fail|broken|error)`)},
	{"revert", regexp.MustCompile(`(?i)\b(revert|undo|rollback)\b`)},
	{"try-again", regexp.MustCompile(`(?i)\b(try again|start over|redo)\b`)},
	{"not-right", regexp.MustCompile(`(?i)\bnot (right|correct|quite)\b`)},
	{"broken", regexp.MustCompile(`(?i)\bbroken\b`)},
	{"perfect", regexp.MustCompile(`(?i)\b(perfect|exactly what)\b`)},
	{"works", regexp.MustCompile(`(?i)\b(that|it|this) works?\b`)},
	{"thanks", regexp.MustCompile(`(?i)\b(thanks|thank you)\b`)},
	{"praise", regexp.MustCompile(`(?i)\b(great|awesome|nice work|good job)\b`)},
}

// Harvest walks the session store and produces digests. It never writes
// inside the sessions dir and never touches attachments.
func Harvest(opts HarvestOpts) (HarvestFile, error) {
	now := time.Now()
	if opts.Now != nil {
		now = opts.Now()
	}
	sessionsDir := opts.SessionsDir
	if sessionsDir == "" {
		sessionsDir = session.Dir
	}
	lookback := opts.LookbackHours
	if lookback <= 0 {
		lookback = DefaultLookbackHours
	}
	cutoff := opts.Checkpoint
	if cutoff.IsZero() {
		cutoff = now.Add(-time.Duration(lookback) * time.Hour)
	}
	maxSessions := opts.MaxSessions
	if maxSessions <= 0 {
		maxSessions = DefaultMaxSessions
	}

	store, err := session.NewSessionStore(sessionsDir)
	if err != nil {
		return HarvestFile{}, fmt.Errorf("open session store: %w", err)
	}

	summaries, err := store.List()
	if err != nil {
		return HarvestFile{}, fmt.Errorf("list sessions: %w", err)
	}
	if opts.IncludeArchived {
		archived, err := store.ListArchived()
		if err != nil {
			return HarvestFile{}, fmt.Errorf("list archived sessions: %w", err)
		}
		summaries = append(summaries, archived...)
	}

	var audit []harvestAuditLine
	if opts.AuditLogPath != "" {
		audit = loadAuditLines(opts.AuditLogPath)
	}

	// Newest first so the cap keeps the most recent sessions.
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})

	file := HarvestFile{GeneratedAt: now.UTC(), Cutoff: cutoff.UTC(), SessionsDir: sessionsDir}
	for _, sum := range summaries {
		if len(file.Sessions) >= maxSessions {
			break
		}
		if opts.ProjectID != "" && sum.ProjectID != opts.ProjectID {
			continue
		}
		if sum.UpdatedAt.Before(cutoff) {
			// Sorted newest first: everything below is older.
			break
		}
		sess, err := store.Load(sum.ID)
		if err != nil {
			continue // unreadable/corrupt session file: skip, never abort
		}
		d := digestSession(sess, cutoff, audit, opts)
		if d == nil {
			continue
		}
		file.Sessions = append(file.Sessions, *d)
	}
	return file, nil
}

// digestSession extracts one session's turns. Returns nil when no turn
// falls inside the window (nothing new to mine).
func digestSession(sess *session.Session, cutoff time.Time, audit []harvestAuditLine, opts HarvestOpts) *SessionDigest {
	d := &SessionDigest{
		SessionID: sess.ID,
		Title:     truncStr(opts.redact(sess.Title), TruncateTitle),
		ProjectID: sess.ProjectID,
		Archived:  sess.Archived,
		CreatedAt: sess.CreatedAt.UTC(),
		UpdatedAt: sess.UpdatedAt.UTC(),
	}
	var windowStart time.Time
	for _, msg := range sess.Messages {
		kind := strings.ToLower(strings.TrimSpace(msg.Kind))
		// Tool transcripts exist for compaction only and sidekick notes are
		// watchdog meta-noise: neither is a task outcome. Summaries are
		// agent-authored session digests and count as finals.
		if kind == string(session.MessageKindToolCall) || kind == string(session.MessageKindToolResult) ||
			kind == string(session.MessageKindSidekick) {
			continue
		}
		if msg.Timestamp.Before(cutoff) {
			continue
		}
		switch {
		case msg.Role == "user":
			prompt := strings.TrimSpace(msg.Content)
			if prompt == "" {
				continue
			}
			d.TurnCount++
			d.Prompts = append(d.Prompts, truncStr(opts.redact(prompt), TruncatePrompt))
			for _, sig := range feedbackLexicon {
				if sig.re.MatchString(prompt) {
					d.FeedbackSignals = append(d.FeedbackSignals, sig.name)
				}
			}
			if windowStart.IsZero() {
				windowStart = msg.Timestamp
			}
		case msg.Role == "agent" && (kind == "" || kind == string(session.MessageKindText) || kind == string(session.MessageKindSummary)):
			final := strings.TrimSpace(msg.Content)
			if final == "" {
				continue
			}
			d.Finals = append(d.Finals, truncStr(opts.redact(final), TruncateFinal))
		}
	}
	if d.TurnCount == 0 {
		return nil
	}
	for _, text := range append(append([]string{}, d.Prompts...), d.Finals...) {
		for _, name := range skillMentionRe.FindAllStringSubmatch(text, -1) {
			d.SkillsUsed = appendUnique(d.SkillsUsed, name[1])
		}
	}
	sort.Strings(d.SkillsUsed)
	if opts.AuditLogPath != "" && !windowStart.IsZero() {
		joinAudit(d, audit, windowStart, sess.UpdatedAt, opts)
	}
	return d
}

// joinAudit attaches audit-log evidence whose timestamp falls inside the
// harvested turn window. Best-effort by design: the audit log carries no
// session ID, so overlapping sessions can share entries.
func joinAudit(d *SessionDigest, audit []harvestAuditLine, start, end time.Time, opts HarvestOpts) {
	for _, e := range audit {
		if e.Timestamp.Before(start) || e.Timestamp.After(end) {
			continue
		}
		if e.Tool != "" {
			d.ToolsUsed = appendUnique(d.ToolsUsed, truncStr(opts.redact(e.Tool), TruncateSkillRef))
		}
		if opts.IncludeAuditArgs {
			line := strings.TrimSpace(e.Command + " " + strings.Join(e.Args, " "))
			if line != "" {
				d.AuditArgs = append(d.AuditArgs, truncStr(opts.redact(line), TruncateAuditArg))
			}
		}
	}
	sort.Strings(d.ToolsUsed)
}

// loadAuditLines reads the exec audit log best-effort: missing file or
// corrupt lines are skipped (an absent log means "no evidence", not "abort").
func loadAuditLines(path string) []harvestAuditLine {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []harvestAuditLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e harvestAuditLine
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.Timestamp.IsZero() {
			continue
		}
		lines = append(lines, e)
	}
	return lines
}

// redact applies SL-004 redaction unless the caller resolved the explicit
// unredacted opt-out. Redaction ALWAYS runs before truncation (call order
// in the extractors above).
func (opts HarvestOpts) redact(s string) string {
	if opts.Unredacted {
		return s
	}
	out, _ := util.RedactSecrets(s)
	return out
}

// truncStr caps s to max runes with an ellipsis marker.
func truncStr(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// appendUnique appends v when not present.
func appendUnique(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// WriteHarvestFile writes the digest file atomically (tmp+rename, 0600 in a
// 0700 dir) per the SL-005 writer rules.
func WriteHarvestFile(path string, file HarvestFile) error {
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o600)
}

// writeFileAtomic writes data to path via a temp file + rename in the
// target directory, with explicit perms on both file and dir.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	tmp, err := os.CreateTemp(dir, ".sleep-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}
