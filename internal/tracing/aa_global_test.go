// aa_global_test.go - pins the global-delegation property the whole design
// rests on (docs/otel-tracing/spec.md): tracers obtained from otel's global
// BEFORE Install — as ADK does at package init — start recording the moment
// a real provider is installed. The file name sorts first on purpose: the
// test must see the pristine delegating global (its delegate is permanent
// once set, so it can only be exercised once per process).
package tracing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"google.golang.org/protobuf/proto"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// TestGlobalDelegationEarlyTracer must be the first test in the package.
func TestGlobalDelegationEarlyTracer(t *testing.T) {
	restoreProvider(t)

	received := make(chan *collectortracepb.ExportTraceServiceRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	// Captured before Install, mirroring ADK's package-init tracers.
	early := otel.Tracer("early-capture")

	shutdown, err := Install(Options{Enabled: true, Endpoint: srv.URL, SampleRatio: 1, Version: "test"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	_, span := early.Start(context.Background(), "early-span")
	if !span.IsRecording() {
		t.Fatal("tracer captured before Install is not recording (global delegation broken)")
	}
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
	var sawEarly bool
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				if sp.GetName() == "early-span" {
					sawEarly = true
				}
			}
		}
	}
	if !sawEarly {
		t.Error("early-span not exported; the pre-Install tracer did not reach the installed provider")
	}
}
