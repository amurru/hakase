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
	// recomputed marks sessions whose flow state was backfilled
	// conservative on load (pre-v2 files, M1). The flags drive the
	// one-time flow_recomputed audit entry and the one-time resume
	// notice; neither is persisted.
	recomputed map[string]bool
	notice     map[string]bool
	noticed    map[string]bool
}

// NewFlowRegistry creates an empty registry.
func NewFlowRegistry() *FlowRegistry {
	return &FlowRegistry{
		labels:      map[string]permissions.SessionLabel{},
		applied:     map[string]map[string]bool{},
		initialized: map[string]bool{},
		recomputed:  map[string]bool{},
		notice:      map[string]bool{},
		noticed:     map[string]bool{},
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

// IsInitialized reports whether the registry holds a live entry for
// sessionID. Load consults it so a read racing a turn never imports stale
// disk state over live folds (C1).
func (r *FlowRegistry) IsInitialized(sessionID string) bool {
	if r == nil || sessionID == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.initialized[sessionID]
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

// ImportMeet atomically merges persisted session state into the registry
// under a single write lock (F1: no check-then-act split between an
// IsInitialized read lock and a later write lock, so a Load racing a turn
// can neither widen the registry nor lose a concurrent fold).
//
// A registry with no live entry for sessionID imports the persisted label
// verbatim (no version bump: the persisted version is authoritative, and
// bumping every resume would break the H3 resume/compaction
// version-unchanged invariant) and reports imported=true. A registry with
// a live entry merges with permissions.Meet (narrowing only, never wider)
// and reports imported=false. Either way the applied-key set becomes the
// union of live and persisted keys (monotone: applied sets only grow), the
// entry is marked initialized, and the merged label is returned.
func (r *FlowRegistry) ImportMeet(sessionID string, persisted permissions.SessionLabel, applied map[string]bool) (merged permissions.SessionLabel, imported bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.initialized[sessionID] {
		r.labels[sessionID] = persisted
		set := map[string]bool{}
		for k, v := range applied {
			set[k] = v
		}
		r.applied[sessionID] = set
		r.initialized[sessionID] = true
		return persisted, true
	}
	cur := r.labels[sessionID]
	merged = permissions.Meet(cur, persisted)
	r.labels[sessionID] = merged
	set := r.applied[sessionID]
	if set == nil {
		set = map[string]bool{}
		r.applied[sessionID] = set
	}
	for k, v := range applied {
		set[k] = v
	}
	return merged, false
}

// appliedCopy returns a copy of the applied-key set under the read lock
// ("" or unknown sessions yield an empty set, never nil).
func (r *FlowRegistry) appliedCopy(sessionID string) map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]bool{}
	for k, v := range r.applied[sessionID] {
		out[k] = v
	}
	return out
}

// Sync copies the registry label back into sess and unions the
// applied-key sets (registry live keys plus whatever the loaded object
// already carries, so a Load-then-Save round trip after a racing turn
// preserves both narrowings' idempotency keys and never double-folds).
// No-op for unknown sessions.
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
	for k, v := range sess.FlowApplied {
		set[k] = v
	}
	sess.FlowApplied = set
}

// RegisterLoaded ensures a just-loaded session resumes under its
// persisted label (resume-only): absent flow recomputes conservative
// (never widest), then the registry imports label + applied set. When the
// registry already holds a live entry for the session (a turn folded after
// the resume), the live entry is authoritative: the import is skipped and
// the live label is synced into the loaded object instead, so a Load
// racing a turn can neither widen the registry nor hand a read path a
// stale label (C1). Returns true when it recomputed.
func RegisterLoaded(sess *Session) bool {
	return RegisterLoadedWith(DefaultRegistry, sess)
}

// RegisterLoadedWith is RegisterLoaded against an explicit registry
// (tests). The check-and-import holds a single write lock inside
// ImportMeet: a Load racing a turn merges (Meet, narrowing only) instead
// of letting stale disk state widen live folds, and the merged label is
// synced back into the loaded object so read paths (turn-end persistence,
// compaction, listings) see a label coherent with the registry they will
// Save back. Returns true when it recomputed.
func RegisterLoadedWith(r *FlowRegistry, sess *Session) bool {
	if sess == nil || r == nil {
		return false
	}
	recomputed := sess.RecomputeFlow()
	merged, fresh := r.ImportMeet(sess.ID, *sess.Flow, sess.FlowApplied)
	l := merged
	sess.Flow = &l
	if !fresh {
		// Merged into a live entry: pick up the union applied set so
		// the Save preserves both sides' idempotency keys. A fresh
		// import already carries exactly the persisted set.
		sess.FlowApplied = r.appliedCopy(sess.ID)
	}
	if recomputed {
		r.mu.Lock()
		r.recomputed[sess.ID] = true
		r.notice[sess.ID] = true
		r.mu.Unlock()
	}
	return recomputed
}

// TakeFlowRecomputed reports (once) that the session's flow state was
// backfilled conservative on load, so the caller can audit
// flow_recomputed. The second call for the same session reports false:
// the recompute is a one-time event.
func (r *FlowRegistry) TakeFlowRecomputed(sessionID string) bool {
	if r == nil || sessionID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.recomputed[sessionID] {
		return false
	}
	delete(r.recomputed, sessionID)
	return true
}

// TakeFlowNotice returns the one-time resume notice for a recomputed
// session ("") once it has been shown; the notice is dropped after the
// first read so the model sees it exactly once (FG-005).
func (r *FlowRegistry) TakeFlowNotice(sessionID string) string {
	if r == nil || sessionID == "" {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.notice[sessionID] || r.noticed[sessionID] {
		return ""
	}
	r.noticed[sessionID] = true
	delete(r.notice, sessionID)
	return FlowResumeNotice(true)
}
