package runserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/interfaces"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Gates implements interfaces.ApprovalGate + interfaces.ClarifyGate for MCP
// runserver runs: hakase's mid-run approval/clarification prompts are
// delivered to the driving MCP client and answered by the human behind it.
// This is what makes hakase usable as a sub-agent without choosing between
// "headless fail-closed on every risky command" and "allow-all".
//
// Delivery is the SEP-2322 input-required mechanism (see the package doc):
// a gate ask hands its elicitation to the run's gateBroker and blocks; the
// run tool handler suspends the tool call with the elicitation as an
// InputRequest; the driving client fulfills it and the call is retried with
// the response. The go-sdk fulfills the same InputRequests transparently for
// pre-2026-07-28 clients, so this code path serves both. Every failure path
// (no broker for the session, unfulfillable client, expiry) is fail-closed -
// the same posture as every headless hakase process.
type Gates struct {
	approvalC interfaces.ApprovalConfig
	clarifyC  interfaces.ClarifyConfig

	mu      sync.Mutex
	brokers map[string]*gateBroker // hakase session id -> its run's broker
}

// NewGates builds the gates from the runtime gate config (cfg.Approval /
// cfg.Clarify).
func NewGates(approval interfaces.ApprovalConfig, clarify interfaces.ClarifyConfig) *Gates {
	return &Gates{
		approvalC: approval,
		clarifyC:  clarify,
		brokers:   map[string]*gateBroker{},
	}
}

// attach registers a run's broker under its hakase session id. Called by the
// run handler for the run's whole lifetime (across gate round trips).
func (g *Gates) attach(sessionID string, b *gateBroker) {
	g.mu.Lock()
	g.brokers[sessionID] = b
	g.mu.Unlock()
}

// detach removes a run's broker (run finished).
func (g *Gates) detach(sessionID string, b *gateBroker) {
	g.mu.Lock()
	if g.brokers[sessionID] == b {
		delete(g.brokers, sessionID)
	}
	g.mu.Unlock()
}

// brokerFor resolves the broker a gate ask should ride: the run serving the
// request's session, or - only when the request carries no session id
// (session-less surfaces) and exactly one run is active - that run. Anything
// else is nil, i.e. fail-closed: a session-A approval must never ride
// session-B's round trip on a multi-client transport.
func (g *Gates) brokerFor(sessionID string) *gateBroker {
	g.mu.Lock()
	defer g.mu.Unlock()
	if sessionID != "" {
		return g.brokers[sessionID]
	}
	if len(g.brokers) == 1 {
		for _, b := range g.brokers {
			return b
		}
	}
	return nil
}

// ApprovalConfig returns the runtime approval configuration.
func (g *Gates) ApprovalConfig() interfaces.ApprovalConfig { return g.approvalC }

// ApprovalExpiry returns the configured approval expiry (default 60s).
func (g *Gates) ApprovalExpiry() time.Duration {
	if g.approvalC.ExpirySeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(g.approvalC.ExpirySeconds) * time.Second
}

// ClarifyConfig returns the runtime clarify configuration.
func (g *Gates) ClarifyConfig() interfaces.ClarifyConfig { return g.clarifyC }

// ClarifyExpiry returns the configured clarify expiry (default 120s).
func (g *Gates) ClarifyExpiry() time.Duration {
	if g.clarifyC.ExpirySeconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(g.clarifyC.ExpirySeconds) * time.Second
}

// elicit routes one elicitation to the session's run and waits for the
// client's answer, bounded by the gate expiry.
func (g *Gates) elicit(ctx context.Context, sessionID string, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
	b := g.brokerFor(sessionID)
	if b == nil {
		return nil, errors.New("no MCP client run is bound for this session (fail-closed)")
	}
	return b.ask(ctx, params)
}

// AskApproval resolves one approval request. Mode allow/deny short-circuit;
// interactive mode elicits the client. Fail-closed on every failure path.
func (g *Gates) AskApproval(req interfaces.ApprovalRequest) (bool, error) {
	switch g.approvalC.Mode {
	case "allow":
		return true, nil
	case "deny":
		return false, nil
	}

	command := req.Command
	if len(req.Args) > 0 && command != "" {
		command = command + " " + strings.Join(req.Args, " ")
	}
	message := fmt.Sprintf("hakase wants to execute %s (risk: %s)\n\n%s\n\nReason: %s",
		req.Tool, req.Risk, command, req.Reason)

	ctx, cancel := context.WithTimeout(context.Background(), g.ApprovalExpiry())
	defer cancel()
	res, err := g.elicit(ctx, req.SessionID, &mcp.ElicitParams{
		Message: message,
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"approve": map[string]any{
					"type":        "boolean",
					"description": "Allow this " + req.Tool + " invocation?",
				},
			},
			"required": []string{"approve"},
		},
	})
	if err != nil {
		return false, fmt.Errorf("approval refused (fail-closed): %w", err)
	}
	if res.Action != "accept" {
		// An explicit decline or a dismissed dialog is a "no", not an error.
		return false, nil
	}
	approved, _ := res.Content["approve"].(bool)
	return approved, nil
}

// AskClarify resolves one mid-run question. Choices become an enum (or an
// array of enum items under multi-select); free text otherwise.
func (g *Gates) AskClarify(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
	props := map[string]any{}
	required := []string{"answer"}
	switch {
	case len(req.Choices) > 0 && req.MultiSelect:
		props["answers"] = map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string", "enum": req.Choices},
			"description": "Pick one or more",
			"maxItems":    len(req.Choices),
		}
		required = []string{"answers"}
	case len(req.Choices) > 0:
		props["answer"] = map[string]any{
			"type":        "string",
			"enum":        req.Choices,
			"description": "Pick one",
		}
	default:
		props["answer"] = map[string]any{
			"type":        "string",
			"description": "Your answer",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.ClarifyExpiry())
	defer cancel()
	res, err := g.elicit(ctx, req.SessionID, &mcp.ElicitParams{
		Message:         req.Question,
		RequestedSchema: map[string]any{"type": "object", "properties": props, "required": required},
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return interfaces.ClarifyResponse{TimedOut: true}, nil
		}
		return interfaces.ClarifyResponse{}, fmt.Errorf("clarify failed: %w", err)
	}
	if res.Action != "accept" {
		return interfaces.ClarifyResponse{Canceled: true}, nil
	}

	answer := interfaces.ClarifyResponse{}
	if raw, ok := res.Content["answers"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				answer.Answer = append(answer.Answer, s)
			}
		}
	} else if s, ok := res.Content["answer"].(string); ok && s != "" {
		answer.Answer = []string{s}
	}
	return answer, nil
}
