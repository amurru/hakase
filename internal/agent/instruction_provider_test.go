// instruction_provider_test.go - pins the ADK state-templating hazard
// behind the StaticInstruction migration.
//
// ADK substitutes every {identifier} in an agent's Instruction string
// from session state and aborts the run with ErrStateKeyNotExist when
// the key is missing. Dynamic instruction content (delegation context,
// skill bodies, job names) legitimately contains braces, so production
// agents are served via util.StaticInstruction (InstructionProvider),
// which skips substitution. These tests run both variants through a
// real runner with a scripted model: the control documents the crash,
// the provider case pins the fix.
package agent

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"

	"amurru/hakase/internal/util"
)

// runProbeText runs one turn against agt and returns the streamed text
// or the first run error.
func runProbeText(runner *runner.Runner, sessionID string) (string, error) {
	var sb strings.Builder
	for ev, err := range runner.Run(context.Background(), "user-1", sessionID,
		genai.NewContentFromText("go", genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			return "", err
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p != nil && p.Text != "" && !p.Thought {
				sb.WriteString(p.Text)
			}
		}
	}
	return sb.String(), nil
}

// probeRunner builds a runner over one no-tool agent with the given
// instruction wiring and a scripted model answering "probe-ok".
func probeRunner(t *testing.T, cfg llmagent.Config) *runner.Runner {
	t.Helper()
	cfg.Name = "probe"
	cfg.Model = &scriptedModel{scripts: [][]*model.LLMResponse{
		{spikeTextResponse("probe-ok")},
	}}
	agt, err := llmagent.New(cfg)
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}
	r, err := runner.New(runner.Config{
		AppName:           "probe",
		Agent:             agt,
		SessionService:    adksession.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	return r
}

// TestInstructionStringWithBracesAbortsRun is the control: a brace
// placeholder in a plain Instruction string aborts the run, which is
// the production crash this migration fixes.
func TestInstructionStringWithBracesAbortsRun(t *testing.T) {
	r := probeRunner(t, llmagent.Config{
		Instruction: "You are a probe. Follow {definitely_missing_state_key} literally.",
	})
	_, err := runProbeText(r, "sess-control")
	if err == nil {
		t.Fatal("plain Instruction with {placeholder}: want state-key error, got success")
	}
	if !strings.Contains(err.Error(), "state key does not exist") {
		t.Fatalf("error = %q, want it to mention the missing state key", err)
	}
}

// TestInstructionProviderServesBracesVerbatim pins the fix: the same
// brace-laden content served via util.StaticInstruction runs cleanly
// and the model answer streams through.
func TestInstructionProviderServesBracesVerbatim(t *testing.T) {
	r := probeRunner(t, llmagent.Config{
		InstructionProvider: util.StaticInstruction(
			"You are a probe. Follow {definitely_missing_state_key} and {query} literally."),
	})
	out, err := runProbeText(r, "sess-provider")
	if err != nil {
		t.Fatalf("provider-served braces: want success, got %v", err)
	}
	if !strings.Contains(out, "probe-ok") {
		t.Fatalf("turn output = %q, want probe-ok", out)
	}
}
