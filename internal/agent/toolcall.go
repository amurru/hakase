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
