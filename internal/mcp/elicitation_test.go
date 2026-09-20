package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"amurru/hakase/internal/interfaces"
	"github.com/google/jsonschema-go/jsonschema"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// elicitingFixture starts a stateless (2026-07-28 MRTR) HTTP server with one
// tool ("deploy") that requires a boolean confirmation. The first call
// returns input_required; the retry completes with "deployed" on accept and
// "cancelled" otherwise. It reports the action the client sent back.
func elicitingFixture(t *testing.T) (url string, gotAction *atomic.Value) {
	t.Helper()
	gotAction = &atomic.Value{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "elicit", Version: "0"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "deploy",
		Description: "deploys after confirmation",
		InputSchema: &jsonschema.Schema{Type: "object"},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if len(req.Params.InputResponses) == 0 {
			return &mcp.CallToolResult{
				InputRequests: mcp.InputRequestMap{"confirm": &mcp.ElicitParams{
					Message: "Deploy to production?",
					RequestedSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"confirm": map[string]any{"type": "boolean"},
						},
					},
				}},
				RequestState: "deploy-1",
			}, nil
		}
		resp, _ := req.Params.InputResponses["confirm"].(*mcp.ElicitResult)
		action := ""
		if resp != nil {
			action = resp.Action
		}
		gotAction.Store(action)
		text := "cancelled"
		if action == "accept" {
			text = "deployed"
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL, gotAction
}

// TestHandleElicitationDeclinesClosed: the T2.1 stub declines without gates.
func TestHandleElicitationDeclinesClosed(t *testing.T) {
	res, err := handleElicitation("srv", context.Background(), &mcp.ElicitRequest{
		Params: &mcp.ElicitParams{Message: "x"},
	})
	if err != nil {
		t.Fatalf("handleElicitation: %v", err)
	}
	if res.Action != "decline" {
		t.Fatalf("action = %q, want decline", res.Action)
	}
}

// TestElicitingServerCompletesWithDecline: an elicitation-bearing tool
// through a client built by newElicitingClient reaches the stub and the
// call completes (server observes decline, tool reports cancelled).
func TestElicitingServerCompletesWithDecline(t *testing.T) {
	url, gotAction := elicitingFixture(t)
	client := newElicitingClient("t")
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: url}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cs.Close()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "deploy"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool errored: %+v", res.Content)
	}
	if len(res.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok || tc.Text != "cancelled" {
		t.Fatalf("result = %+v, want cancelled text", res.Content)
	}
	if gotAction.Load() != "decline" {
		t.Fatalf("server saw action %v, want decline", gotAction.Load())
	}
}

// fakeApprovalGate scripts AskApproval answers and records requests.
type fakeApprovalGate struct {
	approve bool
	err     error
	got     []interfaces.ApprovalRequest
}

func (f *fakeApprovalGate) AskApproval(req interfaces.ApprovalRequest) (bool, error) {
	f.got = append(f.got, req)
	return f.approve, f.err
}

func (f *fakeApprovalGate) ApprovalConfig() interfaces.ApprovalConfig {
	return interfaces.ApprovalConfig{}
}

func (f *fakeApprovalGate) ApprovalExpiry() time.Duration { return time.Second }

// fakeClarifyGate scripts AskClarify answers and records requests.
type fakeClarifyGate struct {
	resp interfaces.ClarifyResponse
	err  error
	mu   sync.Mutex
	got  []interfaces.ClarifyRequest
}

func (f *fakeClarifyGate) AskClarify(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, req)
	return f.resp, f.err
}

func (f *fakeClarifyGate) ClarifyConfig() interfaces.ClarifyConfig {
	return interfaces.ClarifyConfig{}
}

func (f *fakeClarifyGate) ClarifyExpiry() time.Duration { return time.Second }

func (f *fakeClarifyGate) requests() []interfaces.ClarifyRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]interfaces.ClarifyRequest(nil), f.got...)
}

// installElicitationGates swaps the package gates for the test.
func installElicitationGates(a interfaces.ApprovalGate, c interfaces.ClarifyGate) {
	SetApprovalGate(a)
	SetClarifyGate(c)
}

func clearElicitationGates() {
	SetApprovalGate(nil)
	SetClarifyGate(nil)
}

func boolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"confirm": map[string]any{"type": "boolean"},
		},
	}
}

// TestHandleElicitationConfirmApproved maps a boolean schema + approval to
// accept{confirm: true} and routes the hakase session.
func TestHandleElicitationConfirmApproved(t *testing.T) {
	ag, cg := &fakeApprovalGate{approve: true}, &fakeClarifyGate{}
	installElicitationGates(ag, cg)
	defer clearElicitationGates()
	interfaces.RegisterTaskSession("test", "sess_9")
	defer interfaces.UnregisterTask("test")

	res, err := handleElicitation("github", mcpTestCtx{}, &mcp.ElicitRequest{
		Params: &mcp.ElicitParams{Message: "Delete 3 files?", RequestedSchema: boolSchema()},
	})
	if err != nil {
		t.Fatalf("handleElicitation: %v", err)
	}
	if res.Action != "accept" {
		t.Fatalf("action = %q, want accept", res.Action)
	}
	if res.Content["confirm"] != true {
		t.Fatalf("content = %v, want confirm:true", res.Content)
	}
	if len(ag.got) != 1 {
		t.Fatalf("approval prompts = %d, want 1", len(ag.got))
	}
	got := ag.got[0]
	if got.Tool != "mcp_github" || got.SessionID != "sess_9" {
		t.Fatalf("approval req = %+v, want tool mcp_github session sess_9", got)
	}
}

// TestHandleElicitationConfirmDeniedOrHeadless declines closed.
func TestHandleElicitationConfirmDeniedOrHeadless(t *testing.T) {
	params := &mcp.ElicitParams{Message: "Delete?", RequestedSchema: boolSchema()}

	installElicitationGates(&fakeApprovalGate{approve: false}, &fakeClarifyGate{})
	res, _ := handleElicitation("s", mcpTestCtx{}, &mcp.ElicitRequest{Params: params})
	if res.Action != "decline" {
		t.Fatalf("denied action = %q, want decline", res.Action)
	}

	clearElicitationGates()
	res, _ = handleElicitation("s", mcpTestCtx{}, &mcp.ElicitRequest{Params: params})
	if res.Action != "decline" {
		t.Fatalf("headless action = %q, want decline", res.Action)
	}
}

// TestHandleElicitationEnumChoices offers enum values and echoes the native
// value back.
func TestHandleElicitationEnumChoices(t *testing.T) {
	ag, cg := &fakeApprovalGate{}, &fakeClarifyGate{resp: interfaces.ClarifyResponse{Answer: []string{"prod"}}}
	installElicitationGates(ag, cg)
	defer clearElicitationGates()

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"env": map[string]any{"type": "string", "enum": []any{"dev", "prod"}},
		},
	}
	res, err := handleElicitation("s", mcpTestCtx{}, &mcp.ElicitRequest{
		Params: &mcp.ElicitParams{Message: "Which env?", RequestedSchema: schema},
	})
	if err != nil {
		t.Fatalf("handleElicitation: %v", err)
	}
	if res.Action != "accept" || res.Content["env"] != "prod" {
		t.Fatalf("result = %+v, want accept env:prod", res)
	}
	got := cg.requests()
	if len(got) != 1 || len(got[0].Choices) != 2 {
		t.Fatalf("clarify prompts = %+v, want one with 2 choices", got)
	}
}

// TestHandleElicitationClarifyOutcomes covers free text, timeout, cancel,
// and multi-property decline.
func TestHandleElicitationClarifyOutcomes(t *testing.T) {
	freeSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reason": map[string]any{"type": "string"},
		},
	}
	multiSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a": map[string]any{"type": "string"},
			"b": map[string]any{"type": "string"},
		},
	}

	cg := &fakeClarifyGate{resp: interfaces.ClarifyResponse{Answer: []string{"oops"}}}
	installElicitationGates(&fakeApprovalGate{}, cg)
	defer clearElicitationGates()
	res, _ := handleElicitation("s", mcpTestCtx{}, &mcp.ElicitRequest{
		Params: &mcp.ElicitParams{Message: "Why?", RequestedSchema: freeSchema},
	})
	if res.Action != "accept" || res.Content["reason"] != "oops" {
		t.Fatalf("free text = %+v, want accept reason:oops", res)
	}

	cg.resp = interfaces.ClarifyResponse{TimedOut: true}
	res, _ = handleElicitation("s", mcpTestCtx{}, &mcp.ElicitRequest{
		Params: &mcp.ElicitParams{Message: "Why?", RequestedSchema: freeSchema},
	})
	if res.Action != "decline" {
		t.Fatalf("timeout action = %q, want decline", res.Action)
	}

	cg.resp = interfaces.ClarifyResponse{Canceled: true}
	res, _ = handleElicitation("s", mcpTestCtx{}, &mcp.ElicitRequest{
		Params: &mcp.ElicitParams{Message: "Why?", RequestedSchema: freeSchema},
	})
	if res.Action != "cancel" {
		t.Fatalf("cancel action = %q, want cancel", res.Action)
	}

	res, _ = handleElicitation("s", mcpTestCtx{}, &mcp.ElicitRequest{
		Params: &mcp.ElicitParams{Message: "Two?", RequestedSchema: multiSchema},
	})
	if res.Action != "decline" {
		t.Fatalf("multi-property action = %q, want decline", res.Action)
	}
}

// TestHandleElicitationURLFiresPromptWithoutBlocking: url mode accepts
// immediately while the link prompt reaches the clarify gate.
func TestHandleElicitationURLFiresPromptWithoutBlocking(t *testing.T) {
	cg := &fakeClarifyGate{resp: interfaces.ClarifyResponse{Answer: []string{"opened"}}}
	installElicitationGates(&fakeApprovalGate{}, cg)
	defer clearElicitationGates()

	start := time.Now()
	res, err := handleElicitation("s", mcpTestCtx{}, &mcp.ElicitRequest{
		Params: &mcp.ElicitParams{Mode: "url", Message: "Auth needed", URL: "https://example.com/auth"},
	})
	if err != nil {
		t.Fatalf("handleElicitation: %v", err)
	}
	if res.Action != "accept" {
		t.Fatalf("action = %q, want accept", res.Action)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("url elicitation blocked the call")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(cg.requests()) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("url prompt never reached the clarify gate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := cg.requests()[0].Question; !strings.Contains(got, "https://example.com/auth") {
		t.Fatalf("prompt = %q, want the URL", got)
	}
}
