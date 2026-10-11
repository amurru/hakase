package context

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"amurru/hakase/internal/env"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/project"
	sesspkg "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// HistoryBuilder prepends persisted conversation history to the root
// orchestrator's model requests via a BeforeModelCallback, and keeps that
// history within a token budget using a cheapest-first compaction cascade
// (tool-output trim -> deterministic snip -> async LLM summarization).
//
// The builder is created once in setupRunner with the sesspkg.SessionService and
// receives ModelInfo updates asynchronously once the provider fetch lands.
type HistoryBuilder struct {
	svc         *sesspkg.SessionService
	modelInfo   *interfaces.ModelInfo // guarded by modelInfoMu
	modelInfoMu sync.RWMutex
	logf        func(format string, args ...any)
	pending     *util.PendingQueue      // mid-run steering queue (may be nil)
	sidekick    *util.SidekickNoteQueue // sidekick advisory notes (may be nil)

	// Auto-memory (docs/auto-memory/spec.md D5, prompt-cache PC-004):
	// memoryProvider renders the per-turn memory block ("" = nothing to
	// inject). The block is evaluated on the first model call of every
	// turn and frozen for the rest of that turn, so a remember/forget
	// write lands on the next turn. The provider is wired in SetupRunner
	// and stays nil when memory is disabled. Guarded by memoryMu.
	memoryProvider func(ctx agent.Context) string
	memoryMu       sync.Mutex

	// SessionStart hooks (docs/hooks/spec.md HK-104, prompt-cache
	// PC-004): hookProvider renders the one-shot SessionStart context
	// block ("" = nothing to inject); hookSeen tracks settled sessions.
	// Unlike memory, the block is persisted once at the store head
	// before the session's first render and replays from the store
	// afterwards — never a frozen turn block. The rollback-on-empty
	// behavior is kept (a mid-session trust grant re-arms a later call).
	// Wired in SetupRunner; nil when hooks cannot fire SessionStart.
	// Guarded by hookMu.
	hookProvider func(ctx agent.Context) (string, bool)
	hookSeen     map[string]bool
	hookMu       sync.Mutex

	// UserPromptSubmit hooks (docs/hooks/spec.md HK-106): promptProvider
	// renders per-prompt context (""); promptSeen keys on
	// sessionID:last-user-sequence so every NEW user prompt fires once
	// while mid-turn model calls (same messages) do not re-fire. Empty
	// renders roll back like the slots above. Guarded by promptMu.
	promptProvider func(ctx agent.Context) (string, bool)
	promptSeen     map[string]bool
	promptMu       sync.Mutex

	// Token estimates of the rendered project-context block and the git
	// workspace snapshot that are folded into the system prompt
	// (setupRunner). fitToBudget reserves them so large AGENTS.md files or
	// repo snapshots do not silently blow the token budget. They live on the
	// builder rather than in process-global state so concurrent runs keep
	// their own accounting instead of overwriting each other's reserve.
	contextBlockTokens      int
	gitWorkspaceBlockTokens int
	skillIndexBlockTokens   int
}

// SetSkillIndexBlockTokens records the token estimate of the rendered skill index prompt block.
func (h *HistoryBuilder) SetSkillIndexBlockTokens(skillIndexBlockTokens int) {
	h.skillIndexBlockTokens = skillIndexBlockTokens
}

// IsDurablePin returns true if a message is a durable pin (exempt from compaction eviction).
func IsDurablePin(msg sesspkg.Message) bool {
	if msg.Kind == sesspkg.MessageKindDurablePin {
		return true
	}
	contentUpper := strings.ToUpper(strings.TrimSpace(msg.Content))
	if contentUpper == "" {
		return false
	}
	bracketMarkers := []string{"[PIN]", "[CONSTRAINT]", "[GOAL]", "[DECISION]", "[ID]"}
	for _, m := range bracketMarkers {
		if strings.Contains(contentUpper, m) {
			return true
		}
	}
	prefixes := []string{"CONSTRAINTS:", "RULED-OUT:", "NEVER-DO:", "GOAL:", "DECISION:", "IDS:"}
	lines := strings.Split(contentUpper, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, p := range prefixes {
			if strings.HasPrefix(trimmed, p) {
				return true
			}
		}
	}
	return false
}

// NewHistoryBuilder creates a HistoryBuilder bound to the given session
// service. svc may be nil (history injection is a no-op in that case).
func NewHistoryBuilder(svc *sesspkg.SessionService) *HistoryBuilder {
	return &HistoryBuilder{svc: svc}
}

// SetBlockTokenEstimates records the token estimates of the rendered
// project-context block and git workspace snapshot folded into the system
// prompt. Called once in setupRunner with the values rendered for this run.
func (h *HistoryBuilder) SetBlockTokenEstimates(contextBlockTokens, gitWorkspaceBlockTokens int) {
	h.contextBlockTokens = contextBlockTokens
	h.gitWorkspaceBlockTokens = gitWorkspaceBlockTokens
}

// SetPendingQueue attaches the TUI's mid-run message queue so queued prompts
// are steered into the request on every model call. May be nil (no steering).
func (h *HistoryBuilder) SetPendingQueue(q *util.PendingQueue) {
	h.pending = q
}

// SetSidekickQueue attaches the sidekick's advisory-note queue so notes
// produced by the watchdog are injected into the next model call. May be nil
// (no sidekick).
func (h *HistoryBuilder) SetSidekickQueue(q *util.SidekickNoteQueue) {
	h.sidekick = q
}

// SetModelInfo updates the model capabilities used for budget decisions.
// Called from the async ModelInfoMsg fetch once provider data lands.
func (h *HistoryBuilder) SetModelInfo(info *interfaces.ModelInfo) {
	h.modelInfoMu.Lock()
	defer h.modelInfoMu.Unlock()
	h.modelInfo = info
}

// SetMemoryProvider attaches the auto-memory block renderer (SetupRunner).
// The provider is evaluated on the first model call of every turn; a nil
// provider (memory disabled) disables injection entirely.
func (h *HistoryBuilder) SetMemoryProvider(fn func(ctx agent.Context) string) {
	h.memoryMu.Lock()
	defer h.memoryMu.Unlock()
	h.memoryProvider = fn
}

// MemoryProvider returns the attached auto-memory renderer (nil when memory
// is disabled or not yet wired).
func (h *HistoryBuilder) MemoryProvider() func(ctx agent.Context) string {
	h.memoryMu.Lock()
	defer h.memoryMu.Unlock()
	return h.memoryProvider
}

// SetSessionStartProvider attaches the SessionStart-hooks block renderer
// (SetupRunner). The block is persisted once at the store head; see
// persistSessionStart.
func (h *HistoryBuilder) SetSessionStartProvider(fn func(ctx agent.Context) (string, bool)) {
	h.hookMu.Lock()
	defer h.hookMu.Unlock()
	h.hookProvider = fn
	if h.hookSeen == nil {
		h.hookSeen = make(map[string]bool)
	}
}

// SessionStartProvider returns the attached renderer (nil when hooks cannot
// fire SessionStart).
func (h *HistoryBuilder) SessionStartProvider() func(ctx agent.Context) (string, bool) {
	h.hookMu.Lock()
	defer h.hookMu.Unlock()
	return h.hookProvider
}

// reserveSessionStart atomically claims the session's one-shot hook
// persistence; rollbackSessionStart releases a nothing-ran render so a
// later call (e.g. after a mid-session trust grant) stays eligible. A
// silent but executed hook (ran=true, empty block) stays claimed.
func (h *HistoryBuilder) reserveSessionStart(sessionID string) bool {
	h.hookMu.Lock()
	defer h.hookMu.Unlock()
	if h.hookProvider == nil || h.hookSeen[sessionID] {
		return false
	}
	h.hookSeen[sessionID] = true
	return true
}

func (h *HistoryBuilder) rollbackSessionStart(sessionID string) {
	h.hookMu.Lock()
	defer h.hookMu.Unlock()
	delete(h.hookSeen, sessionID)
}

// SetUserPromptProvider attaches the UserPromptSubmit-hooks block renderer
// (SetupRunner). Same once-per-key contract as the slots above, keyed by
// promptKey (session + latest user sequence).
func (h *HistoryBuilder) SetUserPromptProvider(fn func(ctx agent.Context) (string, bool)) {
	h.promptMu.Lock()
	defer h.promptMu.Unlock()
	h.promptProvider = fn
	if h.promptSeen == nil {
		h.promptSeen = make(map[string]bool)
	}
}

// UserPromptProvider returns the attached renderer (nil when hooks cannot
// fire UserPromptSubmit).
func (h *HistoryBuilder) UserPromptProvider() func(ctx agent.Context) (string, bool) {
	h.promptMu.Lock()
	defer h.promptMu.Unlock()
	return h.promptProvider
}

// reserveUserPrompt atomically claims one prompt key; rollbackUserPrompt
// releases an empty render. Same reserve/rollback contract as the
// SessionStart slots below.
func (h *HistoryBuilder) reserveUserPrompt(key string) bool {
	h.promptMu.Lock()
	defer h.promptMu.Unlock()
	if h.promptProvider == nil || h.promptSeen[key] {
		return false
	}
	h.promptSeen[key] = true
	return true
}

func (h *HistoryBuilder) rollbackUserPrompt(key string) {
	h.promptMu.Lock()
	defer h.promptMu.Unlock()
	delete(h.promptSeen, key)
}

// promptKey identifies the latest user prompt for per-prompt hook firing:
// session id plus the sequence of the last user-role message (0 when the
// session has no user history yet). A new prompt appends a message, so the
// key changes exactly when a new prompt lands; mid-turn model calls share
// the key and do not re-fire.
func promptKey(session *sesspkg.Session) string {
	var seq int64
	for i := len(session.Messages) - 1; i >= 0; i-- {
		if session.Messages[i].Role == "user" {
			seq = session.Messages[i].Sequence
			break
		}
	}
	return session.ID + ":" + strconv.FormatInt(seq, 10)
}

// SetLogFunc installs a logger (usually the TUI log pane) for compaction
// status messages.
func (h *HistoryBuilder) SetLogFunc(f func(format string, args ...any)) {
	h.logf = f
}

func (h *HistoryBuilder) logfSafe(format string, args ...any) {
	if h.logf != nil {
		h.logf(format, args...)
	}
}

// frozenStateKey is the ADK invocation-state slot holding the turn's
// frozen context payload (prompt-cache PC-002, spec D3). `temp:` values
// live for the invocation only and are never persisted, so the payload is
// rendered once per turn (the first model call) and spliced read-only
// while the slot survives. stateGet/stateSet are recover-guarded and
// report failure instead of panicking: a context without state degrades to
// re-rendering the (already reserved) sources on later calls, and the
// caller logs that. The guard makes mid-turn mutation by the normal path
// impossible; it is not a structural barrier.
const frozenStateKey = "temp:hakase.frozen_tail"

// steeringStateKey remembers how many steering blocks were already spliced
// in this invocation, so the rewrite log fires on an actual mid-turn
// arrival instead of on every later model call.
const steeringStateKey = "temp:hakase.steering_injected"

// stateGet reads one invocation-state slot; ok is false when the context
// carries no state or the key is absent.
func stateGet(ctx agent.Context, key string) (any, bool) {
	if ctx == nil {
		return nil, false
	}
	var st interface {
		Get(string) (any, error)
	}
	func() {
		defer func() { _ = recover() }()
		st = ctx.State()
	}()
	if st == nil {
		return nil, false
	}
	v, err := st.Get(key)
	if err != nil || v == nil {
		return nil, false
	}
	return v, true
}

// stateSet writes one invocation-state slot; false reports the degraded
// path (no state, a failed write, or a panic inside the state object).
func stateSet(ctx agent.Context, key string, v any) bool {
	if ctx == nil {
		return false
	}
	ok := false
	func() {
		defer func() { _ = recover() }()
		if st := ctx.State(); st != nil {
			ok = st.Set(key, v) == nil
		}
	}()
	return ok
}

// loadFrozenTurn returns the frozen block texts stored for this invocation.
// ok is false when nothing was stored yet (the caller must render and
// store) or when the context carries no state (degraded: render fresh).
func loadFrozenTurn(ctx agent.Context) (texts []string, ok bool) {
	v, ok := stateGet(ctx, frozenStateKey)
	if !ok {
		return nil, false
	}
	return decodeFrozenTexts(v)
}

// storeFrozenTurn records the turn's frozen block texts in invocation
// state. A false return is the degraded path (test mocks without state, or
// a failed write); the caller still splices the freshly rendered blocks
// into the current request and logs the condition.
func storeFrozenTurn(ctx agent.Context, texts []string) bool {
	return stateSet(ctx, frozenStateKey, encodeFrozenTexts(texts))
}

// decodeStateCount reads a small integer invocation-state slot, accepting
// the in-memory int form and the JSON-round-tripped float64.
func decodeStateCount(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}

// encodeFrozenTexts converts block texts to a JSON-safe invocation-state
// value (plain strings survive the durable ADK service's marshal round).
func encodeFrozenTexts(texts []string) any {
	out := make([]any, 0, len(texts))
	for _, t := range texts {
		out = append(out, t)
	}
	return out
}

// decodeFrozenTexts reads back encodeFrozenTexts, accepting the in-memory
// ([]string / []any) and JSON-round-tripped forms.
func decodeFrozenTexts(v any) ([]string, bool) {
	switch t := v.(type) {
	case []string:
		return t, true
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case map[string]any:
		if raw, ok := t["texts"]; ok {
			return decodeFrozenTexts(raw)
		}
	}
	return nil, false
}

// frozenContents rebuilds the user-role contents for stored block texts.
// Pure: no provider, queue, or filesystem reads.
func frozenContents(texts []string) []*genai.Content {
	out := make([]*genai.Content, 0, len(texts))
	for _, t := range texts {
		if t == "" {
			continue
		}
		out = append(out, genai.NewContentFromText(t, genai.RoleUser))
	}
	return out
}

// lastPersistedUser returns the session's most recent user-role message
// (the dedup anchor for the live leading content).
func lastPersistedUser(session *sesspkg.Session) (sesspkg.Message, bool) {
	if session == nil {
		return sesspkg.Message{}, false
	}
	for i := len(session.Messages) - 1; i >= 0; i-- {
		if session.Messages[i].Role == "user" {
			return session.Messages[i], true
		}
	}
	return sesspkg.Message{}, false
}

// splitLiveTail drops the live leading user content when it matches the
// last persisted user message (render-from-store, PC-001): the store's
// render already carries those bytes, so keeping the live copy would
// duplicate the prompt and fork the prefix. Returns the un-persisted ADK
// tail (tool calls/results) and whether the match held.
func splitLiveTail(contents []*genai.Content, session *sesspkg.Session) (tail []*genai.Content, matched bool) {
	if len(contents) == 0 {
		return nil, true
	}
	last, ok := lastPersistedUser(session)
	if !ok || contents[0] == nil || contents[0].Role != genai.RoleUser {
		return contents, false
	}
	// Tool-protocol traffic is never the live copy of a persisted message:
	// a durable resume re-enters the runner with the human answer as a
	// user-role FunctionResponse (resume.go), which is textless and would
	// otherwise match an attachment-only anchor below. Refuse to drop it.
	for _, p := range contents[0].Parts {
		if p != nil && p.FunctionResponse != nil {
			return contents, false
		}
	}
	// The persisted anchor was normalized at ingestion (PC-001); normalize
	// the live copy the same way so a CRLF or NFD live send still matches
	// its stored bytes instead of duplicating the prompt.
	if !CurrentUserMessageMatches(last, sesspkg.NormalizeIngestedText(ContentText(contents[0]))) {
		return contents, false
	}
	return contents[1:], true
}

// BeforeModelCallback assembles the model request under invariant P
// (prompt-cache spec D1/D4):
//
//	[rendered store][frozen turn blocks][steering][un-persisted run tail]
//
// The rendered store is append-only between compactions; the frozen blocks
// are evaluated once per turn and read from ADK invocation state on later
// calls; steering is live per call; the tail is ADK's own tool
// calls/results. Returning (nil, nil) lets the call proceed.
//
// The callback fires once per LLM call; within one run the tool loop makes
// several calls and ADK rebuilds req.Contents from session events each time
// (those events never contain our file-backed history), so we assemble on
// every call rather than tracking InvocationID. Each call receives a fresh
// request built from the ADK session, so there is no duplication risk.
func (h *HistoryBuilder) BeforeModelCallback(ctx agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	if h == nil || h.svc == nil || req == nil {
		return nil, nil
	}

	// Resolve the session the invoking run serves. agentrun registers every
	// driven run's task→session mapping, so parallel runs (web chat, channel
	// threads) each inject their OWN history; a global active session here
	// made whichever run called SetActiveSession last leak its history into
	// the others. Surfaces without a registered run (the TUI loop) fall back
	// to the active session, which they own exclusively.
	var session *sesspkg.Session
	if id := interfaces.SessionIDFromCtx(ctx); id != "" {
		if s, loadErr := h.svc.Store().Load(id); loadErr == nil {
			session = s
		}
	}
	if session == nil {
		var err error
		session, err = h.svc.GetActiveSession()
		if err != nil || session == nil {
			return nil, nil
		}
	}

	// SessionStart hooks are persist-once at the store head (PC-004): the
	// first callback of a session renders the provider and prepends the
	// block before anything else reads the store, so every later call and
	// turn replays it from history instead of re-paying it.
	h.persistSessionStart(ctx, session)

	// Sidekick notes are persist-only (PC-003): drain the queue into the
	// session before rendering the store, so a mid-turn note lands after
	// the current user message in sequence order and stays there on every
	// later call and turn. No special-case tail exclusion.
	h.persistSidekickNotes(session)

	// Frozen turn context (PC-002): the first model call of the turn
	// evaluates the reserve gates and sources once and stores the payload
	// in invocation state; later calls splice it read-only.
	frozenTexts, haveFrozen := loadFrozenTurn(ctx)
	firstCall := !haveFrozen
	if firstCall {
		frozenTexts = h.renderFrozenTurn(ctx, session)
		if !storeFrozenTurn(ctx, frozenTexts) {
			h.logfSafe("cache: frozen turn state unavailable; blocks re-render per call")
		}
	}
	frozen := frozenContents(frozenTexts)

	// Split the live leading user content: when it matches the last
	// persisted user message the store render already carries it.
	tail, matched := splitLiveTail(req.Contents, session)
	if !matched && len(session.Messages) > 0 {
		h.logfSafe("cache: live user content unmatched; keeping live copy")
	}

	// Live steering (pending queue) joins the frozen insertion section per
	// call: it sits after the frozen prefix so a mid-turn arrival extends
	// the tail without rewriting the prefix. The arrival itself is a
	// logged tail rewrite.
	var steering []*genai.Content
	if h.pending != nil && h.pending.Len() > 0 {
		for _, q := range h.pending.Snapshot() {
			steering = append(steering, util.SteeringContent(q))
		}
		// A pre-turn queue is spliced identically on the first call and
		// must stay silent on later calls; only an increase over the count
		// already spliced this turn is a logged tail rewrite. The count
		// lives in invocation state, so it resets every turn.
		prev, havePrev := 0, false
		if v, ok := stateGet(ctx, steeringStateKey); ok {
			prev, havePrev = decodeStateCount(v)
		}
		if len(steering) > prev && (havePrev || !firstCall) {
			h.logfSafe("cache: steering arrival mid-turn, tail rewrite (%d queued)", len(steering))
		}
		if !havePrev || len(steering) != prev {
			_ = stateSet(ctx, steeringStateKey, len(steering))
		}
	}

	if len(session.Messages) == 0 && len(frozen) == 0 && len(steering) == 0 {
		return nil, nil
	}

	// Render the store: summaries first, then the in-context transcript in
	// sequence order — including the current user message (the live copy
	// was dropped above).
	history := h.buildHistory(session.Messages)

	// Fit history into the budget (cascade stages a+b when over trigger).
	h.modelInfoMu.RLock()
	info := h.modelInfo
	h.modelInfoMu.RUnlock()
	budgetCurrent := make([]*genai.Content, 0, len(frozen)+len(steering)+len(tail))
	budgetCurrent = append(budgetCurrent, frozen...)
	budgetCurrent = append(budgetCurrent, steering...)
	budgetCurrent = append(budgetCurrent, tail...)
	history = h.fitToBudget(session, history, budgetCurrent, info)

	// Assemble per invariant P. No harness content is ever appended after
	// the ADK tail.
	assembled := make([]*genai.Content, 0, len(history)+len(frozen)+len(steering)+len(tail))
	assembled = append(assembled, history...)
	assembled = append(assembled, frozen...)
	assembled = append(assembled, steering...)
	assembled = append(assembled, tail...)
	req.Contents = assembled
	return nil, nil
}

// renderFrozenTurn evaluates the turn-injected sources exactly once per
// turn: the per-prompt hook reservation plus the live per-turn renders
// (memory, project snapshot, notices). A mid-turn change renders into the
// NEXT turn's frozen set. Order preserves the previous head precedence
// (freshest first): prompt, memory, snapshot, then notices.
func (h *HistoryBuilder) renderFrozenTurn(ctx agent.Context, session *sesspkg.Session) []string {
	var texts []string

	// UserPromptSubmit hooks: per-prompt reservation on the latest user
	// message. Empty renders without a run roll back (stay eligible).
	if pkey := promptKey(session); h.reserveUserPrompt(pkey) {
		if block, ran := h.promptProvider(ctx); block != "" {
			texts = append(texts, "HOOK PROMPT CONTEXT:\n"+block)
		} else if !ran {
			h.rollbackUserPrompt(pkey)
		}
	}
	// Session-start auto-memory: re-rendered every turn (PC-004) so a
	// remember/forget write is visible on the next turn. An empty block
	// simply contributes nothing; no reservation, no rollback.
	h.memoryMu.Lock()
	memProvider := h.memoryProvider
	h.memoryMu.Unlock()
	if memProvider != nil {
		if block := memProvider(ctx); block != "" {
			texts = append(texts, block)
		}
	}
	// Project snapshot (PC-005): the turn's pre-rendered GIT WORKSPACE
	// block rides the run context (agentrun sets it per turn for bound
	// sessions). Frozen here so every call of the turn splices identical
	// bytes; a mid-turn repo change renders next turn.
	if snap := project.SnapshotFrom(ctx); strings.TrimSpace(snap) != "" {
		texts = append(texts, snap)
	}

	// Live reconcile: project context files changed mid-session. Frozen
	// per turn so the notice text is byte-identical across the turn's
	// calls; a change landing mid-turn renders next turn.
	if notice := ContextUpdateNotice(); notice != "" {
		h.logfSafe("⚠ project context files changed; update notice injected")
		texts = append(texts, notice)
	}
	// Environment staleness: same frozen treatment as the update notice.
	if notice := env.EnvStalenessNotice(); notice != "" {
		h.logfSafe("⚠ system state changed; environment update notice injected")
		texts = append(texts, notice)
	}
	return texts
}

// persistSessionStart inserts the SessionStart hook block once at the
// store head (PC-004), before the session's first render. Later callbacks
// find the persisted marker and do nothing, so the block replays from
// history on every call and turn. A nothing-ran render rolls the slot
// back and stays eligible (e.g. a mid-session trust grant); a silent but
// executed hook settles the slot without persisting anything.
func (h *HistoryBuilder) persistSessionStart(ctx agent.Context, session *sesspkg.Session) {
	if h == nil || session == nil {
		return
	}
	h.hookMu.Lock()
	provider := h.hookProvider
	settled := h.hookSeen[session.ID]
	h.hookMu.Unlock()
	if provider == nil || settled {
		return
	}
	if hasSessionStart(session) {
		// Persisted by an earlier process (or a concurrent run that won
		// the reserve): settle the slot without rendering again.
		h.hookMu.Lock()
		if h.hookSeen == nil {
			h.hookSeen = make(map[string]bool)
		}
		h.hookSeen[session.ID] = true
		h.hookMu.Unlock()
		return
	}
	if !h.reserveSessionStart(session.ID) {
		return
	}
	block, ran := provider(ctx)
	if block == "" {
		if !ran {
			h.rollbackSessionStart(session.ID)
		}
		return
	}
	prependSessionStart(session, "HOOK SESSIONSTART CONTEXT:\n"+block)
	if h.svc != nil {
		if err := h.svc.SaveSession(session); err != nil {
			h.logfSafe("⚠ [context] sessionstart persist failed: %v", err)
		}
	}
}

// hasSessionStart reports whether the session already carries its
// persisted SessionStart block (the cross-restart idempotency marker;
// the in-memory hookSeen slot covers only this process).
func hasSessionStart(session *sesspkg.Session) bool {
	if session == nil {
		return false
	}
	for _, msg := range session.Messages {
		if msg.Kind == sesspkg.MessageKindSessionStart {
			return true
		}
	}
	return false
}

// prependSessionStart inserts the rendered hook block at the store head
// so it replays first (after summaries) on every later render. The
// sequence takes the next cursor value to keep creation order monotonic;
// render and eviction walk slice order, so head placement is what matters.
func prependSessionStart(session *sesspkg.Session, content string) {
	msg := sesspkg.Message{
		Role:      "user",
		Content:   sesspkg.NormalizeIngestedText(content),
		Timestamp: time.Now().UTC(),
		Sequence:  int64(len(session.Messages)),
		InContext: true,
		Kind:      sesspkg.MessageKindSessionStart,
	}
	session.Messages = append([]sesspkg.Message{msg}, session.Messages...)
}

// persistSidekickNotes drains the sidekick advisory queue into the session
// (InContext) so notes render from the store in sequence order. Persisted
// before the next natural save point so they survive a crash.
func (h *HistoryBuilder) persistSidekickNotes(session *sesspkg.Session) {
	if h.sidekick == nil || h.sidekick.Len() == 0 || session == nil {
		return
	}
	for _, n := range h.sidekick.Pending() {
		session.AddMessageWithMeta("sidekick", n.Text, "", 0, sesspkg.MessageKindSidekick)
	}
	if h.svc != nil {
		_ = h.svc.SaveSession(session)
	}
}

// buildHistory converts persisted messages to genai contents: summaries
// first, then the in-context transcript in sequence order. Every in-context
// message renders from its stored bytes (render-from-store, PC-001),
// including the current user message — the live copy is dropped by
// splitLiveTail when it matches, so the prompt appears exactly once.
func (h *HistoryBuilder) buildHistory(msgs []sesspkg.Message) []*genai.Content {
	var summaries, history []*genai.Content
	for _, msg := range msgs {
		if !msg.InContext {
			continue
		}
		if msg.Kind == sesspkg.MessageKindSummary {
			summaries = append(summaries, h.renderMessage(msg))
			continue
		}
		history = append(history, h.renderMessage(msg))
	}
	return append(summaries, history...)
}

// renderMessage converts one persisted message to request content,
// logging `cache: attachment drift` when an attachment's on-disk bytes no
// longer match the persist-time hash (PC-001). Changed bytes after the
// attachment invalidate the prefix cache, so the drift is observable.
func (h *HistoryBuilder) renderMessage(msg sesspkg.Message) *genai.Content {
	for _, att := range msg.Attachments {
		if att.Hash == "" {
			continue
		}
		if current := sesspkg.HashAttachmentFile(att.Path); current != att.Hash {
			name := att.Name
			if name == "" {
				name = att.Path
			}
			h.logfSafe("cache: attachment drift %q (persisted bytes changed)", name)
			break
		}
	}
	return MessageToContent(msg)
}

// currentUserMessageMatches reports whether the in-flight request's user
// content corresponds to the given persisted user message (dedup guard for
// splitLiveTail).
func CurrentUserMessageMatches(msg sesspkg.Message, currentText string) bool {
	if currentText == msg.Content {
		return true
	}
	// Attachment-bearing messages persist the bare prompt; the request text
	// is the prompt followed by any attachment parts (text files), so a
	// prefix match means the same message. An empty stored prompt cannot
	// prefix-match anything: that degenerate case belongs to the
	// attachment-only arm below, so nothing with text is ever dropped by
	// accident.
	if len(msg.Attachments) > 0 && msg.Content != "" && currentText != "" && strings.HasPrefix(currentText, msg.Content) {
		return true
	}
	// Image-only messages persist no text and the request carries only the
	// image part (no text either).
	if len(msg.Attachments) > 0 && currentText == "" && msg.Content == "" {
		return true
	}
	return false
}

// MessageToContent converts a persisted message into a genai.Content with
// the appropriate role. Agent messages map to the model role. Messages with
// attachments rebuild their parts: text verbatim, text files as text parts,
// images as inline data parts (content re-read from Path).
//
// All text content (message body and text-file attachment contents) is framed
// in <UNTRUSTED_DATA> tags via WrapUntrustedData to clearly delimit
// externally-sourced data when it is re-injected into the model context.
func MessageToContent(msg sesspkg.Message) *genai.Content {
	role := genai.Role(msg.Role)
	if msg.Role == "agent" {
		role = genai.RoleModel
	}
	if msg.Role == "sidekick" {
		// Advisory notes are authored content from a second LLM; surface them
		// to the model as user-role context so they are not mistaken for the
		// primary assistant's own output.
		role = genai.RoleUser
	}
	if len(msg.Attachments) == 0 {
		return genai.NewContentFromText(WrapUntrustedData(msg.Content), role)
	}
	var parts []*genai.Part
	if strings.TrimSpace(msg.Content) != "" {
		parts = append(parts, genai.NewPartFromText(WrapUntrustedData(msg.Content)))
	}
	for _, att := range msg.Attachments {
		if att.Path == "" {
			continue
		}
		data, err := os.ReadFile(att.Path)
		if err != nil {
			continue
		}
		if util.IsImageMIME(att.MIME) {
			parts = append(parts, genai.NewPartFromBytes(data, att.MIME))
		} else if !utf8.Valid(data) {
			// The file on disk is not text (deleted/replaced/binary all
			// along): summarize instead of injecting mangled bytes.
			parts = append(parts, genai.NewPartFromText(WrapUntrustedData(
				fmt.Sprintf("[binary file: %s, %d bytes - not readable as text]", att.MIME, len(data)),
			)))
		} else {
			parts = append(parts, genai.NewPartFromText(WrapUntrustedData(string(data))))
		}
	}
	return genai.NewContentFromParts(parts, role)
}

// ContentText extracts the concatenated text of a genai.Content for dedup
// comparison against persisted user messages.
func ContentText(c *genai.Content) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	for _, part := range c.Parts {
		if part != nil && part.Text != "" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// fitToBudget applies the compaction cascade when history + current contents
// exceed the trigger threshold (0.9 * effective max input). It returns the
// trimmed history slice; stage c (LLM summarization) is scheduled async and
// never runs in the callback's hot path.
func (h *HistoryBuilder) fitToBudget(session *sesspkg.Session, history []*genai.Content, current []*genai.Content, info *interfaces.ModelInfo) []*genai.Content {
	// No model info yet (async fetch not complete or failed): don't truncate,
	// but log the near-limit condition only when the session history is
	// suspiciously large. This is a legitimate state (e.g. web mode before
	// the model-info fetch lands), not an error.
	if info == nil {
		return history
	}
	effectiveMax := util.MaxInputTokens(&util.ModelBudget{ContextWindow: info.ContextWindow, MaxInputTokens: info.MaxInputTokens})
	if effectiveMax <= 0 {
		// No usable budget; don't truncate, but log the near-limit condition
		// only when the session history is suspiciously large.
		return history
	}

	// Reserve budget for the system prompt + tool schemas + current run
	// contents, plus the rendered project-context block and the git workspace
	// snapshot (estimates recorded on this builder in setupRunner from the
	// discovered AGENTS.md files and repo snapshot), and the
	// runtime-environment block (env.SystemEnvBlockTokens, set in setupRunner).
	// The flat baseline plus the blocks is a conservative approximation since
	// we cannot see the fully rendered prompt.
	const baseReserveTokens = 8000
	reserveTokens := baseReserveTokens + h.contextBlockTokens + h.gitWorkspaceBlockTokens + h.skillIndexBlockTokens + env.SystemEnvBlockTokens

	currentTokens := util.EstimateContentsTokens(current)
	trigger := int64(effectiveMax * 9 / 10)
	target := int64(effectiveMax * 7 / 10)
	historyTokens := int64(util.EstimateContentsTokens(history))

	if currentTokens+int(historyTokens)+reserveTokens <= int(trigger) {
		return history
	}

	// One rewrite log line per compaction occurrence (prompt-cache D7/PC-006):
	// direction, tokens before/after, reason, and the cascade stages that ran.
	// Evictions inside the stages log per message; this is the aggregate line
	// the verification greps for.
	before := int(historyTokens) + currentTokens
	stages := "tool-trim"

	// Stage (a): trim tool-output transcripts. Tool results are the cheapest
	// to evict (they were already used to produce the agent's answer).
	history = h.stageATrimToolResults(session, history)

	// Stage (b): deterministic snip. Archive oldest messages until the tail
	// fits the target; keep the recent ~20k tokens + last 2 turns verbatim.
	if currentTokens+util.EstimateContentsTokens(history)+reserveTokens > int(target) {
		stages += "+snip"
		history = h.StageBSnip(session, history, currentTokens, reserveTokens, target)
	}

	// Stage (c): async summarization, only if still over target. The summary
	// is picked up on the next turn; UI never blocks.
	if currentTokens+util.EstimateContentsTokens(history)+reserveTokens > int(target) {
		stages += "+summarize"
		h.ScheduleSummarize(session.ID, "")
	}

	after := util.EstimateContentsTokens(history) + currentTokens
	h.logfSafe("⚠ cache: rewrite direction=shrink tokens %d->%d reason=compaction stages=%s", before, after, stages)

	return history
}

// stageATrimToolResults marks tool_result messages out-of-context so they no
// longer enter the request, keeping the tool_use record. Returns the history
// rebuilt from the surviving messages.
func (h *HistoryBuilder) stageATrimToolResults(session *sesspkg.Session, history []*genai.Content) []*genai.Content {
	changed := false
	for i := range session.Messages {
		msg := &session.Messages[i]
		if msg.InContext && msg.Kind == sesspkg.MessageKindToolResult {
			msg.InContext = false
			msg.Content = "[tool result trimmed]"
			changed = true
			h.logfSafe("⚠ [context] Evicted tool result seq %d (role: %s, tokens: %d) at %s", msg.Sequence, msg.Role, msg.Tokens, time.Now().Format(time.RFC3339))
		}
	}
	if changed {
		h.persistSnapshot(session)
		return h.buildHistory(session.Messages)
	}
	return history
}

// stageBSnip archives oldest in-context messages (flips InContext=false,
// keeps them on disk per the aichat pattern) until the remaining tail fits
// within the target budget. The last 2 turns are always kept verbatim.
// Oldest-first eviction naturally preserves ~20k recent tokens whenever the
// budget allows; on tight budgets the budget wins over the recency heuristic.
func (h *HistoryBuilder) StageBSnip(session *sesspkg.Session, history []*genai.Content, currentTokens, reserveTokens int, target int64) []*genai.Content {
	// Hard keep region: the last 2 user turns (never evicted).
	last2TurnStart := -1
	userCount := 0
	for i := len(session.Messages) - 1; i >= 0; i-- {
		if session.Messages[i].Role == "user" && session.Messages[i].InContext {
			userCount++
			if userCount == 2 {
				last2TurnStart = i
				break
			}
		}
	}

	budget := int(target) - currentTokens - reserveTokens
	if budget < 0 {
		budget = 0
	}

	// Evict oldest in-context messages until the surviving history fits the
	// budget, never crossing into the protected last-2-turns region.
	changed := false
	for i := 0; i < len(session.Messages); i++ {
		if last2TurnStart != -1 && i >= last2TurnStart {
			break
		}
		if !session.Messages[i].InContext {
			continue
		}
		if IsDurablePin(session.Messages[i]) {
			continue
		}
		session.Messages[i].InContext = false
		changed = true
		h.logfSafe("⚠ [context] Evicted message seq %d (role: %s, kind: %s, tokens: %d) at %s",
			session.Messages[i].Sequence, session.Messages[i].Role, session.Messages[i].Kind, session.Messages[i].Tokens, time.Now().Format(time.RFC3339))
		if int64(util.EstimateContentsTokens(h.buildHistory(session.Messages))) <= int64(budget) {
			break
		}
	}

	if changed {
		h.persistSnapshot(session)
	}
	return h.buildHistory(session.Messages)
}

// persistSnapshot saves the session's current message state to disk (the
// InContext flips from the compaction cascade). Hint dedup state is synced
// onto the session so a resumed session does not re-attach hints.
func (h *HistoryBuilder) persistSnapshot(session *sesspkg.Session) {
	if h.svc == nil || session == nil {
		return
	}
	if sesspkg.BuildHintedPathsHook != nil {
		session.HintedContextFiles = sesspkg.BuildHintedPathsHook()
	}
	_ = h.svc.SaveSession(session)
}
