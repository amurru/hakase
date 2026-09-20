// elicitation.go - MCP elicitation (2026-07-28 MRTR) routed into hakase's
// approval/clarify gates.
//
// A remote MCP server that needs mid-call input returns an
// InputRequiredResult; the go-sdk client middleware fulfills the
// inputRequests through the per-server ElicitationHandler installed here and
// retries the call. T2.1 ships the handler as decline-closed; T2.2 adds the
// schema -> gate mapping, T2.3 wires the gates on every surface.
//
// Gate access mirrors agent.Runtime: package-level setters called at startup
// (TUI model in main, web gates in serve mode). Nil gates (headless/cron)
// fail closed. internal/mcp must not import internal/agent (Deps keeps the
// manager as `any` behind a factory for exactly this reason), so the holder
// lives here against the leaf interfaces package.
package mcp

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// elicitationGates holds the interactive gates for MCP elicitation prompts.
// Set after runner setup (the TUI program / web bridge does not exist
// earlier); reads happen on tool-call goroutines.
var elicitationGates = struct {
	sync.RWMutex
	approval interfaces.ApprovalGate
	clarify  interfaces.ClarifyGate
}{}

// SetApprovalGate installs the approval gate for MCP elicitation prompts.
// Call from process startup after the surface's gate exists.
func SetApprovalGate(g interfaces.ApprovalGate) {
	elicitationGates.Lock()
	defer elicitationGates.Unlock()
	elicitationGates.approval = g
}

// SetClarifyGate installs the clarify gate for MCP elicitation prompts.
func SetClarifyGate(g interfaces.ClarifyGate) {
	elicitationGates.Lock()
	defer elicitationGates.Unlock()
	elicitationGates.clarify = g
}

// elicitationApprovalGate returns the installed approval gate, or nil when
// uninstalled (headless mode).
func elicitationApprovalGate() interfaces.ApprovalGate {
	elicitationGates.RLock()
	defer elicitationGates.RUnlock()
	return elicitationGates.approval
}

// elicitationClarifyGate returns the installed clarify gate, or nil when
// uninstalled (headless mode).
func elicitationClarifyGate() interfaces.ClarifyGate {
	elicitationGates.RLock()
	defer elicitationGates.RUnlock()
	return elicitationGates.clarify
}

// newElicitingClient builds the MCP client for one managed server. The
// ElicitationHandler routes server input requests into hakase's gates;
// setting it also advertises the elicitation capability to the server.
func newElicitingClient(server string) *mcp.Client {
	return mcp.NewClient(
		&mcp.Implementation{Name: "hakase", Version: "dev"},
		&mcp.ClientOptions{
			ElicitationHandler: func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				return handleElicitation(server, ctx, req)
			},
		},
	)
}

// handleElicitation fulfills one server input request by routing it into
// hakase's gates:
//
//   - url mode: non-blocking clarify carrying the URL (D3); the tool call
//     continues immediately with accept and no content.
//   - form mode, single boolean property: approval gate (confirm-shaped).
//   - form mode, single non-boolean property: clarify gate (enum properties
//     become choices, otherwise free text).
//   - anything else (multi-property schemas, unknown shapes): decline.
//
// Nil gates (headless/cron) and every timeout/cancel path decline or cancel,
// never accept: elicitation is fail-closed. Accepted answers are coerced to
// the schema's property types because the SDK validates result content
// against the requested schema before retrying.
func handleElicitation(server string, ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	if req == nil || req.Params == nil {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	params := req.Params
	if params.Mode == "" {
		if params.URL != "" || params.ElicitationID != "" {
			params.Mode = "url"
		} else {
			params.Mode = "form"
		}
	}
	sessionID := interfaces.SessionIDFromCtx(ctx)
	switch params.Mode {
	case "url":
		return handleURLElicitation(params, sessionID)
	case "form":
		return handleFormElicitation(server, params, sessionID)
	default:
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
}

// handleURLElicitation surfaces the URL without blocking the tool call (D3).
// The prompt is fired and forgotten: the pending entry lives in the gate
// until answered or expired, while the call continues with accept.
func handleURLElicitation(params *mcp.ElicitParams, sessionID string) (*mcp.ElicitResult, error) {
	if g := elicitationClarifyGate(); g != nil {
		question := params.Message
		if question == "" {
			question = "The MCP server asks you to open this link:"
		}
		go g.AskClarify(interfaces.ClarifyRequest{
			Question:  question + "\n" + params.URL,
			SessionID: sessionID,
		})
	}
	return &mcp.ElicitResult{Action: "accept"}, nil
}

// elicitationProperty is the subset of JSON schema used for elicitation:
// flat objects with primitive properties only (SDK-enforced).
type elicitationProperty struct {
	Type        string `json:"type"`
	Enum        []any  `json:"enum"`
	Description string `json:"description"`
}

// handleFormElicitation routes a form elicitation to the approval or clarify
// gate based on the requested schema's shape.
func handleFormElicitation(server string, params *mcp.ElicitParams, sessionID string) (*mcp.ElicitResult, error) {
	props := elicitationProperties(params.RequestedSchema)
	if len(props) != 1 {
		// Zero or multi-property schemas have no single-prompt mapping;
		// decline closed rather than interrogate the user piecemeal.
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	name := props[0].name
	prop := props[0].elicitationProperty
	if prop.Type == "boolean" && len(prop.Enum) == 0 {
		return handleConfirmElicitation(server, params, sessionID, name)
	}
	return handleFieldElicitation(params, sessionID, name, prop)
}

// namedProperty pairs a schema property with its (sorted) name.
type namedProperty struct {
	name string
	elicitationProperty
}

// elicitationProperties normalizes the requested schema (any JSON value)
// into a deterministically ordered property list. Unparseable schemas yield
// nil, which the caller treats as decline.
func elicitationProperties(schema any) []namedProperty {
	if schema == nil {
		return nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var decoded struct {
		Properties map[string]elicitationProperty `json:"properties"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	names := make([]string, 0, len(decoded.Properties))
	for name := range decoded.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]namedProperty, 0, len(names))
	for _, name := range names {
		out = append(out, namedProperty{name: name, elicitationProperty: decoded.Properties[name]})
	}
	return out
}

// handleConfirmElicitation asks for approval and maps it onto the schema's
// boolean property: approved -> accept{prop: true}, denied/timeout/headless
// -> decline.
func handleConfirmElicitation(server string, params *mcp.ElicitParams, sessionID, prop string) (*mcp.ElicitResult, error) {
	g := elicitationApprovalGate()
	if g == nil {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	approved, err := g.AskApproval(interfaces.ApprovalRequest{
		Tool:      "mcp_" + config.SanitizeMCPServerName(server),
		Command:   params.Message,
		Risk:      "unknown",
		Reason:    "mcp elicitation from server " + strconv.Quote(server),
		SessionID: sessionID,
	})
	if err != nil || !approved {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	return &mcp.ElicitResult{Action: "accept", Content: map[string]any{prop: true}}, nil
}

// handleFieldElicitation asks a clarify question for a single non-boolean
// property: enum values become choices, otherwise free text. Answers are
// coerced to the property type. Timeout/headless declines; user cancel
// cancels.
func handleFieldElicitation(params *mcp.ElicitParams, sessionID, name string, prop elicitationProperty) (*mcp.ElicitResult, error) {
	g := elicitationClarifyGate()
	if g == nil {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	question := params.Message
	if prop.Description != "" {
		question += " (" + prop.Description + ")"
	}
	var choices []string
	if len(prop.Enum) > 0 {
		choices = make([]string, 0, len(prop.Enum))
		for _, e := range prop.Enum {
			choices = append(choices, fmt.Sprint(e))
		}
	}
	resp, err := g.AskClarify(interfaces.ClarifyRequest{
		Question:  question,
		Choices:   choices,
		SessionID: sessionID,
	})
	if err != nil {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	if resp.TimedOut {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	if resp.Canceled {
		return &mcp.ElicitResult{Action: "cancel"}, nil
	}
	if len(resp.Answer) == 0 {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	value, ok := coerceElicitationAnswer(resp.Answer[0], prop)
	if !ok {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	return &mcp.ElicitResult{Action: "accept", Content: map[string]any{name: value}}, nil
}

// coerceElicitationAnswer converts a gate answer string to the schema
// property's JSON type. Enum entries keep their native value: the choice
// matching the answer's string form wins.
func coerceElicitationAnswer(answer string, prop elicitationProperty) (any, bool) {
	for _, e := range prop.Enum {
		if fmt.Sprint(e) == answer {
			return e, true
		}
	}
	if len(prop.Enum) > 0 {
		return nil, false
	}
	switch prop.Type {
	case "", "string":
		return answer, true
	case "boolean":
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "true", "yes", "y", "1":
			return true, true
		case "false", "no", "n", "0":
			return false, true
		}
		return nil, false
	case "integer":
		n, err := strconv.Atoi(strings.TrimSpace(answer))
		if err != nil {
			return nil, false
		}
		return float64(n), true // marshals as "3", validates as integer
	case "number":
		n, err := strconv.ParseFloat(strings.TrimSpace(answer), 64)
		if err != nil {
			return nil, false
		}
		return n, true
	default:
		return nil, false
	}
}
