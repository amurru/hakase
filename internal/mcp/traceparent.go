// traceparent.go - W3C trace-context propagation into outbound MCP requests
// (SEP-414, issue #18): when a trace span is active, tool calls and other
// requests carry `_meta["traceparent"]`, so a tracing MCP server joins
// hakase's trace instead of starting its own.
package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"amurru/hakase/internal/tracing"
)

// traceparentMiddleware injects `_meta["traceparent"]` into every outbound
// request while the calling ctx carries a valid span context. Installed on
// every managed-server client (newElicitingClient); with tracing disabled
// the ctx holds no span context, the read returns "" and the request passes
// through untouched — unconditional installation is free. An existing
// traceparent (e.g. from an upstream caller) is never overwritten.
func traceparentMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			params := req.GetParams()
			if params != nil {
				if tp := tracing.Traceparent(ctx); tp != "" {
					meta := params.GetMeta()
					if meta == nil {
						meta = mcp.Meta{}
						params.SetMeta(meta)
					}
					if _, exists := meta["traceparent"]; !exists {
						meta["traceparent"] = tp
					}
				}
			}
			return next(ctx, method, req)
		}
	}
}
