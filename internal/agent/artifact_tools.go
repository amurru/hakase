package agent

import (
	"fmt"
	"strings"

	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// LoadArtifactInput is the input schema for the load_artifact tool.
type LoadArtifactInput struct {
	Name string `json:"name" doc:"Artifact file name to load, as listed in the session's artifact list."`
}

// LoadArtifactOutput is the model-facing view of one artifact. Text
// artifacts are returned inline (redacted); binary artifacts return their
// metadata plus the workspace path so the model can hand it to a
// path-based tool (e.g. the vision tool) instead of receiving raw bytes
// through a JSON function result.
type LoadArtifactOutput struct {
	Name     string `json:"name"`
	Kind     string `json:"kind" doc:"text or binary"`
	Content  string `json:"content,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Bytes    int    `json:"bytes,omitempty"`
	Redacted bool   `json:"redacted,omitempty"`
}

// loadArtifactHandler returns one session artifact's content. Access control
// is the orchestrator's pre-dispatch callback (flowMapTool maps load_artifact
// to the "artifact" action; CheckPreFlowAsk enforces allow/ask/deny and
// audits). The handler then re-checks the ruling for defense in depth and
// applies the fail-closed default the flow layer does not provide: under a
// v2 policy, an artifact with no matching rule is refused rather than
// passed through (LookupWithLabel reports "not found" for unmatched
// resources). An "ask" ruling is not re-escalated here - the pre-dispatch
// gate already resolved it (approval or denial) before the tool ran.
//
// ADK's own loadartifactstool is deliberately NOT used: it injects artifact
// content from ProcessRequest before any tool callback can gate that
// content (docs/adk-adoption/spec.md AD-003 landing notes).
func loadArtifactHandler(ctx agent.Context, input LoadArtifactInput) (LoadArtifactOutput, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return LoadArtifactOutput{}, fmt.Errorf("artifact name is required")
	}
	if permissions.FlowEnabled() {
		eff, rule, detail, found := permissions.LookupWithLabel(flowLabelForCtx(ctx), "artifact", name)
		if !found {
			return LoadArtifactOutput{}, fmt.Errorf("artifact %q refused: no artifact rule matches under the active flow policy (fail-closed)", name)
		}
		if eff == permissions.EffectDeny {
			return LoadArtifactOutput{}, fmt.Errorf("artifact %q denied by policy", name)
		}
		// A rule-backed ask was routed to the human by the pre-dispatch
		// gate (and, if approved, may proceed). A bare policy-default ask
		// has no other approval path for this tool - nothing downstream
		// prompts - so refusing here is what makes the default
		// fail-closed instead of silently permissive.
		if eff == permissions.EffectAsk && rule == nil && detail == "" {
			return LoadArtifactOutput{}, fmt.Errorf("artifact %q refused: no artifact rule under the active flow policy and no approval path for this tool (fail-closed)", name)
		}
	}
	svc := ctx.Artifacts()
	if svc == nil {
		return LoadArtifactOutput{}, fmt.Errorf("artifact service is not configured for this session")
	}
	resp, err := svc.Load(ctx, name)
	if err != nil {
		return LoadArtifactOutput{}, fmt.Errorf("load artifact %q: %w", name, err)
	}
	if resp == nil || resp.Part == nil {
		return LoadArtifactOutput{}, fmt.Errorf("artifact %q is empty", name)
	}

	out := LoadArtifactOutput{Name: name}
	part := resp.Part
	switch {
	case part.InlineData != nil:
		out.Kind = "binary"
		out.MimeType = part.InlineData.MIMEType
		out.Bytes = len(part.InlineData.Data)
		return out, nil
	default:
		out.Kind = "text"
		redacted, didRedact := util.RedactSecrets(part.Text)
		out.Content = redacted
		out.Redacted = didRedact
		return out, nil
	}
}

// registerLoadArtifactTool registers the gated load_artifact tool on the
// orchestrator.
func registerLoadArtifactTool(log LogFunc) (tool.Tool, error) {
	return util.NewDocTool(functiontool.Config{
		Name:        "load_artifact",
		Description: "Load one of the session's artifacts by name. Text artifacts are returned redacted; binary artifacts return metadata plus the workspace path for path-based tools.",
	}, loadArtifactHandler)
}
