// Package tracing installs hakase's OpenTelemetry tracing provider and
// exposes the small span surface the run loops need.
//
// Design (docs/otel-tracing/spec.md, issue #18): the ADK runner already
// emits GenAI spans (invoke_agent, generate_content, execute_tool) through
// otel's *global* tracer provider, which is a delegating proxy — tracers it
// handed out before Install start recording the moment a real provider is
// installed. So tracing is wired by global installation, never passed
// around, and staying disabled is simply "never install": the global
// provider remains no-op, there are no exporter goroutines, and no network
// traffic.
//
// GenAI semantic conventions are still Development status, so every
// attribute key this package owns is a pinned string constant here (see
// runspan.go) — a semconv package bump must never silently rename them.
//
// This package is a leaf: it imports otel and the stdlib only, so any
// internal package (config-free) may use it.
package tracing

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationName identifies hakase's own spans in collectors (ADK's
// spans carry their own, gcp.vertex.agent).
const instrumentationName = "github.com/amurru/hakase"

// Options configures Install. Zero Options (Enabled=false) is the disabled
// default: nothing is installed and Shutdown is a no-op.
type Options struct {
	// Enabled turns tracing on. False keeps the global provider no-op.
	Enabled bool
	// Endpoint is the OTLP/HTTP base URL (e.g. http://localhost:4318).
	// Scheme decides TLS: https:// upgrades, http:// stays plaintext.
	Endpoint string
	// Headers are sent verbatim on every export request (vendor auth).
	Headers map[string]string
	// SampleRatio is the root-sampling probability in [0,1]; children
	// follow their parent (ParentBased). 1 samples everything.
	SampleRatio float64
	// Version becomes the service.version resource attribute.
	Version string
}

// Shutdown flushes and releases the provider. Safe to call when tracing is
// disabled.
type Shutdown func()

// Install builds the OTLP/HTTP exporter and installs the global tracer
// provider + W3C TraceContext propagator. When opts.Enabled is false it
// returns a no-op Shutdown without touching any global. Configuration errors
// (malformed endpoint URL) are returned: an explicitly enabled tracing that
// cannot export is a startup config mistake — fail loud (the exporter
// itself only logs parse errors). A collector that is merely down is not an
// error; the exporter starts lazily and export failures are logged by otel
// without ever blocking runs.
//
// Endpoint semantics: a pathless URL (http://localhost:4318) targets the
// collector at /v1/traces; a URL with an explicit path is used verbatim
// (path-shaped vendor endpoints like Langfuse's .../otel).
func Install(opts Options) (Shutdown, error) {
	if !opts.Enabled {
		return func() {}, nil
	}
	httpOpts := []otlptracehttp.Option{otlptracehttp.WithHeaders(opts.Headers)}
	if opts.Endpoint != "" {
		u, err := url.Parse(opts.Endpoint)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("tracing: invalid OTLP endpoint %q (want scheme://host[:port][/path])", opts.Endpoint)
		}
		if u.Path == "" || u.Path == "/" {
			httpOpts = append(httpOpts, otlptracehttp.WithEndpoint(u.Host))
			if u.Scheme != "https" {
				httpOpts = append(httpOpts, otlptracehttp.WithInsecure())
			}
		} else {
			httpOpts = append(httpOpts, otlptracehttp.WithEndpointURL(opts.Endpoint))
		}
	}
	exporter, err := otlptracehttp.New(context.Background(), httpOpts...)
	if err != nil {
		return nil, fmt.Errorf("tracing: OTLP exporter for %q: %w", opts.Endpoint, err)
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		// Schemaless resource: attribute keys are pinned literals (package
		// doc), so no schema URL / translation contract is claimed.
		sdktrace.WithResource(newResource(version)),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(opts.SampleRatio))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(ctx); err != nil {
			log.Printf("tracing: shutdown: %v", err)
		}
	}, nil
}

// newResource builds the service resource. NewSchemaless because the keys we
// pin are not bound to one semconv version (package doc).
func newResource(version string) *resource.Resource {
	return resource.NewSchemaless(
		attribute.String("service.name", "hakase"),
		attribute.String("service.version", version),
	)
}

// Tracer returns hakase's tracer from the global provider. Callers must go
// through this (not cache a Tracer at package init beyond what the global
// proxy already tolerates) so tests can install a recording provider.
func Tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer(instrumentationName)
}

// Start starts a span on hakase's tracer. Thin wrapper so callers do not
// import otel/trace for the common case; returns the span so attributes can
// be added before End.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}
