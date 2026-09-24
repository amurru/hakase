package tracing

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Run span statuses — the same vocabulary the execution canvas uses
// (interfaces/graph.go GraphStatus*); kept as pinned literals so this leaf
// package does not import interfaces.
const (
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusTimedOut  = "timed_out"
)

// Pinned attribute keys. GenAI semconv are Development status (moved to
// open-telemetry/semantic-conventions-genai, June 2026), so keys are literal
// constants here: "pin attributes internally, map at export time" (issue
// #18). gen_ai.conversation.id is the one registry key we emit verbatim;
// everything else is hakase-namespaced.
const (
	attrTransport   = attribute.Key("hakase.transport")
	attrSessionID   = attribute.Key("hakase.session.id")
	attrTaskID      = attribute.Key("hakase.task.id")
	attrProject     = attribute.Key("hakase.project")
	attrProvider    = attribute.Key("hakase.provider")
	attrModel       = attribute.Key("gen_ai.request.model")
	attrConvID      = attribute.Key("gen_ai.conversation.id")
	attrStatus      = attribute.Key("hakase.run.status")
	attrError       = attribute.Key("error.message")
	attrRetrievalN  = attribute.Key("hakase.retrieval.results")
	attrOperationNm = attribute.Key("gen_ai.operation.name")
)

// runSpanName is the correlation root every transport run creates; ADK's
// invoke_agent / generate_content / execute_tool spans hang beneath it.
const runSpanName = "hakase.run"

// RetrievalOperation is the gen_ai.operation.name value for knowledge recall
// spans, and RetrievalSpanName the span name (spec OT-005).
const (
	RetrievalOperation = "retrieval"
	RetrievalSpanName  = "retrieval knowledge"
)

// StartRetrieval starts a knowledge retrieval span (gen_ai.operation.name =
// retrieval). Call EndRetrieval when done — typically in a defer — to record
// the outcome and result count.
func StartRetrieval(ctx context.Context) (context.Context, trace.Span) {
	return Start(ctx, RetrievalSpanName, attrOperationNm.String(RetrievalOperation))
}

// EndRetrieval records the result count and error outcome on a retrieval
// span and closes it.
func EndRetrieval(span trace.Span, err error, results int) {
	if span == nil {
		return
	}
	span.SetAttributes(attrRetrievalN.Int(results))
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// RunParams describes one user-visible agent run for the correlation root
// span. Empty fields are omitted from the span.
type RunParams struct {
	// Transport is the surface the run came in on: web, telegram, tui, cron.
	Transport string
	// SessionID is the hakase session id ("" for TUI/cron, which have none).
	SessionID string
	// TaskID is the run's task id (doubles as the ADK session id).
	TaskID string
	// Project is the registered project name the session is bound to.
	Project string
	// Provider and Model identify the configured backend.
	Provider, Model string
	// Extra adds literal-keyed attributes (e.g. hakase.cron.job).
	Extra map[string]string
}

// Run is the correlation root span of one run. Nil-safe: End on a zero Run
// is a no-op, so call sites carry no enabled branches.
type Run struct {
	span trace.Span
}

// RunSpan starts the run's correlation root span and returns the ctx to
// derive the run's work context from, so every nested ADK span shares the
// trace.
func RunSpan(ctx context.Context, p RunParams) (context.Context, *Run) {
	attrs := make([]attribute.KeyValue, 0, 7+len(p.Extra))
	if p.Transport != "" {
		attrs = append(attrs, attrTransport.String(p.Transport))
	}
	if p.SessionID != "" {
		attrs = append(attrs, attrSessionID.String(p.SessionID), attrConvID.String(p.SessionID))
	}
	if p.TaskID != "" {
		attrs = append(attrs, attrTaskID.String(p.TaskID))
	}
	if p.Project != "" {
		attrs = append(attrs, attrProject.String(p.Project))
	}
	if p.Provider != "" {
		attrs = append(attrs, attrProvider.String(p.Provider))
	}
	if p.Model != "" {
		attrs = append(attrs, attrModel.String(p.Model))
	}
	for k, v := range p.Extra {
		attrs = append(attrs, attribute.String(k, v))
	}
	ctx, span := Tracer().Start(ctx, runSpanName,
		trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(attrs...))
	return ctx, &Run{span: span}
}

// End closes the run span, mapping the canvas status vocabulary onto span
// status: completed → Ok, failed/timed_out (or anything else) → Error with
// errMsg as the status description.
func (r *Run) End(status, errMsg string) {
	if r == nil || r.span == nil {
		return
	}
	if status == StatusCompleted {
		r.span.SetStatus(codes.Ok, "")
	} else {
		r.span.SetStatus(codes.Error, errMsg)
		r.span.SetAttributes(attrStatus.String(status))
		if errMsg != "" {
			r.span.SetAttributes(attrError.String(errMsg))
		}
	}
	r.span.End()
}
