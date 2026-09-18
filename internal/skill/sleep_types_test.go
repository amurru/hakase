// sleep_types_test.go - SL-010 acceptance: edit algebra, gate, markers.
package skill

import (
	"strings"
	"testing"
)

const sleepTestDoc = `---
name: demo
description: Demo skill.
---

# Demo

Hand-written guidance that must never change.
`

func TestApplyEdits_AddCreatesLearnedBlock(t *testing.T) {
	newDoc, applied, unmatched := ApplyEdits(sleepTestDoc, []TextEdit{
		{Op: "add", Content: "Prefer metric units.", Rationale: "recurring correction"},
	})
	if len(applied) != 1 || len(unmatched) != 0 {
		t.Fatalf("applied=%d unmatched=%d", len(applied), len(unmatched))
	}
	if !strings.Contains(newDoc, LearnedStart) || !strings.Contains(newDoc, "- Prefer metric units.") {
		t.Errorf("learned block missing: %q", newDoc)
	}
	if !strings.Contains(newDoc, "Hand-written guidance that must never change.") {
		t.Error("hand-written content damaged")
	}
	front, _, ok := SplitSkillDoc(newDoc)
	origFront, _, _ := SplitSkillDoc(sleepTestDoc)
	if !ok || front != origFront {
		t.Error("frontmatter must be byte-identical after edits")
	}
}

func TestApplyEdits_DedupAndEmpty(t *testing.T) {
	doc, applied, _ := ApplyEdits(sleepTestDoc, []TextEdit{{Op: "add", Content: "Rule one."}})
	if len(applied) != 1 {
		t.Fatal("setup add failed")
	}
	_, applied2, unmatched2 := ApplyEdits(doc, []TextEdit{
		{Op: "add", Content: "  rule ONE. "}, // normalized duplicate
		{Op: "add", Content: "   "},          // empty
	})
	if len(applied2) != 0 || len(unmatched2) != 2 {
		t.Errorf("applied=%d unmatched=%d, want 0/2", len(applied2), len(unmatched2))
	}
}

func TestApplyEdits_DeleteReplaceInsert(t *testing.T) {
	doc, _, _ := ApplyEdits(sleepTestDoc, []TextEdit{
		{Op: "add", Content: "Alpha rule."},
		{Op: "add", Content: "Beta rule."},
		{Op: "add", Content: "Gamma rule."},
	})
	newDoc, applied, unmatched := ApplyEdits(doc, []TextEdit{
		{Op: "delete", Anchor: "beta"},
		{Op: "replace", Anchor: "gamma", Content: "Gamma rule, revised."},
		{Op: "insert_after", Anchor: "alpha", Content: "Alpha follow-up."},
		{Op: "delete", Anchor: "no-such-rule"},
		{Op: "frobnicate", Content: "x"},
	})
	if len(applied) != 3 || len(unmatched) != 2 {
		t.Fatalf("applied=%d unmatched=%d, want 3/2", len(applied), len(unmatched))
	}
	for _, want := range []string{"Alpha rule.", "Alpha follow-up.", "Gamma rule, revised."} {
		if !strings.Contains(newDoc, want) {
			t.Errorf("missing %q in %q", want, newDoc)
		}
	}
	if strings.Contains(newDoc, "Beta rule.") {
		t.Errorf("delete failed: %q", newDoc)
	}
	// Order: follow-up directly after alpha.
	if strings.Index(newDoc, "Alpha follow-up.") < strings.Index(newDoc, "Alpha rule.") {
		t.Errorf("insert_after misordered: %q", newDoc)
	}
}

func TestApplyEdits_NoopKeepsDocByteIdentical(t *testing.T) {
	newDoc, applied, unmatched := ApplyEdits(sleepTestDoc, []TextEdit{{Op: "delete", Anchor: "missing"}})
	if len(applied) != 0 || len(unmatched) != 1 {
		t.Fatalf("applied=%d unmatched=%d", len(applied), len(unmatched))
	}
	if newDoc != sleepTestDoc {
		t.Error("no-op must return the original document unchanged (no empty block)")
	}
}

func TestApplyEdits_StripsMarkersAndRejectsTargets(t *testing.T) {
	newDoc, applied, unmatched := ApplyEdits(sleepTestDoc, []TextEdit{
		{Op: "add", Content: "Real rule. <!-- SLOW_UPDATE_START --> hijack"},
		{Op: "add", Content: "Routed elsewhere.", Target: "memory"},
	})
	if len(applied) != 1 || len(unmatched) != 1 {
		t.Fatalf("applied=%d unmatched=%d, want 1/1", len(applied), len(unmatched))
	}
	if strings.Contains(newDoc, "SLOW_UPDATE_START") {
		t.Errorf("marker survived in content: %q", newDoc)
	}
	// Exactly one learned block: no duplication.
	if strings.Count(newDoc, LearnedStart) != 1 || strings.Count(newDoc, LearnedEnd) != 1 {
		t.Errorf("block duplicated: %q", newDoc)
	}
}

func TestApplyEdits_AnchorsNeverTouchHandContent(t *testing.T) {
	// An anchor matching only hand-written prose matches no learned line.
	newDoc, applied, unmatched := ApplyEdits(sleepTestDoc, []TextEdit{
		{Op: "delete", Anchor: "hand-written guidance"},
		{Op: "replace", Anchor: "never change", Content: "CHANGED"},
	})
	if len(applied) != 0 || len(unmatched) != 2 {
		t.Fatalf("applied=%d unmatched=%d, want 0/2", len(applied), len(unmatched))
	}
	if strings.Contains(newDoc, "CHANGED") || newDoc != sleepTestDoc {
		t.Error("hand-written content must be unreachable by anchors")
	}
}

func TestSelectGateScore(t *testing.T) {
	if got := SelectGateScore(1, 0, "hard", 0.5); got != 1 {
		t.Errorf("hard = %v", got)
	}
	if got := SelectGateScore(1, 0, "soft", 0.5); got != 0 {
		t.Errorf("soft = %v", got)
	}
	if got := SelectGateScore(1, 0.5, "mixed", 0.5); got != 0.75 {
		t.Errorf("mixed = %v", got)
	}
	if got := SelectGateScore(1, 0, "bogus", 0.5); got != 1 {
		t.Errorf("unknown metric must fail closed to hard, got %v", got)
	}
}

func TestEvaluateGate(t *testing.T) {
	cases := []struct {
		name            string
		cand, cur, best float64
		opts            GateOpts
		action          string
		accepted        bool
	}{
		{"strict new best", 0.8, 0.5, 0.6, GateOpts{}, "accept_new_best", true},
		{"strict accept", 0.8, 0.5, 0.9, GateOpts{}, "accept", true},
		{"tie rejects", 0.5, 0.5, 0.5, GateOpts{}, "reject", false},
		{"drop rejects", 0.4, 0.5, 0.5, GateOpts{}, "reject", false},
		{"leaked abstains", 1.0, 0.0, 0.0, GateOpts{HoldoutLeaked: true}, "reject_unverified", false},
		{"no-regression blocks", 0.9, 0.5, 0.5, GateOpts{Regressed: []string{"h2"}}, "reject", false},
		{"greedy ships", 0.0, 0.0, 0.0, GateOpts{Greedy: true, Applied: true}, "greedy_applied", true},
		{"greedy noop", 0.0, 0.0, 0.0, GateOpts{Greedy: true}, "greedy_noop", false},
	}
	for _, c := range cases {
		got := EvaluateGate(c.cand, c.cur, c.best, c.opts)
		if got.Action != c.action || got.Accepted != c.accepted {
			t.Errorf("%s: got (%s,%v) want (%s,%v): %s",
				c.name, got.Action, got.Accepted, c.action, c.accepted, got.Reason)
		}
	}
}

func TestSplitSkillDoc(t *testing.T) {
	front, rest, ok := SplitSkillDoc(sleepTestDoc)
	if !ok {
		t.Fatal("split failed")
	}
	if !strings.Contains(front, "name: demo") || !strings.Contains(rest, "# Demo") {
		t.Errorf("bad split: %q / %q", front, rest)
	}
	if _, _, ok := SplitSkillDoc("no frontmatter"); ok {
		t.Error("must reject docs without frontmatter")
	}
}
