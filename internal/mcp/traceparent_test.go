// traceparent_test.go - SEP-414 propagation: the sending middleware injects
// _meta["traceparent"] on outbound requests while a span is recording, leaves
// span-less requests untouched, and never overwrites an existing value.
// Uses the go-sdk in-memory transports so a real client session flows
// through the middleware.
package mcp

import (
	"context"
	"regexp"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"

	"amurru/hakase/internal/tracing"
)

// recorderGlobal installs a recording provider and returns its recorder.
func recorderGlobal(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	return rec
}

// connectEchoMeta builds a client/server pair whose echo_meta tool returns
// the traceparent it received in _meta, with the middleware installed
// client-side.
func connectEchoMeta(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	ct, st := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "srv", Version: "v"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo_meta"},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
			tp, _ := req.Params.Meta["traceparent"].(string)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: tp}},
			}, struct{}{}, nil
		})
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "cli", Version: "v"}, nil)
	client.AddSendingMiddleware(traceparentMiddleware())
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

func TestTraceparentInjectedWhileRecording(t *testing.T) {
	recorderGlobal(t)
	cs := connectEchoMeta(t)

	sctx, span := otel.GetTracerProvider().Tracer("test").Start(context.Background(), "call")
	defer span.End()

	res, err := cs.CallTool(sctx, &mcp.CallToolParams{Name: "echo_meta"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	got := resultText(t, res)
	re := regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)
	if !re.MatchString(got) {
		t.Fatalf("server saw traceparent %q, want W3C format", got)
	}
	if want := tracing.Traceparent(sctx); got != want {
		t.Fatalf("server saw %q, want the caller's traceparent %q", got, want)
	}
}

func TestTraceparentOmittedWithoutSpan(t *testing.T) {
	recorderGlobal(t)
	cs := connectEchoMeta(t)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo_meta"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := resultText(t, res); got != "" {
		t.Fatalf("server saw traceparent %q without a recording span, want empty", got)
	}
}

func TestTraceparentNotOverwritten(t *testing.T) {
	recorderGlobal(t)
	cs := connectEchoMeta(t)

	sctx, span := otel.GetTracerProvider().Tracer("test").Start(context.Background(), "call")
	defer span.End()
	preset := "00-11111111111111111111111111111111-2222222222222222-01"

	res, err := cs.CallTool(sctx, &mcp.CallToolParams{
		Name: "echo_meta",
		Meta: mcp.Meta{"traceparent": preset},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := resultText(t, res); got != preset {
		t.Fatalf("server saw %q, want the preset traceparent %q", got, preset)
	}
}
