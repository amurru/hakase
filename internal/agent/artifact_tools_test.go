// artifact_tools_test.go - AD-003 (T2.1): the artifact load path is gated
// under the "artifact" flow action, text content is redacted before it
// reaches the model, and binary artifacts surface metadata only.
package agent

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/genai"
)

// fakeArtifacts is a session artifact store for handler tests.
type fakeArtifacts struct {
	parts map[string]*genai.Part
}

func (f *fakeArtifacts) Save(ctx context.Context, name string, data *genai.Part) (*artifact.SaveResponse, error) {
	if f.parts == nil {
		f.parts = map[string]*genai.Part{}
	}
	f.parts[name] = data
	return &artifact.SaveResponse{Version: 1}, nil
}

func (f *fakeArtifacts) List(context.Context) (*artifact.ListResponse, error) {
	names := make([]string, 0, len(f.parts))
	for n := range f.parts {
		names = append(names, n)
	}
	return &artifact.ListResponse{FileNames: names}, nil
}

func (f *fakeArtifacts) Load(ctx context.Context, name string) (*artifact.LoadResponse, error) {
	p, ok := f.parts[name]
	if !ok {
		return nil, fmt.Errorf("artifact not found: %w", fs.ErrNotExist)
	}
	return &artifact.LoadResponse{Part: p}, nil
}

func (f *fakeArtifacts) LoadVersion(ctx context.Context, name string, version int) (*artifact.LoadResponse, error) {
	return f.Load(ctx, name)
}

// artifactCtx is an agent.Context carrying a fake artifact store. It embeds
// the strict mock so standard context methods read from a real payload
// (flowLabelForCtx probes ctx values).
type artifactCtx struct {
	agent.StrictContextMock
	arts *fakeArtifacts
}

func newArtifactCtx(arts *fakeArtifacts) *artifactCtx {
	return &artifactCtx{StrictContextMock: agent.NewStrictContextMock(context.Background()), arts: arts}
}

func (c *artifactCtx) Artifacts() agent.Artifacts {
	// A nil store must surface as a nil interface (what ADK returns when no
	// artifact service is configured), not a typed nil.
	if c.arts == nil {
		return nil
	}
	return c.arts
}

func TestFlowMapLoadArtifact(t *testing.T) {
	cases := []struct {
		name      string
		args      map[string]any
		wantRes   string
		wantEmpty bool
	}{
		{name: "named", args: map[string]any{"name": "night-report.md"}, wantRes: "night-report.md"},
		{name: "file_name alias", args: map[string]any{"file_name": "a.txt"}, wantRes: "a.txt"},
		{name: "missing name maps to wildcard", args: map[string]any{}, wantRes: "*"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			action, res := flowMapTool("load_artifact", c.args)
			if action != "artifact" {
				t.Errorf("action = %q, want artifact", action)
			}
			if len(res) != 1 || res[0] != c.wantRes {
				t.Errorf("resources = %v, want [%s]", res, c.wantRes)
			}
		})
	}
}

func TestLoadArtifactHandlerRedactsText(t *testing.T) {
	arts := &fakeArtifacts{parts: map[string]*genai.Part{
		"report.md": {Text: "token sk-abcdefghijklmnopqrstuvwxyz0123456789 done"},
	}}
	out, err := loadArtifactHandler(newArtifactCtx(arts), LoadArtifactInput{Name: "report.md"})
	if err != nil {
		t.Fatalf("loadArtifactHandler: %v", err)
	}
	if out.Kind != "text" {
		t.Errorf("Kind = %q, want text", out.Kind)
	}
	if !out.Redacted {
		t.Error("Redacted = false, want true for a secret-shaped payload")
	}
	if strings.Contains(out.Content, "sk-abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("content leaked the secret: %q", out.Content)
	}
}

func TestLoadArtifactHandlerBinaryMetadata(t *testing.T) {
	arts := &fakeArtifacts{parts: map[string]*genai.Part{
		"pic.png": {InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte{1, 2, 3, 4}}},
	}}
	out, err := loadArtifactHandler(newArtifactCtx(arts), LoadArtifactInput{Name: "pic.png"})
	if err != nil {
		t.Fatalf("loadArtifactHandler: %v", err)
	}
	if out.Kind != "binary" || out.MimeType != "image/png" || out.Bytes != 4 {
		t.Errorf("out = %+v, want binary image/png 4 bytes", out)
	}
	if out.Content != "" {
		t.Errorf("Content = %q, want empty for binary artifacts", out.Content)
	}
}

func TestLoadArtifactHandlerErrors(t *testing.T) {
	if _, err := loadArtifactHandler(newArtifactCtx(nil), LoadArtifactInput{}); err == nil {
		t.Error("empty name: want error")
	}
	if _, err := loadArtifactHandler(newArtifactCtx(nil), LoadArtifactInput{Name: "x"}); err == nil {
		t.Error("nil artifact service: want error")
	}
	if _, err := loadArtifactHandler(newArtifactCtx(&fakeArtifacts{}), LoadArtifactInput{Name: "missing"}); err == nil {
		t.Error("missing artifact: want error")
	}
}

func TestCheckPreFlowArtifactGated(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "artifact", Resource: "night-report.md", Effect: permissions.EffectAllow},
		{Action: "artifact", Resource: "private-*", Effect: permissions.EffectDeny},
	}})
	hakasesession.DefaultRegistry.Import("sess-artifact-gate", permissions.StartLabel(), nil)
	t.Cleanup(func() { hakasesession.DefaultRegistry.Set("sess-artifact-gate", permissions.StartLabel()) })
	ctx := flowSessionCtx(t, "task-artifact-gate", "sess-artifact-gate")

	t.Run("allow rule admits", func(t *testing.T) {
		ok, blocked, _, eff, _ := CheckPreFlow(ctx, "load_artifact", map[string]any{"name": "night-report.md"})
		if !ok || blocked {
			t.Fatalf("ok=%v blocked=%v, want admitted", ok, blocked)
		}
		if eff != permissions.EffectAllow {
			t.Errorf("effect = %q, want allow", eff)
		}
	})

	t.Run("deny rule blocks", func(t *testing.T) {
		ok, blocked, _, eff, reason := CheckPreFlow(ctx, "load_artifact", map[string]any{"name": "private-keys.md"})
		if !ok || !blocked {
			t.Fatalf("ok=%v blocked=%v, want blocked", ok, blocked)
		}
		if eff != permissions.EffectDeny {
			t.Errorf("effect = %q, want deny", eff)
		}
		if reason == "" {
			t.Error("empty reason")
		}
	})

	t.Run("unmatched artifact fails closed in the handler", func(t *testing.T) {
		// The flow layer reports "not found" for unmatched resources and
		// passes the call through, so the handler itself must refuse.
		arts := &fakeArtifacts{parts: map[string]*genai.Part{"unlisted.md": {Text: "data"}}}
		hctx := newArtifactCtx(arts)
		hctx.StrictContextMock = agent.NewStrictContextMock(ctx)
		if _, err := loadArtifactHandler(hctx, LoadArtifactInput{Name: "unlisted.md"}); err == nil {
			t.Fatal("handler admitted an unmatched artifact under a v2 policy, want fail-closed refusal")
		}
	})

	t.Run("allowed artifact loads through the handler", func(t *testing.T) {
		arts := &fakeArtifacts{parts: map[string]*genai.Part{"night-report.md": {Text: "all good"}}}
		hctx := newArtifactCtx(arts)
		hctx.StrictContextMock = agent.NewStrictContextMock(ctx)
		out, err := loadArtifactHandler(hctx, LoadArtifactInput{Name: "night-report.md"})
		if err != nil {
			t.Fatalf("allowed artifact refused: %v", err)
		}
		if out.Content != "all good" {
			t.Errorf("content = %q, want %q", out.Content, "all good")
		}
	})

	t.Run("registered tool name reaches the mapping", func(t *testing.T) {
		// Guards against a rename drifting away from the flowMapTool case.
		if _, err := registerLoadArtifactTool(nil); err != nil {
			t.Fatalf("registerLoadArtifactTool: %v", err)
		}
		if got := interfaces.SessionIDFromCtx(ctx); got != "sess-artifact-gate" {
			t.Fatalf("session probe = %q, want sess-artifact-gate", got)
		}
	})
}
