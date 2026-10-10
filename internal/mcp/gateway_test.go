package mcp

import (
	"amurru/hakase/internal/config"
	"context"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

type mockGatewayTool struct {
	name string
	desc string
}

func (m mockGatewayTool) Name() string        { return m.name }
func (m mockGatewayTool) Description() string { return m.desc }
func (m mockGatewayTool) IsLongRunning() bool { return false }
func (m mockGatewayTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        m.name,
		Description: m.desc,
	}
}
func (m mockGatewayTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	return map[string]any{"status": "ok", "executed": m.name, "args": args}, nil
}

type mockGatewayToolset struct {
	tools []tool.Tool
}

func (m mockGatewayToolset) Name() string { return "mcp" }
func (m mockGatewayToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	return m.tools, nil
}

func TestGatewayOverAndUnderBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var fakeTools []tool.Tool
	for i := 0; i < 5; i++ {
		fakeTools = append(fakeTools, mockGatewayTool{
			name: "tool_" + string(rune('0'+i)),
			desc: "description " + string(rune('0'+i)),
		})
	}

	cfg := &config.Config{
		MCPServers: config.MCPConfig{
			Gateway: config.MCPGatewayConfig{
				Enabled: true,
				Budget:  3, // Budget 3 < 5 tools
			},
			Servers: map[string]*config.MCPServerConfig{
				"s1": {
					Type:    "stdio",
					Command: []string{"echo"},
				},
			},
		},
	}

	mgr := &MCPServerManager{
		cfg: cfg,
		servers: map[string]*managedServer{
			"s1": {
				name:    "s1",
				cfg:     cfg.MCPServers.Servers["s1"],
				status:  "connected",
				toolset: mockGatewayToolset{tools: fakeTools},
			},
		},
	}

	ctx := dummyReadonlyContext{}

	// Over-budget test
	tools, err := mgr.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}

	if len(tools) != 3 {
		t.Fatalf("expected 3 gateway meta-tools over budget, got %d", len(tools))
	}

	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name()] = true
	}

	if !names["mcp_search_tools"] || !names["mcp_describe_tool"] || !names["mcp_call_tool"] {
		t.Fatalf("unexpected meta-tool names over budget: %v", names)
	}

	// Under-budget test (raise budget to 10)
	cfg.MCPServers.Gateway.Budget = 10
	toolsUnder, err := mgr.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools under budget: %v", err)
	}

	if len(toolsUnder) != 5 {
		t.Fatalf("expected 5 flat tools under budget, got %d", len(toolsUnder))
	}
}

func TestGatewayHotToolsPassthrough(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var fakeTools []tool.Tool
	for i := 0; i < 5; i++ {
		fakeTools = append(fakeTools, mockGatewayTool{
			name: "tool_" + string(rune('0'+i)),
			desc: "desc",
		})
	}

	cfg := &config.Config{
		MCPServers: config.MCPConfig{
			Gateway: config.MCPGatewayConfig{
				Enabled:  true,
				Budget:   3,
				HotTools: []string{"tool_1"},
			},
			Servers: map[string]*config.MCPServerConfig{
				"s2": {
					Type:    "stdio",
					Command: []string{"echo"},
				},
			},
		},
	}

	mgr := &MCPServerManager{
		cfg: cfg,
		servers: map[string]*managedServer{
			"s2": {
				name:    "s2",
				cfg:     cfg.MCPServers.Servers["s2"],
				status:  "connected",
				toolset: mockGatewayToolset{tools: fakeTools},
			},
		},
	}

	ctx := dummyReadonlyContext{}
	tools, err := mgr.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}

	// 3 meta-tools + 1 hot tool = 4 tools total
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools (3 meta + 1 hot), got %d", len(tools))
	}

	hasHot := false
	for _, tool := range tools {
		if tool.Name() == "mcp_s2_tool_1" {
			hasHot = true
			break
		}
	}

	if !hasHot {
		t.Fatalf("expected hot tool mcp_s2_tool_1 in returned tool list")
	}
}

func TestGatewayCallToolDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	SetGatewayPreToolUseCheck(nil)

	targetTool := mockGatewayTool{name: "mcp_s1_echo", desc: "echo"}
	gt := newGatewayToolset(nil, nil)

	gwTools, err := gt.gatewayTools(dummyReadonlyContext{Context: context.Background()}, []tool.Tool{targetTool})
	if err != nil {
		t.Fatalf("gatewayTools: %v", err)
	}

	var callTool tool.Tool
	for _, t := range gwTools {
		if t.Name() == "mcp_call_tool" {
			callTool = t
			break
		}
	}

	if callTool == nil {
		t.Fatalf("mcp_call_tool not found in gateway tools")
	}

	if runner, ok := callTool.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	}); ok {
		actx := agent.NewContext(&agent.ContextMock{})
		res, err := runner.Run(actx, map[string]any{
			"server": "s1",
			"tool":   "echo",
			"arguments": map[string]any{
				"msg": "hello gateway",
			},
		})
		if err != nil {
			t.Fatalf("call_tool Run failed: %v", err)
		}
		if res["status"] != "ok" || res["executed"] != "mcp_s1_echo" {
			t.Fatalf("unexpected res: %v", res)
		}
	} else {
		t.Fatalf("callTool does not implement Run")
	}
}

func TestResolveToolAmbiguousAndServerEnforcement(t *testing.T) {
	a := mockGatewayTool{name: "mcp_a_search", desc: "a"}
	b := mockGatewayTool{name: "mcp_b_search", desc: "b"}
	tools := []tool.Tool{a, b}

	if _, err := resolveTool(tools, "", "search"); err == nil {
		t.Fatalf("bare name across servers must be ambiguous")
	}
	got, err := resolveTool(tools, "a", "search")
	if err != nil || got.Name() != "mcp_a_search" {
		t.Fatalf("server-scoped resolve = %v, %v", got, err)
	}
	if _, err := resolveTool(tools, "b", "mcp_a_search"); err == nil {
		t.Fatalf("qualified name of another server must not match")
	}
	got, err = resolveTool(tools, "", "mcp_a_search")
	if err != nil || got.Name() != "mcp_a_search" {
		t.Fatalf("qualified resolve = %v, %v", got, err)
	}
}

type blockingGatewayTool struct {
	mockGatewayTool
	calls *int
}

func (m blockingGatewayTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	*m.calls++
	return m.mockGatewayTool.Run(ctx, args)
}

func findGatewayCallTool(t *testing.T, gt *gatewayToolset, raw []tool.Tool) tool.Tool {
	t.Helper()
	gwTools, err := gt.gatewayTools(dummyReadonlyContext{Context: context.Background()}, raw)
	if err != nil {
		t.Fatalf("gatewayTools: %v", err)
	}
	for _, gwt := range gwTools {
		if gwt.Name() == "mcp_call_tool" {
			return gwt
		}
	}
	t.Fatalf("mcp_call_tool not found")
	return nil
}

func TestGatewayCallToolPreToolUseBlocked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	SetGatewayPreToolUseCheck(nil)
	t.Cleanup(func() { SetGatewayPreToolUseCheck(nil) })

	var calls int
	target := blockingGatewayTool{mockGatewayTool: mockGatewayTool{name: "mcp_s1_echo", desc: "echo"}, calls: &calls}
	callTool := findGatewayCallTool(t, newGatewayToolset(nil, nil), []tool.Tool{target})

	SetGatewayPreToolUseCheck(func(ctx context.Context, toolName string, args map[string]any) (bool, map[string]any) {
		if toolName != "mcp_s1_echo" {
			t.Errorf("PreToolUse got tool %q, want mcp_s1_echo", toolName)
		}
		return true, map[string]any{"blocked_by": "test-hook"}
	})

	runner, ok := callTool.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("callTool does not implement Run")
	}
	actx := agent.NewContext(&agent.ContextMock{})
	res, err := runner.Run(actx, map[string]any{
		"server":    "s1",
		"tool":      "echo",
		"arguments": map[string]any{"msg": "hi"},
	})
	if err != nil {
		t.Fatalf("blocked call must return hook result, got err: %v", err)
	}
	if res["blocked_by"] != "test-hook" {
		t.Fatalf("unexpected res: %v", res)
	}
	if calls != 0 {
		t.Fatalf("blocked tool must not run, calls=%d", calls)
	}
}
