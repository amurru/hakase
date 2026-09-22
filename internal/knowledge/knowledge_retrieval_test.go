// knowledge_retrieval_test.go - the retrieval spans wrapping recall_knowledge
// and search_knowledge (docs/otel-tracing/spec.md OT-005): one
// "retrieval knowledge" span per call with the result count, error status on
// failure, and nothing when tracing is off.
package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
)

func recorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	return rec
}

// writeNote seeds one note so search has a result.
func writeNote(t *testing.T, dir, title, body string) {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(title, " ", "-")) + ".md"
	content := "---\ntitle: " + title + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write note: %v", err)
	}
}

func TestRetrievalSpanOnSearch(t *testing.T) {
	rec := recorder(t)
	dir := t.TempDir()
	writeNote(t, dir, "Grafana Tempo", "OTLP tracing setup notes.")

	tools, err := CreateKnowledgeTools(func(string) {}, dir, false)
	if err != nil {
		t.Fatalf("CreateKnowledgeTools: %v", err)
	}
	if _, err := runTool(t, tools[2], map[string]any{"query": "OTLP"}); err != nil {
		t.Fatalf("search_knowledge: %v", err)
	}

	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 retrieval span, got %d", len(ended))
	}
	sp := ended[0]
	if sp.Name() != "retrieval knowledge" {
		t.Errorf("span name %q", sp.Name())
	}
	attrs := map[string]string{}
	for _, kv := range sp.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	if attrs["gen_ai.operation.name"] != "retrieval" {
		t.Errorf("gen_ai.operation.name = %q", attrs["gen_ai.operation.name"])
	}
	if attrs["hakase.retrieval.results"] != "1" {
		t.Errorf("hakase.retrieval.results = %q, want 1", attrs["hakase.retrieval.results"])
	}
}

func TestRetrievalSpanErrorStatusOnFailedRecall(t *testing.T) {
	rec := recorder(t)
	dir := t.TempDir()

	tools, err := CreateKnowledgeTools(func(string) {}, dir, false)
	if err != nil {
		t.Fatalf("CreateKnowledgeTools: %v", err)
	}
	if _, err := runTool(t, tools[1], map[string]any{"name": "no-such-note"}); err == nil {
		t.Fatal("recall of a missing note should fail")
	}

	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 retrieval span, got %d", len(ended))
	}
	sp := ended[0]
	if sp.Status().Code != codes.Error {
		t.Errorf("status %v, want Error for failed recall", sp.Status().Code)
	}
	attrs := map[string]string{}
	for _, kv := range sp.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	if attrs["hakase.retrieval.results"] != "0" {
		t.Errorf("hakase.retrieval.results = %q, want 0", attrs["hakase.retrieval.results"])
	}
}

func TestNoRetrievalSpanWhenTracingDisabled(t *testing.T) {
	otel.SetTracerProvider(noop.NewTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	dir := t.TempDir()
	tools, err := CreateKnowledgeTools(func(string) {}, dir, false)
	if err != nil {
		t.Fatalf("CreateKnowledgeTools: %v", err)
	}
	if _, err := runTool(t, tools[2], map[string]any{"query": "anything"}); err != nil {
		t.Fatalf("search_knowledge: %v", err)
	}
	// The noop global records nothing; the call above passing without panic
	// is the assertion (no span code paths can fail when disabled).
}
