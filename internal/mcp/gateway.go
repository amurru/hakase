// gateway.go - MCP Gateway meta-tools and budget auto-degradation (spec MG-006).
package mcp

import (
	"amurru/hakase/internal/util"
	"encoding/json"
	"fmt"
	"strings"

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

func matchTool(t tool.Tool, server, toolName string) bool {
	tName := t.Name()
	if tName == toolName {
		return true
	}
	if server != "" && tName == MCPToolName(server, toolName) {
		return true
	}
	if server == "" && strings.HasSuffix(tName, "_"+toolName) {
		return true
	}
	return false
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
			for _, t := range tools {
				if matchTool(t, "", args.Name) {
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
				}
			}
			return DescribeToolResult{}, fmt.Errorf("tool %q not found", args.Name)
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
			for _, t := range tools {
				if matchTool(t, args.Server, args.Tool) {
					if runner, ok := t.(interface {
						Run(ctx agent.Context, args any) (map[string]any, error)
					}); ok {
						return runner.Run(ctx, args.Arguments)
					}
					return nil, fmt.Errorf("tool %q does not support execution", args.Tool)
				}
			}
			return nil, fmt.Errorf("mcp tool %q not found", args.Tool)
		},
	)
	if err != nil {
		return nil, err
	}

	out := []tool.Tool{searchTool, describeTool, callTool}

	// Hot tools passthrough
	if len(g.hotTools) > 0 {
		for _, ht := range g.hotTools {
			for _, t := range rawTools {
				if matchTool(t, "", ht) {
					out = append(out, t)
					break
				}
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
