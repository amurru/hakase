// mcp_servers.go - the MCP server runtime manager: builds one ADK mcptoolset
// per configured server, exposes their tools to agents as a single dynamic
// tool.Toolset, tracks per-server status for the /mcp TUI command, and applies
// runtime enable/disable toggles persisted in the user registry.
//
// Resilience contract: ADK's toolProcessor aborts the whole model call when a
// toolset's Tools() returns an error (internal/llminternal/tools_processor.go),
// so Tools() NEVER propagates a per-server failure - a dead server yields no
// tools and its status is surfaced through ListServers instead. ADK re-evaluates
// Tools() at the start of every run, so toggles take effect on the next user
// message without a restart.
package mcp

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/sandbox"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
	"google.golang.org/adk/v2/tool/toolutils"
	"google.golang.org/genai"
)

// MCPManager is the live MCP server manager shared by the TUI (/mcp),
// sub-agent delegation (delegate.go), and headless cron runs. Set by
// setupRunner and cronModelBootstrap; nil when no MCP config is usable.
var MCPManager *MCPServerManager

// MCPServerStatus is a snapshot of one server's runtime state for the /mcp
// TUI view. Status is one of "idle" (not yet connected this run), "connected",
// "failed" (connect or tool-list error), or "disabled".
type MCPServerStatus struct {
	Name      string
	Type      string // "stdio" | "http"
	Transport string // endpoint URL or command line
	Disabled  bool
	ToolCount int
	Status    string
	Error     string
}

// MCPServerManager implements tool.Toolset for all configured MCP servers.
type MCPServerManager struct {
	mu      sync.Mutex // guards servers map
	cfg     *config.Config
	servers map[string]*managedServer
	log     interfaces.LogFunc
}

// managedServer holds one server's toolset plus its live status. cfg and
// toolset are immutable after being installed by reload/Reconnect; status
// fields are guarded by their own mutex so ListServers never blocks on a slow
// MCP connect happening in Tools().
type managedServer struct {
	name    string
	cfg     *config.MCPServerConfig
	toolset tool.Toolset // nil when disabled or the toolset failed to build

	mu        sync.Mutex
	status    string
	err       string
	toolCount int
}

// NewMCPServerManager builds a manager from the effective MCP registry
// (project config + legacy mcp_server_url + user registry).
func NewMCPServerManager(cfg *config.Config, log interfaces.LogFunc) (*MCPServerManager, error) {
	m := &MCPServerManager{cfg: cfg, log: log}
	if err := m.reload(); err != nil {
		return nil, err
	}
	return m, nil
}

// reload rebuilds the effective registry and per-server toolsets from config
// plus the user registry. Called at construction and after every TUI mutation.
func (m *MCPServerManager) reload() error {
	reg, err := config.LoadMCPRegistry(m.cfg)
	if err != nil {
		return err
	}
	servers := make(map[string]*managedServer, len(reg.Servers))
	for name, srvCfg := range reg.Servers {
		ms := &managedServer{name: name, cfg: srvCfg, status: "idle"}
		if !srvCfg.Disabled {
			ts, err := buildMCPServerToolset(name, srvCfg)
			if err != nil {
				ms.status = "failed"
				ms.err = err.Error()
			} else {
				ms.toolset = ts
			}
		} else {
			ms.status = "disabled"
		}
		servers[name] = ms
	}
	m.mu.Lock()
	m.servers = servers
	m.mu.Unlock()
	return nil
}

// Name implements tool.Toolset.
func (m *MCPServerManager) Name() string { return "mcp" }

// Description implements the extended toolset interface (mirrors mcptoolset).
func (m *MCPServerManager) Description() string {
	return "Tools provided by configured MCP servers."
}

// IsLongRunning implements the extended toolset interface (mirrors mcptoolset).
func (m *MCPServerManager) IsLongRunning() bool { return false }

// Tools implements tool.Toolset. Returns every enabled server's tools,
// namespaced as mcp_<server>_<tool> and filtered by per-server include/exclude
// lists. A failed server is skipped (logged + status recorded), never an error.
func (m *MCPServerManager) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	m.mu.Lock()
	servers := make([]*managedServer, 0, len(m.servers))
	for _, ms := range m.servers {
		servers = append(servers, ms)
	}
	m.mu.Unlock()

	// Deterministic order so the tool list is stable across runs.
	sort.Slice(servers, func(i, j int) bool { return servers[i].name < servers[j].name })

	var out []tool.Tool
	for _, ms := range servers {
		if ms.cfg.Disabled {
			ms.setStatus("disabled", "")
			continue
		}
		if ms.toolset == nil {
			continue // failed to build; status already recorded
		}
		// The slow connect/list happens OUTSIDE both locks so the TUI can
		// keep rendering while a server is unreachable.
		tsTools, err := ms.toolset.Tools(ctx)
		if err != nil {
			ms.setStatus("failed", err.Error())
			ms.setToolCount(0)
			if m.log != nil {
				m.log(fmt.Sprintf("mcp: server %q failed: %v", ms.name, err))
			}
			continue
		}
		ms.setStatus("connected", "")
		ms.setToolCount(len(tsTools))
		for _, t := range tsTools {
			nsName := MCPToolName(ms.name, t.Name())
			if !allowsMCPTool(ms.cfg, nsName) {
				continue
			}
			out = append(out, &namedMCPTool{Tool: t, name: nsName})
		}
	}
	return out, nil
}

// ListServers returns the current status of every configured server, sorted by
// name. Never blocks on network I/O.
func (m *MCPServerManager) ListServers() []MCPServerStatus {
	m.mu.Lock()
	servers := make([]*managedServer, 0, len(m.servers))
	for _, ms := range m.servers {
		servers = append(servers, ms)
	}
	m.mu.Unlock()

	sort.Slice(servers, func(i, j int) bool { return servers[i].name < servers[j].name })
	out := make([]MCPServerStatus, 0, len(servers))
	for _, ms := range servers {
		out = append(out, ms.statusSnapshot())
	}
	return out
}

// ServerStatus returns the status of one server.
func (m *MCPServerManager) ServerStatus(name string) (MCPServerStatus, bool) {
	m.mu.Lock()
	ms, ok := m.servers[name]
	m.mu.Unlock()
	if !ok {
		return MCPServerStatus{}, false
	}
	return ms.statusSnapshot(), true
}

// SetDisabled toggles a server at runtime: it persists the toggle to the user
// registry and reloads the effective registry so the change applies on the
// next run. Project-config servers can be disabled this way too - the user
// registry's disabled list overrides the project's enabled default.
func (m *MCPServerManager) SetDisabled(name string, disabled bool) error {
	err := config.UpdateMCPUserRegistry(func(reg *config.MCPUserRegistry) error {
		idx := indexOfString(reg.Disabled, name)
		switch {
		case disabled && idx < 0:
			reg.Disabled = append(reg.Disabled, name)
		case !disabled && idx >= 0:
			reg.Disabled = append(reg.Disabled[:idx], reg.Disabled[idx+1:]...)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("persisting mcp toggle: %w", err)
	}
	return m.reload()
}

// Reconnect clears a server's status so its next tool fetch re-runs connect
// and tools/list. The underlying ADK mcptoolset reconnects lazily with retry
// on every Tools() call, so this is mostly a status reset for the /mcp view.
func (m *MCPServerManager) Reconnect(name string) error {
	m.mu.Lock()
	ms, ok := m.servers[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no mcp server named %q", name)
	}
	if ms.cfg.Disabled {
		return fmt.Errorf("mcp server %q is disabled; enable it first", name)
	}
	ms.setStatus("idle", "")
	return nil
}

// buildMCPServerToolset constructs the ADK toolset for one server. stdio
// servers run the configured command with HAKASE_*-scrubbed env plus the
// server's env block; http servers use a streamable HTTP transport with
// optional headers. Connection is lazy: nothing spawns or dials here.
func buildMCPServerToolset(name string, cfg *config.MCPServerConfig) (tool.Toolset, error) {
	switch {
	case cfg.Type == "http" || (cfg.Type == "" && cfg.URL != "" && len(cfg.Command) == 0):
		client := &http.Client{}
		if len(cfg.Headers) > 0 {
			client.Transport = &headerTransport{headers: config.ExpandEnvMap(cfg.Headers)}
		}
		return mcptoolset.New(mcptoolset.Config{
			Transport: &mcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: client},
		})
	case cfg.Type == "stdio" || len(cfg.Command) > 0:
		argv := make([]string, 0, len(cfg.Command))
		for _, a := range cfg.Command {
			argv = append(argv, config.ExpandEnv(a))
		}
		if len(argv) == 0 || argv[0] == "" {
			return nil, fmt.Errorf("mcp server %q has an empty stdio command", name)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = buildMCPChildEnv(cfg.Env)
		return mcptoolset.New(mcptoolset.Config{
			Transport: &mcp.CommandTransport{Command: cmd},
		})
	default:
		return nil, fmt.Errorf("mcp server %q needs command (stdio) or url (http)", name)
	}
}

// buildMCPChildEnv builds the environment for a stdio MCP child: the parent
// environment with sensitive prefixes scrubbed (same rule as system_exec, so
// HAKASE_* and other provider secrets never leak into MCP children) plus the
// server's configured env block (values env-expanded, so explicit server
// config wins over any scrubbed ambient variable).
func buildMCPChildEnv(serverEnv map[string]string) []string {
	env := sandbox.ScrubEnv(os.Environ())
	for k, v := range config.ExpandEnvMap(serverEnv) {
		env = append(env, k+"="+v)
	}
	return env
}

// headerTransport injects static headers into every request of an HTTP MCP
// transport (the go-sdk's StreamableClientTransport only exposes Endpoint and
// HTTPClient, so headers go through a RoundTripper).
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	if t.base == nil {
		t.base = http.DefaultTransport
	}
	return t.base.RoundTrip(req)
}

// MCPToolName returns the namespaced callable name for an MCP tool:
// mcp_<server>_<tool>, both parts sanitized for provider tool-name rules
// (Gemini-style single underscores; see the MCP design plan).
func MCPToolName(serverName, toolName string) string {
	return "mcp_" + config.SanitizeMCPServerName(serverName) + "_" + config.SanitizeMCPServerName(toolName)
}

// allowsMCPTool applies a server's include/exclude lists to a namespaced tool
// name. Include (when non-empty) is an allow-list; exclude is a deny-list that
// wins over include.
func allowsMCPTool(cfg *config.MCPServerConfig, nsName string) bool {
	if cfg.Tools == nil {
		return true
	}
	if len(cfg.Tools.Include) > 0 {
		allowed := false
		for _, inc := range cfg.Tools.Include {
			if inc == nsName {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	for _, exc := range cfg.Tools.Exclude {
		if exc == nsName {
			return false
		}
	}
	return true
}

// namedMCPTool renames an MCP tool to its namespaced form while delegating
// execution to the underlying tool (which calls the MCP server with the
// original tool name). It mirrors ADK's confirmationTool pattern: embed the
// original tool and override Name/Declaration/ProcessRequest/Run so the
// namespaced name reaches both the model's function schema and the tool
// dispatch map. Run is delegated explicitly (not through the embedded
// tool.Tool interface, which lacks Run) so namedMCPTool satisfies
// toolinternal.FunctionTool and ADK's handleFunctionCalls can dispatch it.
type namedMCPTool struct {
	tool.Tool
	name string
}

func (t *namedMCPTool) Name() string { return t.name }

func (t *namedMCPTool) Declaration() *genai.FunctionDeclaration {
	rt, ok := t.Tool.(interface {
		Declaration() *genai.FunctionDeclaration
	})
	if !ok || rt.Declaration() == nil {
		return nil
	}
	decl := *rt.Declaration()
	decl.Name = t.name
	return &decl
}

func (t *namedMCPTool) ProcessRequest(ctx agent.Context, req *model.LLMRequest) error {
	return toolutils.PackTool(req, t)
}

// Run delegates execution to the embedded MCP tool, satisfying
// toolinternal.FunctionTool so ADK can dispatch function calls.
func (t *namedMCPTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	if ft, ok := t.Tool.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	}); ok {
		return ft.Run(ctx, args)
	}
	return nil, fmt.Errorf("mcp tool %q: underlying tool does not implement Run", t.name)
}

func (ms *managedServer) setStatus(status, errMsg string) {
	ms.mu.Lock()
	ms.status = status
	ms.err = errMsg
	ms.mu.Unlock()
}

func (ms *managedServer) setToolCount(n int) {
	ms.mu.Lock()
	ms.toolCount = n
	ms.mu.Unlock()
}

func (ms *managedServer) statusSnapshot() MCPServerStatus {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	st := MCPServerStatus{
		Name:      ms.name,
		Disabled:  ms.cfg.Disabled,
		ToolCount: ms.toolCount,
		Status:    ms.status,
		Error:     ms.err,
		Transport: ms.cfg.URL,
	}
	switch {
	case ms.cfg.Type != "":
		st.Type = ms.cfg.Type
	case len(ms.cfg.Command) > 0:
		st.Type = "stdio"
	default:
		st.Type = "http"
	}
	if len(ms.cfg.Command) > 0 {
		st.Transport = strings.Join(ms.cfg.Command, " ")
	}
	return st
}

func indexOfString(list []string, target string) int {
	for i, s := range list {
		if s == target {
			return i
		}
	}
	return -1
}
