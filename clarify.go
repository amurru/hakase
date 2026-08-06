package main

import (
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
// doc tags are injected into the inferred JSON schema by newDocTool.
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

// askClarify is the interactive clarify gate. The TUI installs it at startup
// (main.go). When nil (headless/CLI mode), clarify fails closed.
var askClarify func(req ClarifyRequest) (ClarifyResponse, error)

// currentClarify holds the runtime clarify configuration, set in setupRunner
// like currentApproval. Zero value is safe: defaults to 120s expiry.
var currentClarify ClarifyConfig

// clarifyTimeout returns the configured clarify expiry duration. Defaults to
// 120 seconds when not configured (0/negative). A question deserves longer than
// the 60s approval default.
func clarifyTimeout() time.Duration {
	if currentClarify.ExpirySeconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(currentClarify.ExpirySeconds) * time.Second
}

// clarifyExec is a small wrapper: nil askClarify -> error; otherwise delegates
// to the installed askClarify function. Always fails closed.
func clarifyExec(req ClarifyRequest) (ClarifyResponse, error) {
	if askClarify == nil {
		return ClarifyResponse{}, fmt.Errorf("no clarify mechanism available (headless mode)")
	}
	return askClarify(req)
}

// registerClarifyTool creates the clarify function tool registered on the
// orchestrator agent. The model calls it to ask the user a mid-task question.
func registerClarifyTool() (tool.Tool, error) {
	return newDocTool(functiontool.Config{
		Name: "clarify",
		Description: "Pause and ask the user a question mid-task. Use when you need " +
			"user input, a preference, or a decision that you cannot infer. Provide up " +
			"to 4 answer options in 'choices' (do not embed them in the question text); " +
			"omit 'choices' for an open-ended question. The run blocks until the user " +
			"answers or the question times out; a canceled/timed-out response means the " +
			"user did not answer, so proceed with your best judgment.",
	}, clarifyHandler)
}

// clarifyHandler is the tool handler invoked by the ADK when the model calls
// the clarify tool. It clamps choices to 4 (Hermes contract), blocks on
// clarifyExec, and normalizes the response for the model.
func clarifyHandler(ctx agent.Context, input ClarifyInput) (ClarifyOutput, error) {
	// Clamp choices to 4 (Hermes contract).
	choices := input.Choices
	if len(choices) > 4 {
		choices = choices[:4]
	}
	resp, err := clarifyExec(ClarifyRequest{
		Question:    input.Question,
		Choices:     choices,
		MultiSelect: input.MultiSelect,
	})
	if err != nil {
		return ClarifyOutput{}, err
	}
	out := ClarifyOutput{
		Question:       input.Question,
		ChoicesOffered: choices,
		UserResponse:   resp.Answer,
		Canceled:       resp.Canceled,
		TimedOut:       resp.TimedOut,
	}
	// Normalize: a canceled/timeout response carries no answer.
	if resp.Canceled || resp.TimedOut {
		out.UserResponse = nil
	}
	return out, nil
}
