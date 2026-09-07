package interfaces

import (
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"
)

// Payload caps for canvas events, enforced by the emitter (never the
// transport): they bound both the SSE frame size and the ring buffer's memory.
const (
	// GraphArgsCap caps serialized tool-call arguments.
	GraphArgsCap = 8 * 1024
	// GraphResultCap caps serialized tool results.
	GraphResultCap = 64 * 1024
	// GraphTextCap caps a single agent_text/agent_thought delta (clients
	// accumulate deltas per node under their own cap).
	GraphTextCap = 4 * 1024
	// GraphLabelCap caps goal/summary previews.
	GraphLabelCap = 200
)

// GraphEvent is one structured execution-canvas event: a node lifecycle
// transition (agent start/end), a tool call with its arguments and result, a
// sub-agent reasoning delta, or an agent transfer. Events are published under
// the hakase session topic (see EventNotifier.EmitGraphEvent) and rendered by
// the web UI's execution canvas; transports without a canvas may ignore them.
//
// NodeID is the graph node an event belongs to: the root run's taskID for the
// orchestrator, a delegation's taskID for sub-agents, and the owning agent
// node for tool calls. ParentID links a delegation node to the agent that
// spawned it.
//
// The struct marshals directly onto the wire; the SSE bridge wraps it with a
// per-session sequence number and timestamp. Large payloads (Args, Result,
// Text, Goal, Summary) are truncated by the emitter, never by the transport.
type GraphEvent struct {
	Type string `json:"type"` // agent_start|agent_end|tool_start|tool_end|agent_text|agent_thought|transfer

	NodeID   string `json:"node_id"`             // owning graph node (taskID, or transfer:<agent>)
	ParentID string `json:"parent_id,omitempty"` // delegating agent node; empty for root nodes
	Agent    string `json:"agent,omitempty"`     // agent label: orchestrator, web_researcher, ...

	Goal    string `json:"goal,omitempty"`    // agent_start: the delegated goal / user prompt preview
	Status  string `json:"status,omitempty"`  // agent_end: completed|failed|timed_out
	Summary string `json:"summary,omitempty"` // agent_end: final answer preview
	Error   string `json:"error,omitempty"`   // agent_end/tool_end failure detail

	CallID     string          `json:"call_id,omitempty"` // tool_start/tool_end linkage; synthesized when the provider omits it
	Tool       string          `json:"tool,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`   // tool_start: pre-serialized (see GraphArgs)
	Result     string          `json:"result,omitempty"` // tool_end: serialized response, truncated
	OK         bool            `json:"ok,omitempty"`     // tool_end success flag
	DurationMs int64           `json:"duration_ms,omitempty"`

	Text   string `json:"text,omitempty"`   // agent_text / agent_thought delta
	Target string `json:"target,omitempty"` // transfer: target agent name
}

// Graph event types.
const (
	GraphAgentStart   = "agent_start"
	GraphAgentEnd     = "agent_end"
	GraphToolStart    = "tool_start"
	GraphToolEnd      = "tool_end"
	GraphAgentText    = "agent_text"
	GraphAgentThought = "agent_thought"
	GraphTransfer     = "transfer"
)

// Graph agent-end statuses.
const (
	GraphStatusCompleted = "completed"
	GraphStatusFailed    = "failed"
	GraphStatusTimedOut  = "timed_out"
)

// TruncateUTF8 caps s to max bytes without splitting a rune, appending an
// ellipsis when cut. Use it for canvas payloads whose cap is a wire-size
// bound; Truncate (internal/agent) remains the rune-based variant for UI text.
func TruncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}

// GraphArgs serializes tool-call arguments for a GraphEvent, capping the
// payload at GraphArgsCap. Oversized argument maps degrade to
// {"_truncated": "<capped json>"} so the wire stays valid JSON and the client
// can tell the difference from missing arguments.
func GraphArgs(args map[string]any) json.RawMessage {
	if len(args) == 0 {
		return nil
	}
	b, err := json.Marshal(args)
	if err != nil {
		b, err = json.Marshal(map[string]string{"_unserializable": fmt.Sprintf("%v", args)})
		if err != nil {
			return nil
		}
	}
	if len(b) <= GraphArgsCap {
		return b
	}
	return json.RawMessage(`{"_truncated":` + strconv.Quote(TruncateUTF8(string(b), GraphArgsCap)) + `}`)
}
