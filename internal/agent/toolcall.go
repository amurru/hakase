package agent

import (
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// MaxToolCallRepairAttempts caps how many times a malformed tool call may be
// sent back to the model for correction before the run is abandoned.
const MaxToolCallRepairAttempts = 2

// IsToolCallJSONErr reports whether err is a tool-call JSON parse failure from
// the OpenAI-compatible provider layer. It is nil-safe and matches on the
// error prefixes produced by the OpenAI SDK when it cannot unmarshal function
// call or streamed function arguments.
func IsToolCallJSONErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "openai: parse function call args") ||
		strings.Contains(msg, "openai: parse streamed function args")
}

// PseudoToolCallCloseTag is the marker of a model-emitted XML tool call: a
// literal </tool_call> block in the model's text output. Some
// OpenAI-compatible models intermittently emit this pseudo-format instead of
// a native function call, so nothing executes and the raw XML leaks into the
// chat as the answer. Only the closing tag counts: a bare prose mention of
// tool calls (or a call truncated by the output cap before it closed) must
// not trigger a repair round.
const PseudoToolCallCloseTag = "</tool_call>"

// ContainsPseudoToolCall reports whether the model's text output for one
// pass contains a complete XML pseudo tool call that was never dispatched
// as a native function call. Matching is case-insensitive; nil-safe.
func ContainsPseudoToolCall(text string) bool {
	if text == "" {
		return false
	}
	return strings.Contains(strings.ToLower(text), PseudoToolCallCloseTag)
}

// PseudoToolCallRepairMessage builds a corrective user message for the model
// after it emitted an XML <tool_call> block instead of a native function
// call. It states that nothing was executed and orders a native re-emit
// without markup wrappers (models that reach for pseudo-XML also tend to
// echo the UNTRUSTED_DATA guard tag around it). When attempt is positive,
// the message is prefixed with "Attempt %d: ".
func PseudoToolCallRepairMessage(attempt int) *genai.Content {
	var sb strings.Builder
	if attempt > 0 {
		fmt.Fprintf(&sb, "Attempt %d: ", attempt)
	}
	sb.WriteString("Your previous message contained an XML <tool_call> block instead of a native function call, so no tool was executed. ")
	sb.WriteString("Re-emit the call using the native function-calling mechanism (a proper function call part with the tool name and JSON arguments). ")
	sb.WriteString("Do not write XML tags and do not wrap the call in UNTRUSTED_DATA or any other markup.")
	return genai.NewContentFromText(sb.String(), genai.RoleUser)
}

// ToolCallRepairMessage builds a corrective user message for the model after a
// tool call was rejected because its JSON arguments were malformed. The
// message includes the parse error, instructs the model to escape every
// newline and quote inside string values, and asks it to emit only the
// corrected call. When attempt is positive, the message is prefixed with
// "Attempt %d: ".
func ToolCallRepairMessage(err error, attempt int) *genai.Content {
	var sb strings.Builder
	if attempt > 0 {
		fmt.Fprintf(&sb, "Attempt %d: ", attempt)
	}
	fmt.Fprintf(&sb, "Your previous tool call was rejected because its JSON arguments were malformed. Parse error: %v. ", err)
	sb.WriteString("Escape every newline and double quote inside string values (\\n and \\\"). ")
	sb.WriteString("Emit only the corrected tool call, with valid JSON arguments.")
	return genai.NewContentFromText(sb.String(), genai.RoleUser)
}
