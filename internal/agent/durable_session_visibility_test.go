package agent

// Regression test for issue #110: the ADK v2.4.0 node runtime builds each
// model request's contents from the session object the runner holds, so
// events appended during an invocation (the user message, function calls,
// function responses) must be visible to later model calls of the same
// invocation. Only the durable service was affected; the engine's
// in-memory service updates the passed session.

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"

	hakasesession "amurru/hakase/internal/session"
)

// describeTestContents renders the request contents as kind tags so a test
// can assert which events the model would see.
func describeTestContents(contents []*genai.Content) string {
	var parts []string
	for _, c := range contents {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			switch {
			case p == nil:
			case p.FunctionResponse != nil:
				parts = append(parts, "FR:"+p.FunctionResponse.Name)
			case p.FunctionCall != nil:
				parts = append(parts, "FC:"+p.FunctionCall.Name)
			case p.Text != "":
				parts = append(parts, "T:"+p.Text)
			case p.InlineData != nil:
				parts = append(parts, "IMG")
			}
		}
	}
	return strings.Join(parts, "|")
}

func TestDurableSessionInFlightEventsVisibleToModel(t *testing.T) {
	svc, err := hakasesession.NewDurableADKService(t.TempDir())
	if err != nil {
		t.Fatalf("NewDurableADKService: %v", err)
	}

	var shapes []string
	m := &scriptedModel{scripts: [][]*model.LLMResponse{
		{spikeCallResponse("ask_thing", "c1", map[string]any{"q": "hi"})},
		{spikeTextResponse("loop-done")},
	}}
	tl, err := functiontool.New(functiontool.Config{Name: "ask_thing", Description: "test"}, func(_ agent.Context, in spikeArgs) (spikeOut, error) {
		return spikeOut{A: "answer:" + in.Q}, nil
	})
	if err != nil {
		t.Fatalf("functiontool.New: %v", err)
	}
	agt, err := llmagent.New(llmagent.Config{
		Name:  "orchestrator",
		Model: m,
		Tools: []tool.Tool{tl},
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{
			func(_ agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
				shapes = append(shapes, describeTestContents(req.Contents))
				return nil, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}
	r, err := runner.New(runner.Config{
		AppName: "spike", Agent: agt, SessionService: svc, AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}

	for ev, runErr := range r.Run(context.Background(), "user-1", "sess-vis", genai.NewContentFromText("go", genai.RoleUser), agent.RunConfig{}) {
		if runErr != nil {
			t.Fatalf("Run: %v", runErr)
		}
		_ = ev
	}

	if len(shapes) != 2 {
		t.Fatalf("model calls = %d, want 2 (%v)", len(shapes), shapes)
	}
	if !strings.Contains(shapes[0], "T:go") {
		t.Fatalf("first call missing the user message: %q", shapes[0])
	}
	if !strings.Contains(shapes[1], "FC:ask_thing") || !strings.Contains(shapes[1], "FR:ask_thing") {
		t.Fatalf("second call missing the tool transcript: %q", shapes[1])
	}
}
