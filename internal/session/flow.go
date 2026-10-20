// flow.go - Flow Guard label registry + persistence helpers (FG-001/006).
//
// In-memory per-run registry (sync.RWMutex + CAS on Version, same
// discipline as PauseRegistry: TUI+web share state and ADK can run
// read+send concurrently) plus Session.Flow/FlowApplied persistence so
// resume and durable-replay keep restrictions. Label travels in ctx for
// delegates (Phase 2); this file carries the std-context helpers.
package session

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"amurru/hakase/internal/permissions"
)

// FlowKey encodes the idempotency key canonically and losslessly:
// length-prefixed "seq:<dec>:<byte-len-of-tool-id>:<tool-id>" (tool-id is
// caller-supplied and may contain any bytes including ":").
func FlowKey(seq int64, toolCallID string) string {
	return fmt.Sprintf("seq:%d:%d:%s", uint64(seq), len(toolCallID), toolCallID)
}

// ParseFlowKey round-trips FlowKey, including empty tool-ids and the
// max-uint64 boundary (int64 -1 encodes as max-uint64 and decodes back).
func ParseFlowKey(key string) (int64, string, error) {
	rest, ok := strings.CutPrefix(key, "seq:")
	if !ok {
		return 0, "", fmt.Errorf("session: invalid flow key %q (missing seq: prefix)", key)
	}
	a, b, ok := strings.Cut(rest, ":")
	if !ok {
		return 0, "", fmt.Errorf("session: invalid flow key %q", key)
	}
	c, toolID, ok := strings.Cut(b, ":")
	if !ok {
		return 0, "", fmt.Errorf("session: invalid flow key %q", key)
	}
	u, err := strconv.ParseUint(a, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("session: invalid flow key %q: %w", key, err)
	}
	n, err := strconv.Atoi(c)
	if err != nil || n < 0 {
		return 0, "", fmt.Errorf("session: invalid flow key %q (bad length)", key)
	}
	if len(toolID) != n {
		return 0, "", fmt.Errorf("session: invalid flow key %q (length mismatch)", key)
	}
	return int64(u), toolID, nil
}

// ConservativeLabel is the fail-closed label for pre-v2 sessions resumed
// without flow history (M1): never the widest. Callers audit
// flow_recomputed and show the one-time resume notice.
func ConservativeLabel() permissions.SessionLabel {
	return permissions.SessionLabel{Trust: permissions.TrustSuspicious, Audience: permissions.AudienceInternal}
}

// FlowResumeNotice returns the one-time denial notice for a resumed old
// session, or "" when nothing was recomputed.
func FlowResumeNotice(recomputed bool) string {
	if !recomputed {
		return ""
	}
	return "note: pre-v2 session resumed with conservative label"
}

// EnsureFlow initializes a nil Flow to the start label and returns it.
// New sessions start widest; old files go through RecomputeFlow instead.
func (s *Session) EnsureFlow() *permissions.SessionLabel {
	if s.Flow == nil {
		l := permissions.StartLabel()
		s.Flow = &l
	}
	return s.Flow
}

// RecomputeFlow backfills Flow for pre-v2 session files (M1/H3): fold is
// impossible without the v2 rules that produced the history, so any
// session with tool history (or without) resumes conservative, never
// widest. Returns true when it recomputed (caller audits flow_recomputed
// and shows FlowResumeNotice once).
func (s *Session) RecomputeFlow() bool {
	if s.Flow != nil {
		return false
	}
	l := ConservativeLabel()
	s.Flow = &l
	if s.FlowApplied == nil {
		s.FlowApplied = map[string]bool{}
	}
	return true
}

// MarkFlowApplied records an admitted fold key on the session. Returns
// false when the key was already applied (no-op re-admit).
func (s *Session) MarkFlowApplied(key string) bool {
	if s.FlowApplied == nil {
		s.FlowApplied = map[string]bool{}
	}
	if s.FlowApplied[key] {
		return false
	}
	s.FlowApplied[key] = true
	return true
}

// flowLabelCtxKey carries the label for delegate inheritance (Phase 2).
type flowLabelCtxKey struct{}

// WithFlowLabel returns ctx carrying a copy of l (nil clears).
func WithFlowLabel(ctx context.Context, l *permissions.SessionLabel) context.Context {
	if l == nil {
		return context.WithValue(ctx, flowLabelCtxKey{}, nil)
	}
	cp := *l
	return context.WithValue(ctx, flowLabelCtxKey{}, &cp)
}

// FlowLabelFromCtx returns the ctx-carried label copy, or nil when absent.
func FlowLabelFromCtx(ctx context.Context) *permissions.SessionLabel {
	if ctx == nil {
		return nil
	}
	l, _ := ctx.Value(flowLabelCtxKey{}).(*permissions.SessionLabel)
	if l == nil {
		return nil
	}
	cp := *l
	return &cp
}

// FlowRegistry is the per-run in-memory label store. All mutations hold
// the write mutex and compare-and-swap on Version so concurrent ADK
// read+send folds cannot interleave (senior review H2/H3).
type FlowRegistry struct {
	mu          sync.RWMutex
	labels      map[string]permissions.SessionLabel
	applied     map[string]map[string]bool
	initialized map[string]bool
}

// NewFlowRegistry creates an empty registry.
func NewFlowRegistry() *FlowRegistry {
	return &FlowRegistry{
		labels:      map[string]permissions.SessionLabel{},
		applied:     map[string]map[string]bool{},
		initialized: map[string]bool{},
	}
}

// DefaultRegistry is the process-wide per-run label store consulted by
// the sandbox pre-dispatch gates (via FlowLabelFunc wiring in main).
// Tests construct their own registries; production code paths that lack
// a session ID fall back to the start label (v1 parity).
var DefaultRegistry = NewFlowRegistry()

// Ensure initializes sessionID to the start label when absent and returns
// a copy.
func (r *FlowRegistry) Ensure(sessionID string) permissions.SessionLabel {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.initialized[sessionID] {
		r.labels[sessionID] = permissions.StartLabel()
		r.applied[sessionID] = map[string]bool{}
		r.initialized[sessionID] = true
	}
	return r.labels[sessionID]
}

// Get returns a copy of the label, or nil when uninitialized.
func (r *FlowRegistry) Get(sessionID string) *permissions.SessionLabel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.initialized[sessionID] {
		return nil
	}
	l := r.labels[sessionID]
	return &l
}

// Set installs a label unconditionally (load/resume path).
func (r *FlowRegistry) Set(sessionID string, l permissions.SessionLabel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.labels[sessionID] = l
	if r.applied[sessionID] == nil {
		r.applied[sessionID] = map[string]bool{}
	}
	r.initialized[sessionID] = true
}

// Fold applies delta for idemKey, recording (labelVersion, argsHash) for
// the post-admission re-check. Re-admit of the same key is a no-op under
// the same lock (CAS). Returns the resulting label and whether this call
// narrowed it.
func (r *FlowRegistry) Fold(sessionID string, delta permissions.FlowDelta, idemKey string, argsHash string) (permissions.SessionLabel, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.labels[sessionID]
	if !ok || !r.initialized[sessionID] {
		cur = permissions.StartLabel()
		r.labels[sessionID] = cur
		r.applied[sessionID] = map[string]bool{}
		r.initialized[sessionID] = true
	}
	if idemKey != "" && r.applied[sessionID][idemKey] {
		return cur, false
	}
	next, err := permissions.FoldLabel(cur, delta)
	if err != nil {
		return cur, false
	}
	narrowed := next.Trust != cur.Trust || next.Audience != cur.Audience
	cur = next
	r.labels[sessionID] = cur
	if idemKey != "" {
		r.applied[sessionID][idemKey] = true
		permissions.RecordFlowBinding(sessionID, idemKey, cur.Version, argsHash)
	}
	return cur, narrowed
}

// Import seeds the registry from persisted session state (resume path):
// installs the label and the admitted-key set verbatim. No version bump;
// imported state is already authoritative.
func (r *FlowRegistry) Import(sessionID string, l permissions.SessionLabel, applied map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.labels[sessionID] = l
	set := map[string]bool{}
	for k, v := range applied {
		set[k] = v
	}
	r.applied[sessionID] = set
	r.initialized[sessionID] = true
}

// Merge folds other into sessionID's label with Meet (narrowing only)
// atomically under the write mutex, so a concurrent Fold between read and
// write cannot be lost (H2/H4). An uninitialized session merges from the
// start label. Returns the merged label.
func (r *FlowRegistry) Merge(sessionID string, other permissions.SessionLabel) permissions.SessionLabel {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.labels[sessionID]
	if !ok || !r.initialized[sessionID] {
		cur = permissions.StartLabel()
		if r.applied[sessionID] == nil {
			r.applied[sessionID] = map[string]bool{}
		}
		r.initialized[sessionID] = true
	}
	merged := permissions.Meet(cur, other)
	r.labels[sessionID] = merged
	return merged
}

// Sync copies the registry label and applied-key set back into sess
// (callers persist with Save after folds). No-op for unknown sessions.
func (r *FlowRegistry) Sync(sess *Session) {
	if sess == nil {
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.initialized[sess.ID] {
		return
	}
	l := r.labels[sess.ID]
	sess.Flow = &l
	set := map[string]bool{}
	for k, v := range r.applied[sess.ID] {
		set[k] = v
	}
	sess.FlowApplied = set
}

// RegisterLoaded ensures a just-loaded session resumes under its
// persisted label: absent flow recomputes conservative (never widest),
// then the registry imports label + applied set. Returns true when it
// recomputed (caller audits flow_recomputed and shows the one-time
// resume notice).
func RegisterLoaded(sess *Session) bool {
	return RegisterLoadedWith(DefaultRegistry, sess)
}

// RegisterLoadedWith is RegisterLoaded against an explicit registry
// (tests).
func RegisterLoadedWith(r *FlowRegistry, sess *Session) bool {
	if sess == nil || r == nil {
		return false
	}
	recomputed := sess.RecomputeFlow()
	r.Import(sess.ID, *sess.Flow, sess.FlowApplied)
	return recomputed
}
