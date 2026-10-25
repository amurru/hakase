// flowbudget.go - H3 over-budget truncated subset + refine note.
package agent

import (
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/util"
)

// redactPreview re-runs secret redaction over an over-budget preview.
// Callers pass the PRE-redaction snapshot from marshalResultStringsCapped
// (collected before redactResultInPlaceCapped ran), so the preview must be
// re-redacted here - defense in depth, never trust the call-site order.
// Returns a fresh slice; the input is never mutated.
func redactPreview(preview []string) []string {
	out := make([]string, 0, len(preview))
	for _, s := range preview {
		red, _ := util.RedactSecrets(s)
		out = append(out, red)
	}
	return out
}

// truncatePreview caps a (post-redaction) preview to the first 5 strings
// for the model-legible override.
func truncatePreview(preview []string) []string {
	if len(preview) > 5 {
		preview = preview[:5]
	}
	return preview
}

// flowBudgetOverride builds the over-budget egress override (H3): the full
// result is withheld, but the truncated subset (first strings, re-redacted
// here because the snapshot predates the in-place redact) rides along with
// a refine note so the model narrows the query instead of retrying blindly.
func flowBudgetOverride(toolName, action string, label *permissions.SessionLabel, preview []string) map[string]any {
	base := flowWithholdOverride(toolName, action, label)
	base["truncated_preview"] = truncatePreview(redactPreview(preview))
	base["truncated_count"] = len(preview)
	base["refine_note"] = "result exceeded the flow scan budget (512 strings / 1MB); showing first strings only - refine your query"
	if r, ok := base["reason"].(string); ok {
		base["reason"] = r + " (refine your query: result truncated to scan budget)"
	}
	return base
}

// flowBudgetAdmit admits the truncated source subset (H3): the re-redacted
// preview carries only the first strings, and the refine note tells the
// model the remainder was cut for budget.
func flowBudgetAdmit(preview []string) map[string]any {
	subset := truncatePreview(redactPreview(preview))
	return map[string]any{
		"truncated":         true,
		"truncated_count":   len(preview),
		"truncated_preview": subset,
		"refine_note":       "result exceeded the flow scan budget (512 strings / 1MB); showing first strings only - refine your query",
	}
}
