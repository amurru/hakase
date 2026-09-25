// Package runserver mounts hakase agent runs as MCP tools on an existing
// *mcp.Server: a `run` tool driving the transport-neutral agentrun.Driver
// loop (same sessions, persistence, sandbox pinning, tool-call repair and
// tracing the web chat and Telegram use), plus session listing/reading so a
// driving agent can pick threads up, and gate prompts (approvals,
// clarifications) delivered to the MCP client per SEP-2322.
//
// Gate delivery: mid-run approvals/clarifications cannot be standalone
// server-initiated requests on protocol 2026-07-28 (SEP-2322/SEP-2575 forbid
// them). Instead the `run` handler suspends the call with an
// `input_required` result carrying the elicitation as an InputRequest; the
// driving client fulfills it (its human answers) and the call is retried
// with the response, resuming the suspended run. The go-sdk fulfills the
// same InputRequests transparently for pre-2026-07-28 clients, so one code
// path serves both. A client that cannot fulfill (no elicitation support)
// surfaces the failure and the run's gates fail closed on their expiry.
//
// Trust model: stdio MCP is local trust - the server runs with the user's
// config, model keys and sandbox. Nothing here adds a network surface.
package runserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"
)

// Runner is the minimal seam over the agent turn loop so tests can inject a
// fake; *agentrun.Driver satisfies it.
type Runner interface {
	RunTurn(ctx context.Context, sessionID string, content *genai.Content, sink agentrun.EventSink)
}

// Deps wires the run tools. Sessions must be non-nil; Runner nil makes `run`
// refuse with an actionable error (a skills-only process can still mount and
// list sessions).
type Deps struct {
	Runner   Runner
	Sessions *hakasesession.SessionService
	Gates    *Gates
	Log      interfaces.LogFunc
}

const (
	// maxConcurrentRuns caps runs across all sessions: one stdio client could
	// otherwise fire unbounded parallel `run` calls against one process (and
	// one model quota). Per-session concurrency is 1 (see runTracker).
	maxConcurrentRuns = 8

	// defaultRunTimeout bounds a run when the caller does not ask for its own
	// bound. It covers the WHOLE run including suspended gate round trips.
	defaultRunTimeout = 900 * time.Second
	// maxRunTimeout is the hard ceiling for one run (1 day).
	maxRunTimeout = 86400 * time.Second

	// runStateTTL keeps finished runs' results retrievable by a late retry
	// before the registry entry is dropped.
	runStateTTL = 10 * time.Minute

	// titleRunes caps the auto-generated session title, mirroring
	// SessionService.ensureActiveSession.
	titleRunes = 60
)

// Mount adds the run tools to srv. Name the server "hakase" when mounting
// (skills-only processes keep "hakase-skills").
func Mount(srv *mcp.Server, d Deps) {
	mountOn(srv, &server{
		deps:    d,
		runs:    map[string]*suspendedRun{},
		tracker: &runTracker{active: map[string]int{}},
	})
}

// mountOn registers the tools on srv bound to an existing server instance
// (separated from Mount so tests can inspect the registry).
func mountOn(srv *mcp.Server, s *server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "run",
		Description: "Send a prompt to the hakase agent and wait for its answer. " +
			"Runs use hakase's tools (files, shell, knowledge, ...) on this machine and persist to a hakase session. " +
			"Omit session_id to start a fresh session, or pass the session_id of an earlier result to continue that thread. " +
			"Mid-run approvals/clarifications arrive as input requests on this call; stream deltas and activity arrive as progress notifications.",
	}, s.runHandler)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_sessions",
		Description: "List hakase chat sessions (most recently updated first), including the session ids a `run` result can be resumed with.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listSessionsIn) (*mcp.CallToolResult, listSessionsOut, error) {
		out, err := s.listSessions(in)
		if err != nil {
			return nil, listSessionsOut{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_session",
		Description: "Read a hakase session's messages (id, title, role/content per turn). Use list_sessions to find ids.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in getSessionIn) (*mcp.CallToolResult, getSessionOut, error) {
		out, err := s.getSession(in)
		if err != nil {
			return nil, getSessionOut{}, err
		}
		return nil, out, nil
	})
}

// server holds the cross-invocation state the run tool needs: the suspended
// run registry (state token -> run) and the concurrency tracker.
type server struct {
	deps    Deps
	mu      sync.Mutex
	runs    map[string]*suspendedRun
	tracker *runTracker
}

// suspendedRun tracks one in-flight run across the tool-call round trips a
// mid-run gate requires. The run goroutine keeps executing while the tool
// call is suspended; the next invocation resumes it via the state token.
type suspendedRun struct {
	broker    *gateBroker
	state     string
	sessionID string

	done    chan struct{}
	once    sync.Once
	result  runOut
	forget  func()
	pending map[string]*gateQuery
	seq     int
	mu      sync.Mutex
}

// finish marks the run complete and schedules registry cleanup.
func (sr *suspendedRun) finish() {
	sr.once.Do(func() { close(sr.done) })
	if sr.forget != nil {
		time.AfterFunc(runStateTTL, sr.forget)
	}
}

// deliver routes fulfilled input responses to their waiting gate queries.
// Each query's response channel is buffered so a late delivery after the
// gate's expiry is dropped, never blocking.
func (sr *suspendedRun) deliver(responses mcp.InputResponseMap) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	for id, resp := range responses {
		q, ok := sr.pending[id]
		if !ok {
			continue
		}
		delete(sr.pending, id)
		elicit, ok := resp.(*mcp.ElicitResult)
		if !ok || elicit == nil {
			q.respCh <- gateResp{err: fmt.Errorf("input response %s is not an elicitation result (fail-closed)", id)}
			continue
		}
		q.respCh <- gateResp{res: elicit}
	}
}

// gateQuery is one elicitation a run's gate wants the driving client to
// answer.
type gateQuery struct {
	params *mcp.ElicitParams
	respCh chan gateResp
}

type gateResp struct {
	res *mcp.ElicitResult
	err error
}

// gateBroker carries gate queries from a run's gates to the run tool
// handler's input-required returns. Unbuffered: a gate ask issued while no
// invocation is serving the run blocks until the client's retry resumes it -
// that suspension IS the gate round trip.
type gateBroker struct {
	queries chan *gateQuery
}

// ask submits one elicitation and waits for its fulfillment, bounded by ctx
// (the gate expiry). On expiry the caller fails closed; the query, if
// already announced to the client, is answered into its buffered channel and
// dropped.
func (b *gateBroker) ask(ctx context.Context, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
	q := &gateQuery{params: params, respCh: make(chan gateResp, 1)}
	select {
	case b.queries <- q:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-q.respCh:
		return r.res, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// newStateToken returns a random 128-bit hex token used as the opaque
// RequestState. Stdio MCP is local trust; the token's only job is that a
// retry cannot be routed to someone else's run without guessing it.
func newStateToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is a system-level problem; a degenerate token
		// only weakens cross-run routing isolation, not the run itself.
		return "state-fallback"
	}
	return hex.EncodeToString(b[:])
}

// runIn is the `run` tool input.
type runIn struct {
	Prompt string `json:"prompt" jsonschema:"the user prompt to send to hakase"`
	// SessionID continues an existing thread when set.
	SessionID string `json:"session_id,omitempty" jsonschema:"session to continue (from an earlier run/list_sessions result); omit to start a fresh session"`
	// TimeoutSeconds bounds the run; 0 uses the default (900s).
	TimeoutSeconds int `json:"timeout_seconds,omitempty" jsonschema:"max seconds for the run including gate round trips (default 900, max 86400)"`
}

// runOut is the `run` tool result. Status is one of completed, error,
// cancelled, timeout.
type runOut struct {
	SessionID string   `json:"session_id"`
	Status    string   `json:"status"`
	Answer    string   `json:"answer,omitempty"`
	Thinking  string   `json:"thinking,omitempty"`
	Activity  []string `json:"activity,omitempty"`
	Tokens    int      `json:"tokens,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// runHandler implements the `run` tool, including its resume half: an
// invocation either starts a run (fresh state token) or resumes a suspended
// one (RequestState set by the client's retry, InputResponses carrying the
// fulfilled gate answers), then serves completion or the next gate query.
func (s *server) runHandler(ctx context.Context, req *mcp.CallToolRequest, in runIn) (*mcp.CallToolResult, runOut, error) {
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return nil, runOut{}, errors.New("prompt must not be empty")
	}
	if s.deps.Sessions == nil {
		return nil, runOut{}, errors.New("hakase sessions are unavailable in this process")
	}
	if s.deps.Runner == nil {
		return nil, runOut{}, errors.New("hakase runs are not wired in this process (skills-only server)")
	}
	timeout := defaultRunTimeout
	if in.TimeoutSeconds != 0 {
		if in.TimeoutSeconds < 0 {
			return nil, runOut{}, errors.New("timeout_seconds must not be negative")
		}
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
		if timeout > maxRunTimeout {
			return nil, runOut{}, fmt.Errorf("timeout_seconds above the %s cap", maxRunTimeout)
		}
	}

	// Resume or start.
	var sr *suspendedRun
	if req.Params != nil && req.Params.RequestState != "" {
		s.mu.Lock()
		sr = s.runs[req.Params.RequestState]
		s.mu.Unlock()
		if sr == nil {
			return nil, runOut{}, errors.New(
				"unknown or expired run state; the run has finished and its output is in the session - use get_session to read it")
		}
		if in.SessionID != "" && in.SessionID != sr.sessionID {
			return nil, runOut{}, fmt.Errorf("run state belongs to session %s, not %s", sr.sessionID, in.SessionID)
		}
		if req.Params.InputResponses != nil {
			sr.deliver(req.Params.InputResponses)
		}
	} else {
		var err error
		sr, err = s.startRun(ctx, req, prompt, in.SessionID, timeout)
		if err != nil {
			return nil, runOut{}, err
		}
	}

	// Serve completion or the next gate query. On completion the typed
	// return value becomes the tool result; on a query the input-required
	// result suspends the call until the client's retry resumes it.
	// Cancellation ends only the call: the run keeps executing on its
	// detached context and its output stays retrievable via the session.
	for {
		select {
		case q := <-sr.broker.queries:
			// Register the query under a fresh id: exactly one query is
			// outstanding at a time (a run's gates ask sequentially), and a
			// second ask blocks on the unbuffered channel until this handler
			// is re-invoked by the client's retry.
			sr.mu.Lock()
			sr.seq++
			id := fmt.Sprintf("gate-%d", sr.seq)
			sr.pending[id] = q
			requests := mcp.InputRequestMap{id: q.params}
			state := sr.state
			sr.mu.Unlock()
			return &mcp.CallToolResult{InputRequests: requests, RequestState: state}, runOut{}, nil
		case <-sr.done:
			return nil, sr.result, nil
		case <-ctx.Done():
			return nil, runOut{}, fmt.Errorf("run call cancelled; run continues in session %s (use get_session): %w", sr.sessionID, ctx.Err())
		}
	}
}

// startRun resolves the session, takes the run slot, and launches the run
// goroutine on a detached context (the request context dies when this call
// returns an input-required result; the run must survive across round trips,
// keeping ctx values for tracing, sandbox pinning and project binding).
func (s *server) startRun(ctx context.Context, req *mcp.CallToolRequest, prompt, sessionID string, timeout time.Duration) (*suspendedRun, error) {
	if sessionID != "" {
		if _, err := s.deps.Sessions.Store().Load(sessionID); err != nil {
			return nil, fmt.Errorf("session %s not found: %w", sessionID, err)
		}
	} else {
		sess, err := s.deps.Sessions.CreateSession(titleFromPrompt(prompt))
		if err != nil {
			return nil, fmt.Errorf("create session: %w", err)
		}
		sessionID = sess.ID
	}
	if err := s.tracker.TryStart(sessionID); err != nil {
		return nil, err
	}

	sr := &suspendedRun{
		broker:    &gateBroker{queries: make(chan *gateQuery)},
		state:     newStateToken(),
		sessionID: sessionID,
		done:      make(chan struct{}),
		pending:   map[string]*gateQuery{},
	}
	sr.forget = func() {
		s.mu.Lock()
		delete(s.runs, sr.state)
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.runs[sr.state] = sr
	s.mu.Unlock()

	// Persist the user turn first - the same contract as the web handler and
	// Telegram - so the turn survives even an abandoned run.
	if err := s.deps.Sessions.RecordUsageInSession(sessionID, "user", prompt, "", 0, nil); err != nil {
		s.tracker.Finish(sessionID)
		sr.forget()
		return nil, fmt.Errorf("save user message: %w", err)
	}

	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	sink := newRunSink(runCtx, req, s.deps.Log)
	if s.deps.Gates != nil {
		s.deps.Gates.attach(sessionID, sr.broker)
	}
	go func() {
		defer func() {
			cancel()
			if s.deps.Gates != nil {
				s.deps.Gates.detach(sessionID, sr.broker)
			}
			s.tracker.Finish(sessionID)
			sr.finish()
		}()
		s.deps.Runner.RunTurn(runCtx, sessionID, genai.NewContentFromText(prompt, genai.RoleUser), sink)

		out := runOut{
			SessionID: sessionID,
			Answer:    sink.answer(),
			Thinking:  sink.thinking(),
			Activity:  sink.activity(),
			Tokens:    sink.tokens(),
		}
		switch {
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			out.Status = "timeout"
			out.Error = "run exceeded timeout_seconds; partial output is persisted in the session"
		case runCtx.Err() != nil:
			out.Status = "cancelled"
			out.Error = "run cancelled; partial output is persisted in the session"
		case sink.errMsg() != "" && out.Answer == "":
			out.Status = "error"
			out.Error = sink.errMsg()
		default:
			out.Status = "completed"
		}
		sr.mu.Lock()
		sr.result = out
		sr.mu.Unlock()
	}()
	return sr, nil
}

// listSessionsIn is the `list_sessions` input.
type listSessionsIn struct {
	IncludeArchived bool `json:"include_archived,omitempty" jsonschema:"also include archived sessions"`
	Limit           int  `json:"limit,omitempty" jsonschema:"return at most N most recently updated sessions"`
}

// listSessionsOut is the `list_sessions` result.
type listSessionsOut struct {
	Sessions []hakasesession.SessionSummary `json:"sessions"`
}

// listSessions implements the `list_sessions` tool.
func (s *server) listSessions(in listSessionsIn) (listSessionsOut, error) {
	if s.deps.Sessions == nil {
		return listSessionsOut{}, errors.New("hakase sessions are unavailable in this process")
	}
	summaries, err := s.deps.Sessions.ListSessions()
	if err != nil {
		return listSessionsOut{}, fmt.Errorf("list sessions: %w", err)
	}
	if in.IncludeArchived {
		archived, err := s.deps.Sessions.ListArchivedSessions()
		if err != nil {
			return listSessionsOut{}, fmt.Errorf("list archived sessions: %w", err)
		}
		summaries = append(summaries, archived...)
		sort.Slice(summaries, func(i, j int) bool {
			return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
		})
	}
	if in.Limit > 0 && in.Limit < len(summaries) {
		summaries = summaries[:in.Limit]
	}
	if summaries == nil {
		summaries = []hakasesession.SessionSummary{}
	}
	return listSessionsOut{Sessions: summaries}, nil
}

// getSessionIn is the `get_session` input.
type getSessionIn struct {
	SessionID string `json:"session_id" jsonschema:"the session to read"`
	Limit     int    `json:"limit,omitempty" jsonschema:"return only the last N messages"`
}

// getSessionOut is the `get_session` result.
type getSessionOut struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	ProjectID   string       `json:"project_id,omitempty"`
	ProjectName string       `json:"project_name,omitempty"`
	UpdatedAt   time.Time    `json:"updated_at"`
	Messages    []messageOut `json:"messages"`
}

// messageOut is one turn of a session read.
type messageOut struct {
	Sequence    int64                         `json:"sequence"`
	Role        string                        `json:"role"`
	Content     string                        `json:"content"`
	Thinking    string                        `json:"thinking,omitempty"`
	Kind        string                        `json:"kind,omitempty"`
	Timestamp   time.Time                     `json:"timestamp"`
	Attachments []hakasesession.AttachmentRef `json:"attachments,omitempty"`
}

// getSession implements the `get_session` tool.
func (s *server) getSession(in getSessionIn) (getSessionOut, error) {
	if s.deps.Sessions == nil {
		return getSessionOut{}, errors.New("hakase sessions are unavailable in this process")
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return getSessionOut{}, errors.New("session_id must not be empty")
	}
	sess, err := s.deps.Sessions.Store().Load(in.SessionID)
	if err != nil {
		return getSessionOut{}, fmt.Errorf("session %s not found: %w", in.SessionID, err)
	}
	msgs := sess.Messages
	if in.Limit > 0 && in.Limit < len(msgs) {
		msgs = msgs[len(msgs)-in.Limit:]
	}
	out := getSessionOut{
		ID:          sess.ID,
		Title:       sess.Title,
		ProjectID:   sess.ProjectID,
		ProjectName: sess.ProjectName,
		UpdatedAt:   sess.UpdatedAt,
		Messages:    make([]messageOut, 0, len(msgs)),
	}
	for _, m := range msgs {
		out.Messages = append(out.Messages, messageOut{
			Sequence:    m.Sequence,
			Role:        m.Role,
			Content:     m.Content,
			Thinking:    m.Thinking,
			Kind:        m.Kind,
			Timestamp:   m.Timestamp,
			Attachments: m.Attachments,
		})
	}
	return out, nil
}

// titleFromPrompt derives a session title from the prompt, mirroring
// SessionService.ensureActiveSession's 60-char cap (rune-safe).
func titleFromPrompt(prompt string) string {
	r := []rune(prompt)
	if len(r) > titleRunes {
		return string(r[:titleRunes]) + "..."
	}
	return prompt
}

// runTracker enforces one run per session and the process-wide cap. Modeled
// on the Telegram channel's runs map (transport concern, not driver logic).
type runTracker struct {
	mu     sync.Mutex
	active map[string]int
}

var (
	errSessionBusy   = errors.New("a run is already active in this session; wait for it to finish")
	errRunsSaturated = fmt.Errorf("too many concurrent hakase runs (cap %d); try again shortly", maxConcurrentRuns)
)

// TryStart takes the session's run slot, refusing when it is held or the
// process-wide cap is reached. The slot is held for the whole run lifetime,
// including suspended gate round trips.
func (t *runTracker) TryStart(sessionID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active[sessionID] > 0 {
		return errSessionBusy
	}
	total := 0
	for _, n := range t.active {
		total += n
	}
	if total >= maxConcurrentRuns {
		return errRunsSaturated
	}
	t.active[sessionID]++
	return nil
}

// Finish releases the session's run slot.
func (t *runTracker) Finish(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := t.active[sessionID]; n <= 1 {
		delete(t.active, sessionID)
	} else {
		t.active[sessionID] = n - 1
	}
}
