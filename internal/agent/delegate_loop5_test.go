// delegate_loop5_test.go - loop 5 M3: the handler's post-run entry lands
// under the merged label and the next call hits without re-running.
package agent

import (
	"context"
	"fmt"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"

	"google.golang.org/adk/v2/agent"
)

// delegLoop5Ctx is a minimal agent.Context whose ADK session id is the
// task id (resolved to the hakase session via RegisterTaskSession).
type delegLoop5Ctx struct {
	agent.ContextMock
	taskID string
}

func (c *delegLoop5Ctx) SessionID() string { return c.taskID }

func TestDelegateHandlerPostRunMergedEntryHits(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "ask"})
	sess := "sess-loop5-deleg-handler"
	hakasesession.DefaultRegistry.Import(sess, permissions.StartLabel(), nil)
	t.Cleanup(func() { hakasesession.DefaultRegistry.Set(sess, permissions.StartLabel()) })

	taskID := "task-loop5-deleg-handler"
	interfaces.RegisterTaskSession(taskID, sess)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	ctx := &delegLoop5Ctx{taskID: taskID}

	calls := 0
	saved := delegateSubRunOverride
	delegateSubRunOverride = func(runCtx context.Context, goal string) (string, []string, error) {
		calls++
		// Simulate a tainted child run: fold suspicious trust into the
		// child state like a real tool callback would.
		if st := DelegateFlowStateFromCtx(runCtx); st != nil {
			st.mu.Lock()
			next, _ := permissions.FoldLabel(st.label, permissions.FlowDelta{Trust: "suspicious"})
			st.label = next
			st.mu.Unlock()
		}
		return "fake summary", nil, nil
	}
	t.Cleanup(func() { delegateSubRunOverride = saved })
	// Cleanup cache entries for this goal (test hygiene L5: no cross-test
	// hits through the shared cache).
	norm := normalizeDelegationGoal("loop5 handler merged probe")
	t.Cleanup(func() {
		delegationCache.mu.Lock()
		for k := range delegationCache.m {
			if len(k) >= len(norm) && k[:len(norm)] == norm {
				delete(delegationCache.m, k)
			}
		}
		delegationCache.mu.Unlock()
	})

	input := DelegateTaskArgs{Goal: "loop5 handler merged probe"}
	first, err := delegateTaskHandler(ctx, input)
	if err != nil {
		t.Fatalf("first handler call: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fake runner calls = %d, want 1", calls)
	}
	if first.Status != "completed" || first.Summary != "fake summary" {
		t.Fatalf("first result = %+v, want completed fake summary", first)
	}
	parent := hakasesession.DefaultRegistry.Get(sess)
	if parent == nil || parent.Trust != permissions.TrustSuspicious {
		t.Fatalf("parent after handler = %+v, want suspicious (merged)", parent)
	}
	// The post-run entry lands under the merged label (goal+session+
	// terminal taint), not just the pre-run key.
	postKey := delegationCacheKeyFor(norm, sess, parent)
	e, ok := delegationCacheEntryFor(postKey)
	if !ok || !e.hasLbl {
		t.Fatalf("post-run entry missing under merged label: %v", ok)
	}
	if e.label.Trust != permissions.TrustSuspicious {
		t.Fatalf("post-run entry label = %+v, want suspicious", e.label)
	}
	// The next call at the narrowed label hits without re-running.
	second, err := delegateTaskHandler(ctx, input)
	if err != nil {
		t.Fatalf("second handler call: %v", err)
	}
	if calls != 1 {
		t.Fatalf("second call re-ran (calls=%d), want cache hit", calls)
	}
	if second.TaskID != first.TaskID || second.Summary != first.Summary {
		t.Fatalf("second = %+v, want cached %+v", second, first)
	}
	var _ = fmt.Sprint
}
