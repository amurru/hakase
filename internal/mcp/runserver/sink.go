package runserver

import (
	"context"
	"strings"
	"sync"

	"amurru/hakase/internal/interfaces"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// maxActivityLines bounds the activity buffer returned by `run` (last N
	// lines kept, oldest dropped); maxActivityLineLen caps one line.
	maxActivityLines   = 200
	maxActivityLineLen = 500

	// errorLogPrefix is the line the agentrun driver emits on run errors
	// (agentrun.go: "Error: %v"). The sink mirrors it into the result's
	// error field; keep in step if the driver's format changes.
	errorLogPrefix = "Error: "
)

// runSink adapts agentrun.EventSink to one `run` tool call: it accumulates
// the answer/thinking text and a bounded activity buffer, and forwards every
// stream delta and activity line to the driving MCP client as a progress
// notification (when the call carries a progress token - clients that do not
// send one simply get the final result).
//
// Progress is bound to the call currently being served, not to the call that
// started the run. MCP only accepts a progress notification while the request
// carrying its token is in flight, and a run that hits a gate spans several
// calls: each round trip returns an input-required result and the client
// re-enters with a fresh token. The handler therefore binds the sink on
// every invocation and unbinds it on the way out, so a delta that lands
// while no call is in flight is buffered but never emitted against a
// completed request.
// progressNotifier is the slice of *mcp.ServerSession the sink needs, so a
// test can observe what would be sent without a live stdio session.
type progressNotifier interface {
	NotifyProgress(context.Context, *mcp.ProgressNotificationParams) error
}

type runSink struct {
	mu      sync.Mutex
	ctx     context.Context
	session progressNotifier
	token   any
	// active is true only while a request is being served.
	active bool
	log    interfaces.LogFunc

	content       strings.Builder
	thinkBuf      strings.Builder
	activityLines []string
	errLine       string
	tokenCount    int
	events        float64
}

// bindToken points the sink at one session/token pair and marks it active.
func (s *runSink) bindToken(ctx context.Context, n progressNotifier, token any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
	s.session = n
	s.token = token
	s.active = true
}

// bind points the sink at the request being served right now. A client is
// not required to send a progress token, in which case nothing is emitted
// and only the final result matters.
func (s *runSink) bind(ctx context.Context, req *mcp.CallToolRequest) {
	var (
		n     progressNotifier
		token any
	)
	if req != nil {
		n = req.Session
		if req.Params != nil {
			token = req.Params.GetProgressToken()
		}
	}
	s.bindToken(ctx, n, token)
}

// unbind marks the sink as having no in-flight request. The run keeps
// executing on its detached context after a gate suspends the call, so
// without this a later delta would notify a token whose request is done.
func (s *runSink) unbind() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = false
}

// OnStream reports a text delta: appended to the answer/thinking buffers and
// pushed as a progress message.
func (s *runSink) OnStream(sessionID, content, thinking string) {
	s.mu.Lock()
	if content != "" {
		s.content.WriteString(content)
	}
	if thinking != "" {
		s.thinkBuf.WriteString(thinking)
	}
	s.mu.Unlock()
	if content != "" {
		s.notify("hakase: " + content)
	} else if thinking != "" {
		s.notify("hakase (thinking): " + thinking)
	}
}

// OnLog reports one activity line: buffered (bounded) and pushed as a
// progress message. Lines matching the driver's error format are mirrored
// into the result's error field.
func (s *runSink) OnLog(sessionID, line string) {
	s.mu.Lock()
	s.activityLines = append(s.activityLines, interfaces.TruncateUTF8(line, maxActivityLineLen))
	if len(s.activityLines) > maxActivityLines {
		s.activityLines = s.activityLines[len(s.activityLines)-maxActivityLines:]
	}
	if strings.HasPrefix(line, errorLogPrefix) {
		s.errLine = line
	}
	s.mu.Unlock()
	s.notify("hakase: " + line)
	if s.log != nil {
		s.log("mcp run (session " + sessionID + "): " + line)
	}
}

// OnUsage records the latest token usage.
func (s *runSink) OnUsage(sessionID string, tokens, percent int) {
	s.mu.Lock()
	s.tokenCount = tokens
	s.mu.Unlock()
}

// OnDone signals turn completion; the `run` handler unblocks on RunTurn
// returning, so nothing to do here.
func (s *runSink) OnDone(sessionID string) {}

// OnGraphEvent carries execution-canvas events; MCP has no canvas in v1.
func (s *runSink) OnGraphEvent(sessionID string, ev interfaces.GraphEvent) {}

func (s *runSink) answer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.content.String()
}

func (s *runSink) thinking() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.thinkBuf.String()
}

func (s *runSink) activity() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.activityLines) == 0 {
		return nil
	}
	out := make([]string, len(s.activityLines))
	copy(out, s.activityLines)
	return out
}

func (s *runSink) tokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokenCount
}

func (s *runSink) errMsg() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errLine
}

// notify sends one progress notification for the call currently being
// served. Fire-and-forget: notification failures (client gone mid-run) must
// not fail the run; the call context cancellation ends the run through the
// driver. Emitting nothing while unbound is deliberate - the run outlives
// the call that started it, and a notification for a completed request is
// protocol-invalid.
func (s *runSink) notify(message string) {
	s.mu.Lock()
	if !s.active || s.session == nil || s.token == nil {
		s.mu.Unlock()
		return
	}
	s.events++
	progress := s.events
	ctx, session, token := s.ctx, s.session, s.token
	s.mu.Unlock()
	_ = session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
		ProgressToken: token,
		Progress:      progress,
		Message:       interfaces.TruncateUTF8(message, maxActivityLineLen),
	})
}
