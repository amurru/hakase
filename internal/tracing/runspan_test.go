// runspan_test.go - run root span + traceparent tests against a recording
// provider (no network).
package tracing

import (
	"context"
	"regexp"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recorderProvider installs a syncing recorder provider and returns it.
func recorderProvider(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	restoreProvider(t)
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	return rec
}

// spanAttrMap flattens a recorded span's attributes.
func spanAttrMap(sp sdktrace.ReadOnlySpan) map[string]string {
	m := map[string]string{}
	for _, kv := range sp.Attributes() {
		m[string(kv.Key)] = kv.Value.Emit()
	}
	return m
}

// TestRunSpanAttrsAndSuccessStatus checks the pinned attributes and the
// completed → Ok mapping.
func TestRunSpanAttrsAndSuccessStatus(t *testing.T) {
	rec := recorderProvider(t)

	_, run := RunSpan(context.Background(), RunParams{
		Transport: "web",
		SessionID: "sess-9",
		TaskID:    "task-9",
		Project:   "hakase",
		Extra:     map[string]string{"hakase.cron.job": "nightly"},
	})
	run.End(StatusCompleted, "")

	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 ended span, got %d", len(ended))
	}
	sp := ended[0]
	if sp.Name() != "hakase.run" {
		t.Errorf("span name %q, want hakase.run", sp.Name())
	}
	if sp.Status().Code != codes.Ok {
		t.Errorf("status %v, want Ok for completed", sp.Status().Code)
	}
	attrs := spanAttrMap(sp)
	for k, want := range map[string]string{
		"hakase.transport":       "web",
		"hakase.session.id":      "sess-9",
		"hakase.task.id":         "task-9",
		"hakase.project":         "hakase",
		"gen_ai.conversation.id": "sess-9",
		"hakase.cron.job":        "nightly",
	} {
		if attrs[k] != want {
			t.Errorf("attr %s = %q, want %q", k, attrs[k], want)
		}
	}
}

// TestRunSpanFailureStatus checks failed/timed_out → Error with the message
// recorded, and that empty fields are omitted.
func TestRunSpanFailureStatus(t *testing.T) {
	rec := recorderProvider(t)

	_, run := RunSpan(context.Background(), RunParams{Transport: "cron", TaskID: "t"})
	run.End(StatusTimedOut, "cron job x timed out")

	sp := rec.Ended()[0]
	if sp.Status().Code != codes.Error {
		t.Errorf("status %v, want Error for timed_out", sp.Status().Code)
	}
	if sp.Status().Description != "cron job x timed out" {
		t.Errorf("status description %q", sp.Status().Description)
	}
	attrs := spanAttrMap(sp)
	if attrs["hakase.run.status"] != "timed_out" {
		t.Errorf("hakase.run.status = %q, want timed_out", attrs["hakase.run.status"])
	}
	if attrs["error.message"] != "cron job x timed out" {
		t.Errorf("error.message = %q", attrs["error.message"])
	}
	if _, ok := attrs["hakase.session.id"]; ok {
		t.Error("hakase.session.id should be omitted when empty")
	}
}

// TestRunEndNilSafe pins the nil-safety the run loops lean on.
func TestRunEndNilSafe(t *testing.T) {
	var run *Run
	run.End(StatusFailed, "no span at all") // must not panic
}

// TestTraceparentFormat checks the W3C wire format for a recording span and
// the empty result without one.
func TestTraceparentFormat(t *testing.T) {
	rec := recorderProvider(t)

	if tp := Traceparent(context.Background()); tp != "" {
		t.Fatalf("Traceparent(background) = %q, want empty", tp)
	}

	ctx, span := Tracer().Start(context.Background(), "tp")
	defer span.End()
	tp := Traceparent(ctx)
	re := regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)
	if !re.MatchString(tp) {
		t.Fatalf("traceparent %q does not match the W3C format", tp)
	}
	if len(rec.Ended()) != 0 {
		t.Error("no span should have ended yet")
	}
}
