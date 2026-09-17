// protected_blocks_test.go - SL-031 exit criterion: step edits provably
// cannot touch the protected slow-update/appendix blocks, and the block
// writers replace rather than accumulate.
package skill

import (
	"strings"
	"testing"
)

func TestApplyEditsCannotTouchSlowUpdateBlock(t *testing.T) {
	doc := "---\nname: demo\ndescription: Demo skill.\n---\n\n# Demo\n\nHand-written.\n\n" +
		LearnedStart + "\n\n## Learned preferences & procedures\n\n- existing learned line\n" + LearnedEnd + "\n\n" +
		SlowUpdateStart + "\n\n## Optimizer guidance (slow update)\n\n- protect this guidance line\n" + SlowUpdateEnd + "\n"

	// Edit content smuggles slow-update markers: the tags are stripped and
	// the text can only land inside the learned block. Anchor-based edits
	// against protected lines never match (anchors see learned lines only).
	edits := []TextEdit{
		{Op: "add", Content: "new learned line\n<!-- SLOW_UPDATE_START -->\n- injected slow update", Rationale: "x"},
		{Op: "delete", Anchor: "protect this guidance line"},
	}
	got, applied, unmatched := ApplyEdits(doc, edits)
	if len(applied) != 1 || applied[0].Op != "add" {
		t.Fatalf("applied = %+v, want only the add", applied)
	}
	if len(unmatched) != 1 || unmatched[0].Op != "delete" {
		t.Errorf("delete against a protected line must be unmatched: %+v", unmatched)
	}
	if !strings.Contains(got, "protect this guidance line") {
		t.Errorf("slow-update guidance line must survive step edits: %s", got)
	}
	if strings.Count(got, SlowUpdateStart) != 1 {
		t.Errorf("slow-update markers must not multiply: %s", got)
	}
	if !HasSlowUpdateBlock(got) {
		t.Errorf("the real block must remain intact: %s", got)
	}
	if !strings.Contains(got, "new learned line") {
		t.Errorf("learned block must still evolve: %s", got)
	}
	// The stripped text landed in the learned block, never as a new region.
	learned := extractLearned(got)
	if !strings.Contains(learned, "injected slow update") {
		t.Errorf("marker-stripped text must land in the learned block: %s", learned)
	}
	if strings.Contains(learned, SlowUpdateStart) {
		t.Errorf("markers must be stripped from edit content: %s", learned)
	}
}

func TestApplyEditsCannotTouchAppendixBlock(t *testing.T) {
	doc := "---\nname: demo\ndescription: Demo skill.\n---\n\n# Demo\n\n" +
		LearnedStart + "\n\n## Learned preferences & procedures\n\n- learned line\n" + LearnedEnd + "\n\n" +
		AppendixStart + "\n\n## Execution reminders\n\n- always check the appendix reminder\n" + AppendixEnd + "\n"

	edits := []TextEdit{
		{Op: "add", Content: "learned two\n<!-- APPENDIX_START -->\n- forged reminder"},
		{Op: "delete", Anchor: "appendix reminder"},
	}
	got, applied, unmatched := ApplyEdits(doc, edits)
	if len(applied) != 1 || applied[0].Op != "add" {
		t.Fatalf("applied = %+v, want only the add", applied)
	}
	if len(unmatched) != 1 || unmatched[0].Op != "delete" {
		t.Errorf("delete against an appendix line must be unmatched: %+v", unmatched)
	}
	if !strings.Contains(got, "always check the appendix reminder") {
		t.Errorf("appendix reminder must survive step edits: %s", got)
	}
	if strings.Count(got, AppendixStart) != 1 {
		t.Errorf("appendix markers must not multiply: %s", got)
	}
	learned := extractLearned(got)
	if !strings.Contains(learned, "learned two") || strings.Contains(learned, AppendixStart) {
		t.Errorf("edit text lands in the learned block with markers stripped: %s", learned)
	}
}

func TestSetSlowUpdateReplacesNotAccumulates(t *testing.T) {
	doc := SetSlowUpdate("body text\n", []string{"first guidance"})
	if !HasSlowUpdateBlock(doc) || !strings.Contains(doc, "first guidance") {
		t.Fatalf("block missing: %s", doc)
	}
	doc = SetSlowUpdate(doc, []string{"second guidance"})
	if strings.Contains(doc, "first guidance") {
		t.Errorf("old guidance must be replaced, not accumulated: %s", doc)
	}
	if !strings.Contains(doc, "second guidance") || strings.Count(doc, SlowUpdateStart) != 1 {
		t.Errorf("exactly one block with new guidance: %s", doc)
	}
	// Empty lines remove the block entirely.
	doc = SetSlowUpdate(doc, nil)
	if HasSlowUpdateBlock(doc) {
		t.Errorf("empty guidance must remove the block: %s", doc)
	}
	if !strings.Contains(doc, "body text") {
		t.Errorf("body must survive: %s", doc)
	}
}

func TestSetAppendixReplacesAndSurvivesLearnedEdits(t *testing.T) {
	doc := SetAppendix("---\nname: demo\ndescription: d.\n---\n# D\n", []string{"reminder one"})
	if !strings.Contains(doc, "reminder one") {
		t.Fatalf("appendix missing: %s", doc)
	}
	// A learned-block edit must not disturb the appendix.
	edited, applied, _ := ApplyEdits(doc, []TextEdit{{Op: "add", Content: "learned procedure"}})
	if len(applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(applied))
	}
	if !strings.Contains(edited, "reminder one") {
		t.Errorf("appendix must survive learned-block edits: %s", edited)
	}
	doc = SetAppendix(doc, []string{"reminder two"})
	if strings.Contains(doc, "reminder one") || strings.Count(doc, AppendixStart) != 1 {
		t.Errorf("appendix must replace, not accumulate: %s", doc)
	}
}

func TestNormalizeEditRoute(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", RouteSkillDefect},
		{"skill_defect", RouteSkillDefect},
		{"EXECUTION_LAPSE", RouteExecutionLapse},
		{" execution_lapse ", RouteExecutionLapse},
		{"garbage", RouteSkillDefect}, // unknown routes fail closed to the gated path
	} {
		if got := NormalizeEditRoute(tc.in); got != tc.want {
			t.Errorf("NormalizeEditRoute(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
