// tracing_test.go - provider bootstrap tests: disabled = untouched global
// no-op; enabled = spans exported to an in-process OTLP/HTTP collector, and
// tracers captured before Install still record (the global delegating proxy
// property ADK's package-init tracers rely on).
package tracing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/proto"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// restoreProvider restores a fresh noop global provider on cleanup. The
// otel global's delegating wrapper keeps its delegate permanently once set,
// so the pristine default cannot be re-installed after the first
// SetTracerProvider — a fresh noop provider is the same contract ("global
// stays no-op") and is order-independent. Tests in this package mutate the
// global; they must stay sequential (no t.Parallel).
func restoreProvider(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })
}

// TestInstallDisabledLeavesNoop pins the disabled default: nothing is
// installed, spans are not recorded, and no traceparent is produced.
func TestInstallDisabledLeavesNoop(t *testing.T) {
	shutdown, err := Install(Options{})
	if err != nil {
		t.Fatalf("Install(disabled): %v", err)
	}
	defer shutdown()

	_, span := Start(context.Background(), "noop-check")
	defer span.End()
	if span.IsRecording() {
		t.Fatal("span is recording with tracing disabled; global provider was installed")
	}
	if tp := Traceparent(context.Background()); tp != "" {
		t.Fatalf("Traceparent with tracing disabled = %q, want empty", tp)
	}
}

// TestInstallEnabledExportsOTLP runs the install → span → flush → decode
// path against an in-process OTLP/HTTP collector (the delegation property
// for pre-Install tracers lives in aa_global_test.go).
func TestInstallEnabledExportsOTLP(t *testing.T) {
	restoreProvider(t)

	received := make(chan *collectortracepb.ExportTraceServiceRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Errorf("unexpected OTLP path %q", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		req := &collectortracepb.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(body, req); err != nil {
			t.Errorf("unmarshal export request: %v", err)
		}
		received <- req
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	shutdown, err := Install(Options{
		Enabled:     true,
		Endpoint:    srv.URL,
		SampleRatio: 1,
		Version:     "test",
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	_, span := Start(context.Background(), "export-check")
	if !span.IsRecording() {
		t.Fatal("span not recording with tracing enabled")
	}
	span.SetAttributes(attribute.String("hakase.check", "yes"))
	span.End()

	done := make(chan struct{})
	go func() { defer close(done); shutdown() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not flush in time")
	}

	var req *collectortracepb.ExportTraceServiceRequest
	select {
	case req = <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("no OTLP export received")
	}
	if len(req.ResourceSpans) == 0 {
		t.Fatal("export carried no resource spans")
	}
	rs := req.ResourceSpans[0]
	var sawServiceName bool
	for _, attr := range rs.GetResource().GetAttributes() {
		if attr.GetKey() == "service.name" && attr.GetValue().GetStringValue() == "hakase" {
			sawServiceName = true
		}
	}
	if !sawServiceName {
		t.Error("resource missing service.name=hakase")
	}

	names := map[string]bool{}
	for _, ss := range rs.GetScopeSpans() {
		for _, sp := range ss.GetSpans() {
			names[sp.GetName()] = true
		}
	}
	if !names["export-check"] {
		t.Errorf("exported spans %v missing export-check", names)
	}
}

// TestInstallSampleRatioZero pins that a 0 ratio drops root spans.
func TestInstallSampleRatioZero(t *testing.T) {
	restoreProvider(t)
	shutdown, err := Install(Options{Enabled: true, Endpoint: "http://127.0.0.1:1", SampleRatio: 0})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	defer shutdown()

	_, span := Start(context.Background(), "unsampled")
	defer span.End()
	if span.IsRecording() {
		t.Fatal("root span recorded despite sample_ratio 0")
	}
}

// TestInstallBadEndpointFailsLoud pins the fail-loud rule for a malformed
// explicit endpoint.
func TestInstallBadEndpointFailsLoud(t *testing.T) {
	if _, err := Install(Options{Enabled: true, Endpoint: "://bad"}); err == nil {
		t.Fatal("expected an error for a malformed endpoint URL, got none")
	} else if !strings.Contains(err.Error(), "tracing") {
		t.Errorf("error should be prefixed with tracing, got: %v", err)
	}
}

// attrMap flattens OTLP key-value attributes.
func attrMap(t *testing.T, attrs []*commonpb.KeyValue) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, kv := range attrs {
		m[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	return m
}
