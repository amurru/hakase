package agent

import (
	"errors"
	"strings"
	"testing"
)

func TestIsToolCallJSONErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "generic error",
			err:  errors.New("generic error"),
			want: false,
		},
		{
			name: "network timeout",
			err:  errors.New("network timeout"),
			want: false,
		},
		{
			name: "parse function call args",
			err:  errors.New("openai: parse function call args: invalid character '\\n' in string literal"),
			want: true,
		},
		{
			name: "parse streamed function args",
			err:  errors.New("openai: parse streamed function args: unexpected end of JSON input"),
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsToolCallJSONErr(c.err); got != c.want {
				t.Fatalf("IsToolCallJSONErr(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestContainsPseudoToolCall(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{name: "empty", text: "", want: false},
		{name: "plain answer", text: "The zebra has black and white stripes.", want: false},
		{
			name: "prose mention without block",
			text: "I will now make a tool call to search the web.",
			want: false,
		},
		{
			name: "unclosed open tag only",
			text: "<tool_call><name>mcp_lightpanda_search</name>",
			want: false,
		},
		{
			name: "observed failure shape",
			text: "<UNTRUSTED_DATA>\nسأبحث عن صور حمار الوحش.\n</UNTRUSTED_DATA>\n</tool_call>",
			want: true,
		},
		{
			name: "case-insensitive close tag",
			text: "query here</TOOL_CALL>",
			want: true,
		},
		{
			name: "full block with args",
			text: "<tool_call>{\"name\": \"web_search\", \"args\": {\"query\": \"zebra\"}}</tool_call>",
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ContainsPseudoToolCall(c.text); got != c.want {
				t.Fatalf("ContainsPseudoToolCall(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

func TestPseudoToolCallRepairMessage(t *testing.T) {
	for _, attempt := range []int{0, 1} {
		msg := PseudoToolCallRepairMessage(attempt)
		if msg == nil {
			t.Fatal("PseudoToolCallRepairMessage returned nil content")
		}
		if msg.Role != "user" {
			t.Fatalf("content role = %q, want %q", msg.Role, "user")
		}
		var text strings.Builder
		for _, part := range msg.Parts {
			if part != nil && part.Text != "" {
				text.WriteString(part.Text)
			}
		}
		got := text.String()
		for _, want := range []string{"no tool was executed", "native function", "UNTRUSTED_DATA"} {
			if !strings.Contains(got, want) {
				t.Errorf("attempt %d message %q does not mention %q", attempt, got, want)
			}
		}
		if attempt > 0 && !strings.Contains(got, "Attempt 1: ") {
			t.Errorf("message %q missing attempt prefix", got)
		}
	}
}
func TestToolCallRepairMessage(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		attempt int
	}{
		{
			name:    "with parse error",
			err:     errors.New("openai: parse function call args: invalid character '\\n' in string literal"),
			attempt: 1,
		},
		{
			name:    "streamed error no attempt prefix",
			err:     errors.New("openai: parse streamed function args: unexpected end of JSON input"),
			attempt: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := ToolCallRepairMessage(c.err, c.attempt)
			if msg == nil {
				t.Fatal("ToolCallRepairMessage returned nil content")
			}
			if msg.Role != "user" {
				t.Fatalf("content role = %q, want %q", msg.Role, "user")
			}
			var text strings.Builder
			for _, part := range msg.Parts {
				if part != nil && part.Text != "" {
					text.WriteString(part.Text)
				}
			}
			got := text.String()
			if !strings.Contains(got, "malformed") {
				t.Errorf("message %q does not mention malformed arguments", got)
			}
			if !strings.Contains(got, c.err.Error()) {
				t.Errorf("message %q does not include parse error %q", got, c.err.Error())
			}
			if c.attempt > 0 {
				if !strings.Contains(got, "Attempt 1: ") {
					t.Errorf("message %q missing attempt prefix", got)
				}
			}
		})
	}
}
