package agent

import (
	"strings"
	"testing"

	"amurru/hakase/internal/config"
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/memory"
	"amurru/hakase/internal/project"

	agent "google.golang.org/adk/v2/agent"
)

// runRememberTool invokes a created tool through the ADK runnable interface
// (the knowledge-tools test pattern).
func runRememberTool(t *testing.T, tl any, args any) map[string]any {
	t.Helper()
	runnable, ok := tl.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("tool %T does not implement Run", tl)
	}
	out, err := runnable.Run(agent.NewContext(&agent.ContextMock{}), args)
	if err != nil {
		t.Fatalf("tool Run: %v", err)
	}
	return out
}

func TestRememberToolsRoundTrip(t *testing.T) {
	isolateHome(t) // notes store lands in the temp HOME
	tools, err := createRememberTools(200)
	if err != nil {
		t.Fatalf("createRememberTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("want 2 tools (remember, forget_memory), got %d", len(tools))
	}

	out := runRememberTool(t, tools[0], map[string]any{
		"category": "user",
		"content":  "  prefers terse answers  ",
	})
	id, _ := out["id"].(string)
	if !strings.HasPrefix(id, "mem_") {
		t.Fatalf("remember returned id %v, want mem_*", out["id"])
	}

	// Update path (id passed): same note, Updated flag set.
	out2 := runRememberTool(t, tools[0], map[string]any{
		"category": "feedback",
		"content":  "prefers even terser answers",
		"id":       id,
	})
	if updated, _ := out2["updated"].(bool); !updated {
		t.Fatalf("update path did not report updated: %v", out2)
	}

	store, err := memory.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	got := store.Get()
	if len(got.Notes) != 1 || got.Notes[0].Category != "feedback" {
		t.Fatalf("store after update = %+v", got.Notes)
	}

	// forget_memory: existing id removes, unknown id is a clean false.
	out3 := runRememberTool(t, tools[1], map[string]any{"id": id})
	if forgotten, _ := out3["forgotten"].(bool); !forgotten {
		t.Fatalf("forget_memory did not remove: %v", out3)
	}
	out4 := runRememberTool(t, tools[1], map[string]any{"id": "mem_missing"})
	if forgotten, _ := out4["forgotten"].(bool); forgotten {
		t.Fatalf("unknown id must report forgotten=false: %v", out4)
	}
	if got := store.Get(); len(got.Notes) != 0 {
		t.Fatalf("store not empty after forget: %+v", got.Notes)
	}
}

func TestRememberToolValidationAndCap(t *testing.T) {
	isolateHome(t)
	tools, err := createRememberTools(1)
	if err != nil {
		t.Fatalf("createRememberTools: %v", err)
	}
	runnable := tools[0].(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	})
	ctx := agent.NewContext(&agent.ContextMock{})

	if _, err := runnable.Run(ctx, map[string]any{"category": "vibes", "content": "x"}); err == nil || !strings.Contains(err.Error(), "unknown category") {
		t.Fatalf("want category error, got %v", err)
	}
	if _, err := runnable.Run(ctx, map[string]any{"category": "user", "content": "ok"}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, err = runnable.Run(ctx, map[string]any{"category": "user", "content": "second"})
	if err == nil || !strings.Contains(err.Error(), "memory is full") {
		t.Fatalf("want full-store error, got %v", err)
	}
}

func TestRememberToolStampsProjectRoot(t *testing.T) {
	isolateHome(t)
	prev := project.CurrentRoot()
	defer project.SetCurrentRoot(prev)
	project.SetCurrentRoot("/tmp/some/project")

	tools, err := createRememberTools(200)
	if err != nil {
		t.Fatalf("createRememberTools: %v", err)
	}
	runRememberTool(t, tools[0], map[string]any{"category": "project", "content": "uses pnpm"})

	store, err := memory.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	got := store.Get()
	if len(got.Notes) != 1 || got.Notes[0].Project != "/tmp/some/project" {
		t.Fatalf("note not stamped with process root: %+v", got.Notes)
	}
}

func TestWireMemoryDisabled(t *testing.T) {
	isolateHome(t)
	hb := hctx.NewHistoryBuilder(nil)
	disabled := false
	tools, err := wireMemory(&config.Config{Memory: config.MemoryConfig{Enabled: &disabled}}, hb)
	if err != nil {
		t.Fatalf("wireMemory disabled: %v", err)
	}
	if tools != nil {
		t.Fatalf("disabled memory must create no tools, got %d", len(tools))
	}
	if hb.MemoryProvider() != nil {
		t.Fatalf("disabled memory must not attach a provider")
	}
}

func TestWireMemoryEnabledAttachesProvider(t *testing.T) {
	isolateHome(t)
	hb := hctx.NewHistoryBuilder(nil)
	tools, err := wireMemory(&config.Config{}, hb) // zero config = enabled, defaults
	if err != nil {
		t.Fatalf("wireMemory enabled: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("enabled memory must attach 2 tools, got %d", len(tools))
	}
	if hb.MemoryProvider() == nil {
		t.Fatalf("enabled memory must attach a provider")
	}

	// The provider renders the block for the process root and stays silent
	// when the store is empty or scoped elsewhere.
	project.SetCurrentRoot("")
	if block := hb.MemoryProvider()(agent.NewContext(&agent.ContextMock{})); block != "" {
		t.Fatalf("empty store must render empty block, got %q", block)
	}
	store, err := memory.OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	if _, err := store.Add("lesson", "visible note", "", 0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := store.Add("project", "scoped elsewhere", "/other/repo", 0); err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	block := hb.MemoryProvider()(agent.NewContext(&agent.ContextMock{}))
	if !strings.Contains(block, "visible note") || strings.Contains(block, "scoped elsewhere") {
		t.Fatalf("provider leaked out-of-scope notes:\n%s", block)
	}
}
