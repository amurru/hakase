package agent

import (
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/util"
	"fmt"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// ClarifyRequest describes a mid-task question for the user.
type ClarifyRequest struct {
	Question    string   // question text
	Choices     []string // optional answer choices (max 4, per Hermes)
	MultiSelect bool     // allow multiple choices (only with Choices)
}

// ClarifyResponse is the user's answer, plus cancel/timeout signals.
type ClarifyResponse struct {
	Answer   []string // selected choice(s) or free-text answer (1 element)
	Canceled bool     // user pressed Esc
	TimedOut bool     // no answer within the expiry window
}

// ClarifyInput is the schema for the clarify tool as seen by the model.
// doc tags are injected into the inferred JSON schema by util.NewDocTool.
type ClarifyInput struct {
	Question    string   `json:"question" doc:"The question to ask the user (required)."`
	Choices     []string `json:"choices,omitempty" doc:"Optional predefined answer choices (up to 4). Omit for an open-ended question."`
	MultiSelect bool     `json:"multi_select,omitempty" doc:"When true, the user may select multiple choices. Only applies when choices are provided."`
}

// ClarifyOutput is the tool result returned to the model.
type ClarifyOutput struct {
	Question       string   `json:"question" doc:"The question that was asked."`
	ChoicesOffered []string `json:"choices_offered,omitempty" doc:"The choices that were offered, if any."`
	UserResponse   []string `json:"user_response" doc:"The user's answer. A single element for free text or a single choice; multiple elements when multi_select is used. Empty when the question was canceled or timed out."`
	Canceled       bool     `json:"canceled,omitempty" doc:"True when the user dismissed the question (Esc)."`
	TimedOut       bool     `json:"timed_out,omitempty" doc:"True when no answer arrived before the expiry timeout."`
}

// clarifyTimeout returns the configured clarify expiry duration from deps.
// Defaults to 120 seconds when not configured (0/negative).
func clarifyTimeout() time.Duration {
	if deps == nil || deps.ClarifyCfg.ExpirySeconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(deps.ClarifyCfg.ExpirySeconds) * time.Second
}

// askClarify wraps the interactive clarify gate. When the gate is nil
// (headless mode / not yet wired), fails closed.
func askClarify(req ClarifyRequest) (ClarifyResponse, error) {
	if rt == nil {
		return ClarifyResponse{}, fmt.Errorf("no clarify mechanism available (headless mode)")
	}
	g := rt.ClarifyGate()
	if g == nil {
		return ClarifyResponse{}, fmt.Errorf("no clarify mechanism available (headless mode)")
	}
	ifaceResp, err := g.AskClarify(interfaces.ClarifyRequest{
		Question:    req.Question,
		Choices:     req.Choices,
		MultiSelect: req.MultiSelect,
	})
	if err != nil {
		return ClarifyResponse{}, err
	}
	return ClarifyResponse{
		Answer:   ifaceResp.Answer,
		Canceled: ifaceResp.Canceled,
		TimedOut: ifaceResp.TimedOut,
	}, nil
}

// registerClarifyTool creates the clarify tool for the orchestrator.
func registerClarifyTool() (tool.Tool, error) {
	return util.NewDocTool(functiontool.Config{
		Name:        "clarify",
		Description: "Ask the user a question mid-task when you need input you cannot infer. Pass up to 4 answer options in 'choices', omit for an open-ended question.",
	}, func(ctx agent.Context, input ClarifyInput) (ClarifyOutput, error) {
		resp, err := askClarify(ClarifyRequest{
			Question:    input.Question,
			Choices:     input.Choices,
			MultiSelect: input.MultiSelect,
		})
		if err != nil {
			return ClarifyOutput{}, err
		}
		return ClarifyOutput{
			Question:       input.Question,
			ChoicesOffered: input.Choices,
			UserResponse:   resp.Answer,
			Canceled:       resp.Canceled,
			TimedOut:       resp.TimedOut,
		}, nil
	})
}
