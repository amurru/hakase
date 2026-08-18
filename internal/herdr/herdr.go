// Package herdr reports hakase's agent lifecycle state to a Herdr pane so the
// terminal multiplexer can show idle / working / blocked status and (optionally)
// session identity. Reporting is a no-op outside Herdr, so a Reporter can be
// kept around in every deployment.
//
// The integration follows Herdr's "Integrate your own agent" contract:
// https://herdr.dev/docs/integrations/
package herdr

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

const (
	envEnabled = "HERDR_ENV"
	envBinPath = "HERDR_BIN_PATH"
	envPaneID  = "HERDR_PANE_ID"

	// sourceName is the stable, unique --source for hakase's own-agent reports.
	// Herdr uses it to scope lifecycle authority and ignore stale --seq values.
	sourceName = "custom:hakase"
	agentName  = "hakase"

	// Lifecycle states understood by Herdr.
	StateIdle    = "idle"
	StateWorking = "working"
	StateBlocked = "blocked"

	// commandTimeout bounds a single Herdr CLI invocation.
	commandTimeout = 3 * time.Second

	// releaseWait bounds how long Release waits for its queued command (and
	// any work still ahead of it) before giving up: worst case one report
	// pair mid-flight (2 x commandTimeout) plus the release command itself.
	releaseWait = 3*commandTimeout + time.Second
)

// Reporter pushes state to a Herdr pane through the Herdr CLI. All methods are
// safe to call on a nil *Reporter (no-op), which is what callers get when
// hakase is not running inside Herdr.
type Reporter struct {
	bin  string
	pane string

	mu        sync.Mutex
	seq       uint64
	lastState string
	lastMsg   string
	lastSess  string
}

// NewReporter builds a Reporter from the process environment. It returns nil
// when hakase is not running inside a Herdr-managed pane, so callers can treat
// a nil Reporter as "skip reporting".
//
// The herdr binary is located in this order: the HERDR_BIN_PATH hint (when set
// and pointing at an existing file), then the herdr binary resolved from PATH.
// The hint exists for environments that launch agents through a wrapper; in the
// common case herdr is simply on PATH.
func NewReporter() *Reporter {
	if os.Getenv(envEnabled) != "1" {
		return nil
	}
	pane := os.Getenv(envPaneID)
	if pane == "" {
		return nil
	}
	bin := resolveHerdrBin()
	if bin == "" {
		return nil
	}
	initReportWorkQueue()
	return &Reporter{bin: bin, pane: pane}
}

// resolveHerdrBin returns the path to the herdr CLI, preferring the
// HERDR_BIN_PATH hint and falling back to PATH lookup.
func resolveHerdrBin() string {
	if hint := os.Getenv(envBinPath); hint != "" {
		if _, err := os.Stat(hint); err == nil {
			return hint
		}
	}
	if path, err := exec.LookPath("herdr"); err == nil {
		return path
	}
	return ""
}

// Report sends the current lifecycle state to Herdr. message and sessionID are
// optional and omitted when empty. Reporting is best-effort: failures are
// swallowed and never affect the agent. Consecutive identical (state, message,
// session) reports are suppressed so the CLI only runs on a real transition.
//
// Each non-suppressed report first (re)establishes the agent session via
// report-agent-session. Herdr only surfaces an agent in its agent menu after
// the session is registered, so this must run before the state update; calling
// it on every transition also re-registers the agent if the Herdr server has
// been reloaded.
//
// Report never blocks: the session + state command pair is queued as a single
// work item on the serialized report worker (see reportQueue). This matters
// because the TUI calls Report from AppModel.View, on the render path.
func (r *Reporter) Report(state, message, sessionID string) {
	if r == nil {
		return
	}

	r.mu.Lock()
	if r.lastState == state && r.lastMsg == message && r.lastSess == sessionID {
		r.mu.Unlock()
		return
	}
	r.seq++
	seq := r.seq
	r.lastState = state
	r.lastMsg = message
	r.lastSess = sessionID
	r.mu.Unlock()

	// Herdr ignores reports whose --seq is not strictly greater than the last
	// accepted one, so every call (session registration and state update
	// alike) must carry a monotonic, increasing seq. We share a single seq
	// across the two calls in one Report so the state update always wins over
	// the session registration and subsequent Reports always win over prior
	// ones.
	sessionArgs := r.sessionArgs(seq, sessionID)
	stateArgs := r.stateArgs(state, seq, message, sessionID)
	submitReportWork(reportWork{
		run: func() {
			r.execSync(sessionArgs)
			r.execSync(stateArgs)
		},
		droppable: true,
	})
}

// sessionArgs builds the report-agent-session command for a report pair.
func (r *Reporter) sessionArgs(seq uint64, sessionID string) []string {
	args := []string{
		"pane", "report-agent-session", r.pane,
		"--source", sourceName,
		"--agent", agentName,
		"--seq", strconv.FormatUint(seq, 10),
	}
	if sessionID != "" {
		args = append(args, "--agent-session-id", sessionID)
	}
	return args
}

// stateArgs builds the report-agent command for a report pair.
func (r *Reporter) stateArgs(state string, seq uint64, message, sessionID string) []string {
	args := []string{
		"pane", "report-agent", r.pane,
		"--source", sourceName,
		"--agent", agentName,
		"--state", state,
		"--seq", strconv.FormatUint(seq, 10),
	}
	if message != "" {
		args = append(args, "--message", message)
	}
	if sessionID != "" {
		args = append(args, "--agent-session-id", sessionID)
	}
	return args
}

// releaseArgs builds the release-agent command.
func (r *Reporter) releaseArgs(seq uint64) []string {
	return []string{
		"pane", "release-agent", r.pane,
		"--source", sourceName,
		"--agent", agentName,
		"--seq", strconv.FormatUint(seq, 10),
	}
}

// Release returns lifecycle authority for this source back to Herdr. Call it
// when hakase exits so Herdr stops tracking the agent.
//
// The --seq must be strictly greater than every prior report's seq, otherwise
// Herdr treats the release as stale and keeps the agent registered (which is
// exactly what left a stuck "hakase" entry in the agents menu on exit).
//
// Release waits (bounded by releaseWait) for the queued release command to
// actually run before returning, because shutdown paths exit the process
// immediately after calling it; a fire-and-forget release could be abandoned
// with the agent still registered.
func (r *Reporter) Release() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.seq++
	seq := r.seq
	r.mu.Unlock()

	args := r.releaseArgs(seq)
	done := make(chan struct{})
	submitReportWork(reportWork{
		run: func() { r.execSync(args) },
		// Once authority is released, pending state reports are moot.
		supersedes: true,
		done:       done,
	})

	select {
	case <-done:
	case <-time.After(releaseWait):
	}
}

// reportWork is one serialized unit of Herdr CLI work.
type reportWork struct {
	run func()

	// droppable marks supersede-able state reports. Any later enqueue removes
	// pending droppable work from the queue: Herdr only honors the report
	// with the highest --seq, so an older pending report is redundant.
	droppable bool

	// supersedes marks work (release) after which pending state reports are
	// moot; enqueueing it clears all pending droppable work.
	supersedes bool

	// done is closed after run completes when the caller waits on it.
	done chan struct{}
}

// reportQueue serializes Herdr CLI invocations onto one worker goroutine so
// submitting work never blocks the caller (in particular the TUI render path,
// which calls Report from AppModel.View). The queue is an unbounded slice:
// coalescing in enqueue keeps at most one pending report pair queued, so it
// cannot grow without bound.
type reportQueue struct {
	mu     sync.Mutex
	items  []reportWork
	notify chan struct{}
}

var (
	rq     *reportQueue
	rqOnce sync.Once
)

func initReportWorkQueue() {
	rqOnce.Do(func() {
		rq = &reportQueue{notify: make(chan struct{}, 1)}
		go rq.worker()
	})
}

// submitReportWork queues work and returns immediately; it never blocks.
func submitReportWork(w reportWork) {
	initReportWorkQueue()
	rq.enqueue(w)
}

// enqueue adds work to the queue.
//
// A newly queued report carries a higher --seq than everything pending, and a
// release makes pending state work moot; in both cases pending droppable work
// is dropped first. This coalesces superseded state updates, keeps the queue
// short, and means submission can never block on a full queue.
func (q *reportQueue) enqueue(w reportWork) {
	q.mu.Lock()
	if w.droppable || w.supersedes {
		kept := q.items[:0]
		for _, it := range q.items {
			if !it.droppable {
				kept = append(kept, it)
			}
		}
		q.items = kept
	}
	q.items = append(q.items, w)
	q.mu.Unlock()

	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// worker runs queued work serially, preserving queue order: a report pair's
// session command always precedes its state command, and a release always
// follows the work queued before it.
func (q *reportQueue) worker() {
	for range q.notify {
		q.drain()
	}
}

func (q *reportQueue) drain() {
	for {
		q.mu.Lock()
		if len(q.items) == 0 {
			q.mu.Unlock()
			return
		}
		w := q.items[0]
		q.items = q.items[1:]
		q.mu.Unlock()

		w.run()
		if w.done != nil {
			close(w.done)
		}
	}
}

// flush blocks until every work item queued before it has completed, bounded
// by timeout. It reports whether the drain finished in time. Used by tests
// that need deterministic reporting.
func (r *Reporter) flush(timeout time.Duration) bool {
	if r == nil {
		return true
	}
	done := make(chan struct{})
	submitReportWork(reportWork{run: func() {}, done: done})
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// execSync runs one Herdr CLI command with a short timeout so a slow or
// missing daemon can never stall the worker for long. Output is discarded so
// it cannot pollute the agent's terminal.
func (r *Reporter) execSync(args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	_ = cmd.Run()
}
