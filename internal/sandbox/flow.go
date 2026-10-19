// flow.go - Flow Guard pre-dispatch label resolution for sandbox gates.
//
// FlowLabelFunc resolves the flow label for a call context (nil = feature
// off: callers keep exact v1 behavior). Main wires it to the session
// registry via interfaces.SessionIDFromCtx; production gates fall back to
// the start label when no session or no label is registered.
package sandbox

import (
	"context"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/session"
)

// FlowLabelFunc is set by main to resolve the Flow Guard label for a call
// context. Nil = flow gates off (callers keep v1 behavior exactly).
var FlowLabelFunc func(ctx context.Context) *permissions.SessionLabel

// flowLabelFromCtx resolves the label for ctx: the wired func when set,
// else nil (caller keeps v1 behavior). A registered session without a
// label yields the start label; an empty session ID does the same.
func flowLabelFromCtx(ctx context.Context) *permissions.SessionLabel {
	if FlowLabelFunc != nil {
		if l := FlowLabelFunc(ctx); l != nil {
			cp := *l
			return &cp
		}
		return nil
	}
	// Default wiring (no func set): resolve via the session registry so
	// out-of-the-box runs still enforce narrowed labels. No session or
	// no label = start label (v1 parity).
	if ctx != nil {
		if id := interfaces.SessionIDFromCtx(ctx); id != "" {
			if l := session.DefaultRegistry.Get(id); l != nil {
				return l
			}
		}
	}
	l := permissions.StartLabel()
	return &l
}
