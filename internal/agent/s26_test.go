// s26_test.go - output tag neutralization tests (S26).
package agent

import (
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
)

// TestS26BothTagsNeutralized pins both families: open <tag> and close
// </tag> spans escape (no live brackets survive), for tool-shaped and
// markup-shaped input.
func TestS26BothTagsNeutralized(t *testing.T) {
	for _, in := range []string{
		"<script>alert(1)</script>",
		"run <tool>ls</tool> now",
		"</close> leading close",
		"<unclosed trailing",
		"bare > bracket",
	} {
		out, changed := neutralizeFlowTags(in)
		if !changed {
			t.Fatalf("input %q must neutralize", in)
		}
		if strings.ContainsAny(out, "<>") {
			t.Fatalf("no live bracket may survive, got %q", out)
		}
	}
	if out, changed := neutralizeFlowTags("plain text, no tags"); changed || out != "plain text, no tags" {
		t.Fatalf("clean text must pass through unchanged, got %q changed=%v", out, changed)
	}
}

// TestS26LongTagWithheld pins no->32: a tag span longer than 32 runes is
// replaced with a length-only marker carrying no content.
func TestS26LongTagWithheld(t *testing.T) {
	long := "<" + strings.Repeat("x", 40) + ">"
	out, changed := neutralizeFlowTags("prefix " + long + " suffix")
	if !changed {
		t.Fatal("over-long tag must neutralize")
	}
	if strings.Contains(out, strings.Repeat("x", 40)) {
		t.Fatalf("over-long content must not emit, got %q", out)
	}
	if !strings.Contains(out, "[tag-withheld:len=") {
		t.Fatalf("over-long span must carry a length marker, got %q", out)
	}
	if strings.ContainsAny(out, "<>") {
		t.Fatalf("no live bracket may survive, got %q", out)
	}
}

// TestS26Boundary32Allowed pins the boundary: a 32-rune span escapes in
// place (content preserved, brackets escaped); 33 withholds.
func TestS26Boundary32Allowed(t *testing.T) {
	span32 := "<" + strings.Repeat("y", 30) + ">" // 32 runes total
	if got := len([]rune(span32)); got != 32 {
		t.Fatalf("fixture miscounted: %d", got)
	}
	out, _ := neutralizeFlowTags(span32)
	if strings.Contains(out, "[tag-withheld") {
		t.Fatalf("32-rune span must not withhold, got %q", out)
	}
	span33 := "<" + strings.Repeat("y", 31) + ">"
	out33, _ := neutralizeFlowTags(span33)
	if !strings.Contains(out33, "[tag-withheld") {
		t.Fatalf("33-rune span must withhold, got %q", out33)
	}
}

// TestS26NestedTag pins H2: nested brackets inside a span interior escape
// too - no live < or > survives, even for <<x>, <a <b>> shapes.
func TestS26NestedTag(t *testing.T) {
	for _, in := range []string{
		"<<script>>",
		"<a <b>>",
		"<<x>",
		"<outer <inner> tail>",
	} {
		out, changed := neutralizeFlowTags(in)
		if !changed {
			t.Fatalf("nested input %q must neutralize", in)
		}
		if strings.ContainsAny(out, "<>") {
			t.Fatalf("nested brackets must escape, input %q got %q", in, out)
		}
		if strings.Contains(out, "script>") && strings.Contains(out, "<script") {
			t.Fatalf("live tag survived in %q", out)
		}
	}
}

// TestS26DenialNeutralized pins the wiring: denial and preview renderers
// neutralize tag-bearing input (defense in depth against rule/query
// text reaching the model or an approval card live).
func TestS26DenialNeutralized(t *testing.T) {
	label := permissions.StartLabel()
	if text := flowDenialText("<script>", &label, permissions.EffectAsk); strings.ContainsAny(text, "<>") {
		t.Fatalf("denial text must neutralize tags, got %q", text)
	}
	if p := flowApprovalPreview("send_message", []string{"<img src=x onerror=y>"}); strings.ContainsAny(p, "<>") {
		t.Fatalf("approval preview must neutralize tags, got %q", p)
	}
	if r := flowWithholdOverride("send_message", "<b>hi</b>", &label); strings.ContainsAny(r["reason"].(string), "<>") {
		t.Fatalf("withhold reason must neutralize tags, got %q", r["reason"])
	}
}
