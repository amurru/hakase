// flowbudget_test.go - H3 over-budget preview redaction (C-NEW).
//
// The over-budget preview snapshot predates the in-place redact, so the
// budget overrides must re-redact before admitting any string to the
// model. A secret in the truncated window must never survive it.
package agent

import (
	"fmt"
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
)

// TestFlowBudgetPreviewRedacted pins C-NEW on both over-budget legs: a
// secret in the first lines of an over-budget result is absent from
// truncated_preview (egress withhold and source admit alike), while the
// refine note still rides along.
func TestFlowBudgetPreviewRedacted(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "read", Resource: "*", Effect: permissions.EffectAllow},
		{Action: "send_message", Resource: "*", Effect: permissions.EffectAllow},
	}})
	bulk := make([]string, 0, 600)
	bulk = append(bulk, "leak "+flowHighsSecret, "also leak "+flowHighsSecret)
	for i := 0; i < 600; i++ {
		bulk = append(bulk, fmt.Sprintf("benign line %d with padding to look like a large read result", i))
	}

	// Egress leg: withhold carries a redacted truncated_preview.
	sess := "flow-budgetredact-egress"
	ctx := flowSessionCtx(t, "task-budgetredact-egress", sess)
	hakasesession.DefaultRegistry.Fold(sess, permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")
	tl := stubTool(t, "send_message")
	override, ferr := CheckPostFlow(ctx, tl, map[string]any{"channel": "#g", "body": "hi"}, map[string]any{"bulk": bulk}, nil)
	if ferr != nil {
		t.Fatalf("err = %v", ferr)
	}
	if override == nil || override["withheld"] != true {
		t.Fatalf("over-budget egress must withhold, got %v", override)
	}
	assertPreviewRedacted(t, override)

	// Source leg: admit carries a redacted truncated_preview.
	sess2 := "flow-budgetredact-source"
	ctx2 := flowSessionCtx(t, "task-budgetredact-source", sess2)
	hakasesession.DefaultRegistry.Fold(sess2, permissions.FlowDelta{Trust: "suspicious"}, "k", "h")
	tl2 := stubTool(t, "read_file")
	override2, ferr2 := CheckPostFlow(ctx2, tl2, map[string]any{"path": "docs/a.md"}, map[string]any{"bulk": bulk}, nil)
	if ferr2 != nil {
		t.Fatalf("err = %v", ferr2)
	}
	if override2 == nil || override2["truncated"] != true {
		t.Fatalf("over-budget source must admit truncated subset, got %v", override2)
	}
	assertPreviewRedacted(t, override2)
}

// assertPreviewRedacted checks an over-budget override carries a refine
// note and a truncated_preview free of the secret.
func assertPreviewRedacted(t *testing.T, override map[string]any) {
	t.Helper()
	if _, ok := override["refine_note"]; !ok {
		t.Fatalf("over-budget override must carry a refine note, got %v", override)
	}
	raw, ok := override["truncated_preview"]
	if !ok {
		t.Fatalf("over-budget override must carry truncated_preview, got %v", override)
	}
	items, ok := raw.([]string)
	if !ok {
		t.Fatalf("truncated_preview = %T, want []string", raw)
	}
	if len(items) == 0 || len(items) > 5 {
		t.Fatalf("truncated_preview has %d strings, want 1..5", len(items))
	}
	for _, s := range items {
		if strings.Contains(s, flowHighsSecret) || strings.Contains(s, "supersecret123456") {
			t.Fatalf("secret survives in truncated_preview: %q", s)
		}
	}
	if joined := strings.Join(items, "\n"); !strings.Contains(joined, "REDACTED") {
		t.Fatalf("truncated_preview carries no redaction token: %q", joined)
	}
}
