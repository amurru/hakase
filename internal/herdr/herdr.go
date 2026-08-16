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
	r.reportSession(seq, sessionID)
	r.reportState(state, seq, message, sessionID)
}

// reportSession registers (or refreshes) the hakase agent with Herdr. Without
// this, report-agent updates are ignored and the agent never appears in
// Herdr's agent menu.
func (r *Reporter) reportSession(seq uint64, sessionID string) {
	args := []string{
		"pane", "report-agent-session", r.pane,
		"--source", sourceName,
		"--agent", agentName,
		"--seq", strconv.FormatUint(seq, 10),
	}
	if sessionID != "" {
		args = append(args, "--agent-session-id", sessionID)
	}
	r.exec(args)
}

// reportState pushes the current lifecycle state.
func (r *Reporter) reportState(state string, seq uint64, message, sessionID string) {
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
	r.exec(args)
}

// Release returns lifecycle authority for this source back to Herdr. Call it
// when hakase exits so Herdr stops tracking the agent.
//
// The --seq must be strictly greater than every prior report's seq, otherwise
// Herdr treats the release as stale and keeps the agent registered (which is
// exactly what left a stuck "hakase" entry in the agents menu on exit).
func (r *Reporter) Release() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.seq++
	seq := r.seq
	r.mu.Unlock()

	r.exec([]string{
		"pane", "release-agent", r.pane,
		"--source", sourceName,
		"--agent", agentName,
		"--seq", strconv.FormatUint(seq, 10),
	})
}

// reportWorkQueue is a channel that queues report operations to a serialized
// worker goroutine. This prevents blocking the TUI AppModel.View rendering.
var reportWorkQueue chan func()

var reportWorkQueueOnce sync.Once

func initReportWorkQueue() {
	reportWorkQueueOnce.Do(func() {
		reportWorkQueue = make(chan func(), 100)
		go reportWorker()
	})
}

func reportWorker() {
	for fn := range reportWorkQueue {
		fn()
	}
}

// exec runs the Herdr CLI in a separate goroutine with a short timeout so a
// slow or missing daemon can never block the agent's UI thread. Output is
// discarded so it cannot pollute the agent's terminal.
// When reportWorkQueue is initialized (by calling a Report method), this
// runs through the queued worker instead of blocking the caller.
func (r *Reporter) exec(args []string) {
	if reportWorkQueue != nil {
		// Queue to worker if the queue is initialized
		reportWorkQueue <- func() {
			r.execSync(args)
		}
		return
	}
	// Fallback to direct execution for backward compatibility
	r.execSync(args)
}

// execSync is the actual implementation that runs the Herdr CLI command.
func (r *Reporter) execSync(args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	_ = cmd.Run()
}
