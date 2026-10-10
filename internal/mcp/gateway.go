// gateway.go - MCP Gateway meta-tools and budget auto-degradation (spec MG-006).
package mcp

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/util"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

type gatewayToolset struct {
	mgr      *MCPServerManager
	hotTools []string
}

func newGatewayToolset(mgr *MCPServerManager, hotTools []string) *gatewayToolset {
	return &gatewayToolset{
		mgr:      mgr,
		hotTools: hotTools,
	}
}

func (g *gatewayToolset) Name() string {
	return "mcp_gateway"
}

func (g *gatewayToolset) Description() string {
	return "MCP Gateway meta-tools for searching, describing, and calling MCP tools."
}

type SearchToolsArgs struct {
	Query string `json:"query,omitempty" doc:"Substring or keyword to search in tool names and descriptions"`
	Limit int    `json:"limit,omitempty" doc:"Maximum number of tools to return (default 20)"`
}

type ToolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type SearchToolsResult struct {
	Count int           `json:"count"`
	Tools []ToolSummary `json:"tools"`
}

type DescribeToolArgs struct {
	Name string `json:"name" doc:"Name of the tool to describe (e.g. 'mcp_github_create_issue' or 'create_issue')"`
}

type DescribeToolResult struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type CallToolArgs struct {
	Server    string         `json:"server,omitempty" doc:"MCP server name (optional if tool is namespaced like mcp_<server>_<tool>)"`
	Tool      string         `json:"tool" doc:"Tool name to call (e.g. 'search' or namespaced 'mcp_github_search')"`
	Arguments map[string]any `json:"arguments,omitempty" doc:"Arguments map to pass to the tool"`
}

// gatewayPreToolUseCheck mirrors hooks.Runner.CheckPreToolUse without
// importing internal/agent or internal/hooks (see elicitation.go: the manager
// stays decoupled behind a factory). Wired at process startup from
// cmd/hakase after SetupRunner builds deps.HooksRunner; nil means no hooks.
var gatewayPreToolUse = struct {
	sync.RWMutex
	check func(ctx context.Context, toolName string, args map[string]any) (bool, map[string]any)
}{}

// SetGatewayPreToolUseCheck installs the PreToolUse enforcement for nested
// gateway calls. Call once at startup with deps.HooksRunner.CheckPreToolUse.
func SetGatewayPreToolUseCheck(check func(ctx context.Context, toolName string, args map[string]any) (bool, map[string]any)) {
	gatewayPreToolUse.Lock()
	defer gatewayPreToolUse.Unlock()
	gatewayPreToolUse.check = check
}

func gatewayCheckPreToolUse(ctx context.Context, toolName string, args map[string]any) (bool, map[string]any) {
	gatewayPreToolUse.RLock()
	defer gatewayPreToolUse.RUnlock()
	if gatewayPreToolUse.check == nil {
		return false, nil
	}
	return gatewayPreToolUse.check(ctx, toolName, args)
}

// resolveTool finds one tool by bare or namespaced name. Exact qualified
// names win; bare names must match exactly one tool, otherwise the request
// is ambiguous and must name a server. A qualified name that does not belong
// to the requested server never matches.
func resolveTool(tools []tool.Tool, server, toolName string) (tool.Tool, error) {
	if strings.TrimSpace(toolName) == "" {
		return nil, fmt.Errorf("mcp tool %q not found", toolName)
	}
	var exact, matches []tool.Tool
	sanitizedName := config.SanitizeMCPServerName(toolName)
	serverPrefix := ""
	if server != "" {
		serverPrefix = "mcp_" + config.SanitizeMCPServerName(server) + "_"
	}
	for _, t := range tools {
		name := t.Name()
		if name == toolName && (server == "" || strings.HasPrefix(name, serverPrefix)) {
			exact = append(exact, t)
			continue
		}
		if server != "" {
			if name == MCPToolName(server, toolName) {
				matches = append(matches, t)
			}
		} else if strings.HasPrefix(name, "mcp_") && strings.HasSuffix(name, "_"+sanitizedName) {
			matches = append(matches, t)
		}
	}
	if len(exact) > 0 {
		matches = exact
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("mcp tool %q not found", toolName)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, t := range matches {
			names = append(names, t.Name())
		}
		return nil, fmt.Errorf("mcp tool %q is ambiguous; specify server: %s",
			toolName, strings.Join(names, ", "))
	}
}

func (g *gatewayToolset) gatewayTools(ctx agent.ReadonlyContext, rawTools []tool.Tool) ([]tool.Tool, error) {
	searchTool, err := util.NewDocTool(
		functiontool.Config{
			Name:        "mcp_search_tools",
			Description: "Search available MCP tools by keyword or substring match in tool name and description.",
		},
		func(ctx agent.Context, args SearchToolsArgs) (SearchToolsResult, error) {
			tools := rawTools
			if len(tools) == 0 && g.mgr != nil {
				var err error
				tools, err = g.mgr.rawTools(ctx)
				if err != nil {
					return SearchToolsResult{}, err
				}
			}
			q := strings.ToLower(strings.TrimSpace(args.Query))
			limit := args.Limit
			if limit <= 0 {
				limit = 20
			}
			var matched []ToolSummary
			for _, t := range tools {
				name := t.Name()
				desc := ""
				if declGetter, ok := t.(interface {
					Declaration() *genai.FunctionDeclaration
				}); ok {
					if decl := declGetter.Declaration(); decl != nil {
						desc = decl.Description
					}
				}
				if q == "" || strings.Contains(strings.ToLower(name), q) || strings.Contains(strings.ToLower(desc), q) {
					matched = append(matched, ToolSummary{
						Name:        name,
						Description: desc,
					})
					if len(matched) >= limit {
						break
					}
				}
			}
			return SearchToolsResult{
				Count: len(matched),
				Tools: matched,
			}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	describeTool, err := util.NewDocTool(
		functiontool.Config{
			Name:        "mcp_describe_tool",
			Description: "Get detailed schema and parameter descriptions for a specific MCP tool.",
		},
		func(ctx agent.Context, args DescribeToolArgs) (DescribeToolResult, error) {
			tools := rawTools
			if len(tools) == 0 && g.mgr != nil {
				var err error
				tools, err = g.mgr.rawTools(ctx)
				if err != nil {
					return DescribeToolResult{}, err
				}
			}
			t, err := resolveTool(tools, "", args.Name)
			if err != nil {
				return DescribeToolResult{}, err
			}
			res := DescribeToolResult{Name: t.Name()}
			if declGetter, ok := t.(interface {
				Declaration() *genai.FunctionDeclaration
			}); ok {
				if decl := declGetter.Declaration(); decl != nil {
					res.Description = decl.Description
					if decl.Parameters != nil {
						var params map[string]any
						if b, err := json.Marshal(decl.Parameters); err == nil {
							_ = json.Unmarshal(b, &params)
							res.Parameters = params
						}
					}
				}
			}
			return res, nil
		},
	)
	if err != nil {
		return nil, err
	}

	callTool, err := util.NewDocTool(
		functiontool.Config{
			Name:        "mcp_call_tool",
			Description: "Execute an MCP tool by server/tool name with arguments.",
		},
		func(ctx agent.Context, args CallToolArgs) (map[string]any, error) {
			tools := rawTools
			if len(tools) == 0 && g.mgr != nil {
				var err error
				tools, err = g.mgr.rawTools(ctx)
				if err != nil {
					return nil, err
				}
			}
			t, err := resolveTool(tools, args.Server, args.Tool)
			if err != nil {
				return nil, err
			}
			if blocked, result := gatewayCheckPreToolUse(ctx, t.Name(), args.Arguments); blocked {
				return result, nil
			}
			if runner, ok := t.(interface {
				Run(ctx agent.Context, args any) (map[string]any, error)
			}); ok {
				return runner.Run(ctx, args.Arguments)
			}
			return nil, fmt.Errorf("tool %q does not support execution", args.Tool)
		},
	)
	if err != nil {
		return nil, err
	}

	out := []tool.Tool{searchTool, describeTool, callTool}

	// Hot tools passthrough: resolve unambiguously; skip missing or
	// ambiguous entries rather than exposing an arbitrary first match.
	if len(g.hotTools) > 0 {
		for _, ht := range g.hotTools {
			if t, err := resolveTool(rawTools, "", ht); err == nil {
				out = append(out, t)
			}
		}
	}

	return out, nil
}

func (g *gatewayToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	var rawTools []tool.Tool
	if g.mgr != nil {
		var err error
		rawTools, err = g.mgr.rawTools(ctx)
		if err != nil {
			return nil, nil
		}
	}
	return g.gatewayTools(ctx, rawTools)
}
