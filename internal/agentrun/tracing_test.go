// tracing_test.go - RunTurn's tracing run span: one "hakase.run" span per
// run, ended failed with the panic message on the panic path, and nothing
// recorded when tracing is disabled. Uses a nil Runner: RunTurn recovers the
// nil-deref panic, which exercises the real span wiring without an ADK
// runner harness.
package agentrun

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"

	"google.golang.org/genai"
)

// recorder installs a recording global provider; cleanup restores a fresh
// noop (the otel global's delegating wrapper keeps its delegate permanently,
// so restoring "the previous provider" would leak it into later tests).
func recorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	return rec
}

// noopGlobal pins the global to a fresh noop provider — the same contract as
// "tracing never installed" — and restores noop on cleanup.
func noopGlobal(t *testing.T) {
	t.Helper()
	otel.SetTracerProvider(noop.NewTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })
}

// runSpan finds the single hakase.run span among the ended spans.
func runSpan(t *testing.T, rec *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	var found sdktrace.ReadOnlySpan
	for _, sp := range rec.Ended() {
		if sp.Name() == "hakase.run" {
			if found != nil {
				t.Fatal("more than one hakase.run span for one run")
			}
			found = sp
		}
	}
	if found == nil {
		t.Fatalf("no hakase.run span among %d ended spans", len(rec.Ended()))
	}
	return found
}

func TestRunTurnDisabledTracingRecordsNothing(t *testing.T) {
	noopGlobal(t)
	d := NewForTransport(nil, nil, "web")
	sink := &recordingSink{}
	d.RunTurn(context.Background(), "sess-1", genai.NewContentFromText("hi", "user"), sink)
	if !sink.done {
		t.Fatal("OnDone not called with tracing disabled")
	}
}

func TestRunTurnRunSpanPanicPath(t *testing.T) {
	rec := recorder(t)
	d := NewForTransport(nil, nil, "web") // nil Runner: panics, recovered
	sink := &recordingSink{}
	d.RunTurn(context.Background(), "sess-1", genai.NewContentFromText("hi", "user"), sink)
	if !sink.done {
		t.Fatal("OnDone not called on the panic path")
	}

	sp := runSpan(t, rec)
	if sp.Status().Code != codes.Error {
		t.Errorf("status %v, want Error on the panic path", sp.Status().Code)
	}
	if !strings.Contains(sp.Status().Description, "panic") {
		t.Errorf("status description %q should mention the panic", sp.Status().Description)
	}
	attrs := map[string]string{}
	for _, kv := range sp.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	if attrs["hakase.transport"] != "web" {
		t.Errorf("hakase.transport = %q, want web", attrs["hakase.transport"])
	}
	if attrs["hakase.session.id"] != "sess-1" {
		t.Errorf("hakase.session.id = %q, want sess-1", attrs["hakase.session.id"])
	}
	if attrs["gen_ai.conversation.id"] != "sess-1" {
		t.Errorf("gen_ai.conversation.id = %q, want sess-1", attrs["gen_ai.conversation.id"])
	}
	if attrs["hakase.task.id"] == "" {
		t.Error("hakase.task.id should be set (generated per run)")
	}
}

func TestNewForTransportSetsLabel(t *testing.T) {
	d := NewForTransport(nil, nil, "telegram")
	if d.Transport != "telegram" {
		t.Fatalf("Transport = %q, want telegram", d.Transport)
	}
	if d := New(nil, nil); d.Transport != "" {
		t.Fatalf("New should leave Transport empty, got %q", d.Transport)
	}
}
