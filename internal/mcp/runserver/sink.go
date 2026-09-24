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
type runSink struct {
	ctx     context.Context
	session *mcp.ServerSession
	token   any
	log     interfaces.LogFunc

	mu            sync.Mutex
	content       strings.Builder
	thinkBuf      strings.Builder
	activityLines []string
	errLine       string
	tokenCount    int
	events        float64
}

func newRunSink(ctx context.Context, req *mcp.CallToolRequest, log interfaces.LogFunc) *runSink {
	s := &runSink{ctx: ctx, log: log}
	if req != nil {
		s.session = req.Session
		s.token = req.Params.GetProgressToken()
	}
	return s
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

// notify sends one progress notification. Fire-and-forget: notification
// failures (client gone mid-run) must not fail the run; the call context
// cancellation ends the run through the driver.
func (s *runSink) notify(message string) {
	if s.session == nil || s.token == nil {
		return
	}
	s.mu.Lock()
	s.events++
	progress := s.events
	s.mu.Unlock()
	_ = s.session.NotifyProgress(s.ctx, &mcp.ProgressNotificationParams{
		ProgressToken: s.token,
		Progress:      progress,
		Message:       interfaces.TruncateUTF8(message, maxActivityLineLen),
	})
}
