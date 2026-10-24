package agent

import (
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"
	"context"
	"fmt"
	"strings"
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
	SessionID   string   // hakase session of the asking run (gate prompt routing)
}

// ClarifyResponse is the user's answer, plus cancel/timeout signals.
type ClarifyResponse struct {
	Answer   []string // selected choice(s) or free-text answer (1 element)
	Canceled bool     // user pressed Esc
	TimedOut bool     // no answer within the expiry window
}

// ClarifyInput is the schema for the clarify tool as seen by the model.
// doc tags are injected into the inferred JSON schema by util.NewDocTool.
// Choices is a util.StringSlice: providers occasionally serialize the
// array as a JSON string, and the tolerant form keeps the question
// alive instead of failing schema validation.
type ClarifyInput struct {
	Question    string           `json:"question" doc:"The question to ask the user (required)."`
	Choices     util.StringSlice `json:"choices,omitempty" doc:"Optional predefined answer choices (up to 4). Omit for an open-ended question."`
	MultiSelect bool             `json:"multi_select,omitempty" doc:"When true, the user may select multiple choices. Only applies when choices are provided."`
}

// ClarifyOutput is the tool result returned to the model.
type ClarifyOutput struct {
	Question       string   `json:"question" doc:"The question that was asked."`
	ChoicesOffered []string `json:"choices_offered,omitempty" doc:"The choices that were offered, if any."`
	UserResponse   []string `json:"user_response" doc:"The user's answer. A single element for free text or a single choice; multiple elements when multi_select is used. Empty when the question was canceled or timed out."`
	Canceled       bool     `json:"canceled,omitempty" doc:"True when the user dismissed the question (Esc)."`
	TimedOut       bool     `json:"timed_out,omitempty" doc:"True when no answer arrived before the expiry timeout."`
}

// redactChoiceList redacts each choice for the persisted pause boundary
// (S10/H2): the live prompt carries full text, the record carries only
// redacted values.
func redactChoiceList(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, c := range in {
		out[i] = util.RedactTruncate(c, 0)
	}
	return out
}

// clarifyTimeout returns the configured clarify expiry duration from deps.
// Defaults to 120 seconds when not configured (0/negative).
func clarifyTimeout() time.Duration {
	if deps == nil || deps.ClarifyCfg.ExpirySeconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(deps.ClarifyCfg.ExpirySeconds) * time.Second
}

// validateClarifyRequest enforces the clarify contract fail-closed
// (M4 loop 5): the question is required, at most 4 choices, every choice
// a non-empty string, and multi_select only with choices. Each entry is
// inspected individually so a wrong-typed or empty choice fails the call
// instead of reaching the gate with a dead choice list.
func validateClarifyRequest(req ClarifyRequest) error {
	if strings.TrimSpace(req.Question) == "" {
		return fmt.Errorf("clarify: question must not be empty")
	}
	for i, c := range req.Choices {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("clarify: choice %d must not be empty", i)
		}
	}
	if req.MultiSelect && len(req.Choices) == 0 {
		return fmt.Errorf("clarify: multi_select requires choices")
	}
	return nil
}

// parseClarifyChoices decodes a raw choices value with an explicit type
// switch (M4 loop 5): nil/absent stays nil (open-ended), []any and
// []string are inspected entry-by-entry (every entry must be a string),
// a string decodes as one choice, and any other type fails closed
// instead of reaching the gate with a dead list.
func parseClarifyChoices(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	switch t := raw.(type) {
	case []any:
		if len(t) == 0 {
			return nil, nil
		}
		out := make([]string, 0, len(t))
		for i, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("clarify: choice %d must be a string (got %T)", i, e)
			}
			if strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("clarify: choice %d must not be empty", i)
			}
			out = append(out, s)
		}
		return out, nil
	case []string:
		for i, s := range t {
			if strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("clarify: choice %d must not be empty", i)
			}
		}
		if len(t) == 0 {
			return nil, nil
		}
		return t, nil
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, nil
		}
		return []string{t}, nil
	default:
		return nil, fmt.Errorf("clarify: choices must be an array of strings (got %T)", raw)
	}
}

// askClarify wraps the interactive clarify gate. When the gate is nil
// (headless mode / not yet wired), fails closed. While blocked, the
// pause is recorded for durable resume (best-effort; never fails the
// gate) and removed on resolve.
func askClarify(ctx context.Context, req ClarifyRequest) (ClarifyResponse, error) {
	if err := validateClarifyRequest(req); err != nil {
		return ClarifyResponse{}, err
	}
	if rt == nil {
		return ClarifyResponse{}, fmt.Errorf("no clarify mechanism available (headless mode)")
	}
	g := rt.ClarifyGate()
	if g == nil {
		return ClarifyResponse{}, fmt.Errorf("no clarify mechanism available (headless mode)")
	}
	pauseID := recordGatePause(ctx, hakasesession.PauseGateClarify, req.SessionID,
		"clarify: "+util.RedactTruncate(req.Question, util.MaxLogField),
		map[string]any{
			"question": util.RedactTruncate(req.Question, 0), "choices": redactChoiceList(req.Choices),
			"multi_select": req.MultiSelect,
		})
	if pauseID != "" {
		defer unrecordGatePause(pauseID)
	}
	ifaceResp, err := g.AskClarify(ctx, interfaces.ClarifyRequest{
		Question:    req.Question,
		Choices:     req.Choices,
		MultiSelect: req.MultiSelect,
		SessionID:   req.SessionID,
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
		// Durable resume (Phase 4): marks the call event with
		// LongRunningToolIDs so a restart mid-question can resume
		// from the answer. Synchronous behavior is unchanged.
		IsLongRunning: true,
	}, func(ctx agent.Context, input ClarifyInput) (ClarifyOutput, error) {
		// M4 loop 5: decode choices through the explicit type switch so
		// a wrong-typed raw value fails closed instead of reaching the
		// gate with a dead list. The typed StringSlice already coerced
		// provider string forms; re-inspect each entry here.
		choices, err := parseClarifyChoices([]string(input.Choices))
		if err != nil {
			return ClarifyOutput{}, err
		}
		resp, err := askClarify(ctx, ClarifyRequest{
			Question:    input.Question,
			Choices:     choices,
			MultiSelect: input.MultiSelect,
			SessionID:   interfaces.SessionIDFromCtx(ctx),
		})
		if err != nil {
			return ClarifyOutput{}, err
		}
		return ClarifyOutput{
			Question:       input.Question,
			ChoicesOffered: []string(input.Choices),
			UserResponse:   resp.Answer,
			Canceled:       resp.Canceled,
			TimedOut:       resp.TimedOut,
		}, nil
	})
}
