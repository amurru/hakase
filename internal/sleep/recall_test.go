// recall_test.go - SL-033 acceptance: keyword reduction over intents and
// BM25 recall over a real knowledge directory (reuses knowledge/score.go).
package sleep

import (
	"strings"
	"testing"

	"amurru/hakase/internal/knowledge"
)

func TestRecallQueries(t *testing.T) {
	queries := recallQueries([]string{
		"Deploy the kubernetes service again, it failed with CrashLoopBackOff!",
		"Fix the docker build cache",
	})
	if len(queries) == 0 || len(queries) > MaxRecallQueries {
		t.Fatalf("queries = %v", queries)
	}
	for _, q := range queries {
		if len(q) < MinRecallTokenLen {
			t.Errorf("short token leaked: %q", q)
		}
		if strings.Contains(q, "!") || strings.Contains(q, ",") {
			t.Errorf("punctuation leaked into query: %q", q)
		}
	}
	// Deduplication: same intent twice must not double the queries.
	again := recallQueries([]string{"deploy the service", "deploy the service"})
	if len(again) != len(recallQueries([]string{"deploy the service"})) {
		t.Errorf("duplicate intents must dedup: %v", again)
	}
}

func TestRecallNotesOverKnowledgeDir(t *testing.T) {
	dir := t.TempDir()
	note := &knowledge.KnowledgeNote{
		Slug: "kubernetes-crashloops",
		Frontmatter: knowledge.KnowledgeFrontmatter{
			Title:   "Kubernetes CrashLoopBackOff playbook",
			Tags:    []string{"lessons-learned", "kubernetes"},
			Summary: "Check image pull secrets and probe timeouts when pods crashloop.",
		},
		Body: "When a kubernetes pod enters CrashLoopBackOff after a deploy, verify the image pull secrets first, then probe timeouts.",
	}
	note.Raw = string(knowledge.SerializeNote(note))
	if err := knowledge.SaveNote(dir, note); err != nil {
		t.Fatalf("save note: %v", err)
	}
	other := &knowledge.KnowledgeNote{
		Slug: "sourdough-timing",
		Frontmatter: knowledge.KnowledgeFrontmatter{
			Title: "Sourdough fermentation timing",
			Tags:  []string{"lessons-learned", "baking"},
		},
		Body: "Bulk fermentation in a cold kitchen takes twice as long as the recipe states.",
	}
	other.Raw = string(knowledge.SerializeNote(other))
	if err := knowledge.SaveNote(dir, other); err != nil {
		t.Fatalf("save note: %v", err)
	}

	notes := RecallNotes(dir, []string{"the kubernetes deploy crashes with crashloops in the cluster"}, 2)
	if len(notes) == 0 {
		t.Fatal("expected at least one recalled note")
	}
	if notes[0].Title != note.Frontmatter.Title {
		t.Errorf("top note = %q, want the kubernetes playbook", notes[0].Title)
	}

	// Top-k respected.
	if got := RecallNotes(dir, []string{"kubernetes", "sourdough", "fermentation", "probe", "image", "deploy"}, 1); len(got) != 1 {
		t.Errorf("k=1 returned %d notes", len(got))
	}
	// Empty knowledge dir or zero k recalls nothing without error.
	if got := RecallNotes(t.TempDir(), []string{"kubernetes"}, 3); got != nil {
		t.Errorf("empty dir must recall nothing, got %+v", got)
	}
	if got := RecallNotes(dir, []string{"kubernetes"}, 0); got != nil {
		t.Errorf("k=0 must recall nothing, got %+v", got)
	}
}

func TestRenderRecallBlock(t *testing.T) {
	notes := []RecalledNote{{Slug: "s", Title: "A playbook", Summary: "Do the thing."}}
	out := RenderRecallBlock(notes)
	for _, want := range []string{"Recalled lessons", "A playbook", "Do the thing."} {
		if !strings.Contains(out, want) {
			t.Errorf("recall block missing %q: %s", want, out)
		}
	}
	if RenderRecallBlock(nil) != "" {
		t.Error("no notes must render nothing")
	}
}
