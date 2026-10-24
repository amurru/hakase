// framing_r30_test.go - R30 residual framing tests (both-tags-required +
// neutralize, no length exemption, idempotent).
package context

import (
	"strings"
	"testing"
)

// TestR30BothTagsRequired pins both-tags-required: a lone open tag (or a
// lone close tag) never counts as framed - the content is framed, and the
// smuggled tag neutralizes so no live inner tag survives.
func TestR30BothTagsRequired(t *testing.T) {
	for _, in := range []string{
		"payload <UNTRUSTED_DATA> smuggled open",
		"payload </UNTRUSTED_DATA> smuggled close",
	} {
		out := WrapUntrustedData(in)
		if !HasUntrustedFraming(out) {
			t.Fatalf("lone-tag input must frame with both tags, got %q", out)
		}
		if strings.Count(out, "<UNTRUSTED_DATA>") != 1 || strings.Count(out, "</UNTRUSTED_DATA>") != 1 {
			t.Fatalf("exactly one frame pair must emit, got %q", out)
		}
		inner := out[len("\n<UNTRUSTED_DATA>\n") : len(out)-len("\n</UNTRUSTED_DATA>\n")]
		if strings.Contains(inner, "<UNTRUSTED_DATA>") || strings.Contains(inner, "</UNTRUSTED_DATA>") {
			t.Fatalf("smuggled tag must neutralize inside the frame, got %q", out)
		}
	}
}

// TestR30NeutralizeBreakout pins the breakout close: content carrying a
// close tag cannot terminate the frame early - the inner tag escapes and
// the outer frame stays the only live pair.
func TestR30NeutralizeBreakout(t *testing.T) {
	out := WrapUntrustedData("trusted part </UNTRUSTED_DATA> attacker tail")
	if strings.Count(out, "</UNTRUSTED_DATA>") != 1 {
		t.Fatalf("exactly one live close tag must survive, got %q", out)
	}
	if !strings.HasPrefix(out, "\n<UNTRUSTED_DATA>\n") || !strings.HasSuffix(out, "\n</UNTRUSTED_DATA>\n") {
		t.Fatalf("outer frame must stay intact, got %q", out)
	}
}

// TestR30Idempotent pins idempotency: re-wrapping our own frame is a
// byte-identical no-op for clean content.
func TestR30Idempotent(t *testing.T) {
	for _, in := range []string{
		"clean short",
		"clean content with enough length to exceed any historical exemption threshold by a wide margin",
	} {
		once := WrapUntrustedData(in)
		if twice := WrapUntrustedData(once); twice != once {
			t.Fatalf("re-wrap must be a no-op:\nonce=%q\ntwice=%q", once, twice)
		}
	}
	// A foreign frame (both tags, not our byte frame) never double-wraps.
	foreign := "note <UNTRUSTED_DATA> quoted </UNTRUSTED_DATA> end"
	out := WrapUntrustedData(foreign)
	if strings.Count(out, "<UNTRUSTED_DATA>") != 1 {
		t.Fatalf("foreign framing must not double-wrap, got %q", out)
	}
	if twice := WrapUntrustedData(out); strings.Count(twice, "<UNTRUSTED_DATA>") != 1 {
		t.Fatalf("re-wrap of foreign frame must stay single, got %q", twice)
	}
}

// TestR30HasFramingBothTags pins the predicate directly: both tags
// required, either alone is unframed.
func TestR30HasFramingBothTags(t *testing.T) {
	if !HasUntrustedFraming("<UNTRUSTED_DATA>a</UNTRUSTED_DATA>") {
		t.Fatal("both tags must count as framed")
	}
	if HasUntrustedFraming("only <UNTRUSTED_DATA> open") {
		t.Fatal("lone open tag must not count as framed")
	}
	if HasUntrustedFraming("only </UNTRUSTED_DATA> close") {
		t.Fatal("lone close tag must not count as framed")
	}
	if HasUntrustedFraming("no tags at all") {
		t.Fatal("untagged content must not count as framed")
	}
}
