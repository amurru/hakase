package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/trace"
)

// Traceparent renders the W3C traceparent header value
// ("00-<32hex trace id>-<16 hex span id>-<flags>") for the span context in
// ctx, or "" when there is none (no span, or a no-op span with an invalid
// zero context — the tracing-disabled case). The sampled flag is taken from
// the context as-is: unsampled contexts still propagate, so downstream
// servers keep the trace id even when hakase's sampler said no.
func Traceparent(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	// TraceFlags is a fmt.Stringer, so %02x would hex-encode its "01" string
	// ("3031") — narrow to a plain byte first.
	return fmt.Sprintf("00-%s-%s-%02x", sc.TraceID(), sc.SpanID(), byte(sc.TraceFlags()&trace.FlagsSampled))
}
