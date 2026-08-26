// Package sidekick implements a second, independently-configured LLM that
// runs alongside the primary orchestrator. It serves two purposes:
//
//   - Side-process (Ask): answer an isolated side-question via the
//     ask_sidekick tool or the /sidekick TUI command, without disturbing the
//     main agent's context or plan.
//   - Consult / watchdog (Consult): when enabled in watch or full mode, review
//     the CURRENT run's transcript after every model turn and surface concrete,
//     actionable items the orchestrator may have missed as quiet advisory notes
//     (inline chips, no notification dispatch).
//
// The package is intentionally decoupled from internal/agent (no import cycle):
// the caller builds the model.LLM via the provider factory and injects it.
package sidekick

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// Severity values for advisory notes (must match the watchdog output contract).
const (
	SeverityInfo       = "info"
	SeveritySuggestion = "suggestion"
	SeverityWarning    = "warning"
	SeverityCritical   = "critical"
)

// Note is a single advisory observation produced by the watchdog.
type Note struct {
	Severity string `json:"severity"`
	Text     string `json:"text"`
}

// Sidekick wraps a second LLM model plus the wiring needed to deliver notes.
type Sidekick struct {
	cfg      *config.SidekickConfig
	llm      model.LLM
	notifier interfaces.EventNotifier
	queue    *util.SidekickNoteQueue

	mu           sync.Mutex
	lastEval     time.Time
	evalsThisRun int
	seenHashes   map[string]bool
}

// New builds a Sidekick. Any of llm / notifier / queue may be nil (the
// relevant behavior is simply skipped). Use Enabled to gate behavior.
func New(cfg *config.SidekickConfig, llm model.LLM, notifier interfaces.EventNotifier, queue *util.SidekickNoteQueue) *Sidekick {
	return &Sidekick{
		cfg:        cfg,
		llm:        llm,
		notifier:   notifier,
		queue:      queue,
		seenHashes: make(map[string]bool),
	}
}

// Enabled reports whether the sidekick is active and has a usable model.
func (s *Sidekick) Enabled() bool {
	return s != nil && s.cfg != nil && s.cfg.EnabledWithModel() && s.llm != nil
}

// TranscriptWindow reports the configured transcript budget (in characters)
// for context passed to consultations and on-demand asks. Callers may pass
// the value straight into RecentTranscript.
func (s *Sidekick) TranscriptWindow() int {
	if s == nil || s.cfg == nil {
		return 0
	}
	return s.cfg.TranscriptWindowChars
}

// BeginRun resets per-run evaluation counters for a new invocation.
func (s *Sidekick) BeginRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evalsThisRun = 0
	s.seenHashes = make(map[string]bool)
	s.lastEval = time.Time{}
}

// EndRun clears per-run state.
func (s *Sidekick) EndRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evalsThisRun = 0
	s.seenHashes = make(map[string]bool)
}

// Ask runs an isolated side-question through the sidekick model and returns
// its concise answer.
func (s *Sidekick) Ask(ctx context.Context, prompt string) (string, error) {
	if !s.Enabled() {
		return "", fmt.Errorf("sidekick is not enabled")
	}
	sys := "You are a concise second-opinion assistant embedded alongside a primary research agent. " +
		"Answer the user's side-question directly and briefly, using only the information provided. " +
		"Do not perform actions or tool calls. Keep responses under 600 characters when possible."
	resp, err := s.callLLM(ctx, sys, prompt)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp), nil
}

// Consult reviews the current run's transcript and, when something actionable
// is found, enqueues advisory notes (for next-turn injection) and/or pushes
// them to the notifier. It is a no-op when disabled, not in watch/full mode,
// debounced, or past the per-run evaluation budget.
func (s *Sidekick) Consult(ctx context.Context, sessionID, transcript string) ([]Note, error) {
	if !s.Enabled() {
		return nil, nil
	}
	mode := s.cfg.EffectiveMode()
	if mode != config.ModeWatch && mode != config.ModeFull {
		return nil, nil
	}
	s.mu.Lock()
	if time.Since(s.lastEval) < time.Duration(s.cfg.EvaluateDebounceSeconds)*time.Second {
		s.mu.Unlock()
		return nil, nil
	}
	if s.evalsThisRun >= s.cfg.MaxEvaluationsPerRun {
		s.mu.Unlock()
		return nil, nil
	}
	s.lastEval = time.Now()
	s.evalsThisRun++
	s.mu.Unlock()

	if len(transcript) > s.cfg.TranscriptWindowChars {
		transcript = transcript[len(transcript)-s.cfg.TranscriptWindowChars:]
	}

	sys := "You are a watchful second-opinion reviewer observing a primary agent's ongoing work. " +
		"Review ONLY the transcript of the CURRENT run. Identify concrete, actionable items the agent " +
		"may have MISSED, risks, uncovered edge cases, or unverified assumptions. Do not repeat what the " +
		"agent already did. Be terse. Output ONLY a JSON array of objects " +
		"{\"severity\": \"info|suggestion|warning|critical\", \"text\": \"...\"}. " +
		"Use severity \"critical\" only for correctness or safety risks. If nothing is worth flagging, output []."

	raw, err := s.callLLM(ctx, sys, transcript)
	if err != nil {
		return nil, err
	}
	notes := parseNotes(raw, s.cfg)
	if len(notes) == 0 {
		return nil, nil
	}

	s.mu.Lock()
	var out []Note
	for _, n := range notes {
		if n.Text == "" {
			continue
		}
		if len(n.Text) > s.cfg.MaxNoteChars {
			n.Text = n.Text[:s.cfg.MaxNoteChars]
		}
		h := hashNote(n)
		if s.seenHashes[h] {
			continue
		}
		s.seenHashes[h] = true
		out = append(out, n)
		if len(out) >= s.cfg.MaxNotesPerTurn {
			break
		}
	}
	s.mu.Unlock()

	for _, n := range out {
		if s.queue != nil {
			s.queue.Add(util.SidekickNote{Severity: n.Severity, Text: n.Text})
		}
		if s.notifier != nil {
			s.notifier.SidekickEvent(sessionID, n.Severity, n.Text)
		}
	}
	return out, nil
}

// RunTranscript builds a plain-text transcript of the current run's events
// (filtered to the active invocation) from an ADK agent context.
func RunTranscript(ctx agent.Context) string {
	sess := ctx.Session()
	if sess == nil {
		return ""
	}
	invID := ctx.InvocationID()
	var sb strings.Builder
	for ev := range sess.Events().All() {
		if invID != "" && ev.InvocationID != invID {
			continue
		}
		if ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p != nil && p.Text != "" && !p.Thought {
				sb.WriteString(p.Text)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

func (s *Sidekick) callLLM(ctx context.Context, system, user string) (string, error) {
	req := &model.LLMRequest{
		Model: s.llm.Name(),
		Contents: []*genai.Content{
			genai.NewContentFromText(user, genai.RoleUser),
		},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText(system, genai.RoleUser),
		},
	}
	var sb strings.Builder
	for resp, err := range s.llm.GenerateContent(ctx, req, false) {
		if err != nil {
			return "", err
		}
		if resp != nil && resp.Content != nil {
			for _, p := range resp.Content.Parts {
				if p != nil && p.Text != "" && !p.Thought {
					sb.WriteString(p.Text)
				}
			}
		}
	}
	return sb.String(), nil
}

// parseNotes extracts a JSON array of notes from the model output, tolerating
// prose framing. Invalid severities are normalized to info.
func parseNotes(raw string, cfg *config.SidekickConfig) []Note {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "["); i >= 0 {
		raw = raw[i:]
	}
	if j := strings.LastIndex(raw, "]"); j >= 0 {
		raw = raw[:j+1]
	}
	var notes []Note
	if err := json.Unmarshal([]byte(raw), &notes); err != nil {
		return nil
	}
	for i := range notes {
		switch notes[i].Severity {
		case SeverityInfo, SeveritySuggestion, SeverityWarning, SeverityCritical:
		default:
			notes[i].Severity = SeverityInfo
		}
		if len(notes[i].Text) > cfg.MaxNoteChars {
			notes[i].Text = notes[i].Text[:cfg.MaxNoteChars]
		}
	}
	return notes
}

func hashNote(n Note) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(n.Severity+":"+n.Text)))
}
