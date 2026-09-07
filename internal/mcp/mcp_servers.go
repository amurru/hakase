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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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

	mu        sync.Mutex // guards the status and cooldown fields below
	status    string
	err       string
	toolCount int

	// Failure cooldown: after a failed connect/list the server is skipped
	// without dialing until failedUntil, so a dead server costs one attempt
	// per backoff window instead of one per model call. consecFails drives
	// the exponential backoff and is reset on a successful connect.
	failedUntil time.Time
	consecFails int
}

const (
	failureCooldownBase = 30 * time.Second
	failureCooldownMax  = 5 * time.Minute
)

// startCooldown arms the skip window after a failed attempt: the next
// cooldown window doubles per consecutive failure, capped at
// failureCooldownMax, so a dead server is re-probed roughly once per window
// (recovery is detected on the first probe after expiry) instead of on every
// model call.
func (ms *managedServer) startCooldown(now time.Time) {
	shift := ms.consecFails
	if shift > 4 {
		shift = 4
	}
	cool := failureCooldownBase << shift
	if cool > failureCooldownMax {
		cool = failureCooldownMax
	}
	ms.mu.Lock()
	ms.consecFails++
	ms.failedUntil = now.Add(cool)
	ms.mu.Unlock()
}

// clearCooldown removes any armed cooldown (successful connect or manual
// reconnect) so the next Tools() call dials again.
func (ms *managedServer) clearCooldown() {
	ms.mu.Lock()
	ms.consecFails = 0
	ms.failedUntil = time.Time{}
	ms.mu.Unlock()
}

// cooldownRemaining reports how much longer the server must be skipped
// without dialing; 0 means a dial is allowed.
func (ms *managedServer) cooldownRemaining(now time.Time) time.Duration {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if now.Before(ms.failedUntil) {
		return ms.failedUntil.Sub(now)
	}
	return 0
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

	now := time.Now()
	var out []tool.Tool
	for _, ms := range servers {
		if ms.cfg.Disabled {
			ms.setStatus("disabled", "")
			continue
		}
		if ms.toolset == nil {
			continue // failed to build; status already recorded
		}
		// Deterministic health gate: a server that failed recently is
		// skipped without dialing until its cooldown expires. The failure
		// was already logged when it happened, so this stays silent and
		// keeps the per-call cost of a dead server at zero (the web search
		// fallback probes the manager a second time per model call; the
		// gate makes that probe free too).
		if ms.cooldownRemaining(now) > 0 {
			continue
		}
		// The slow connect/list happens OUTSIDE both locks so the TUI can
		// keep rendering while a server is unreachable.
		tsTools, err := ms.toolset.Tools(ctx)
		if err != nil {
			ms.startCooldown(now)
			ms.setStatus("failed", err.Error())
			ms.setToolCount(0)
			if m.log != nil {
				m.log(fmt.Sprintf("mcp: server %q failed: %v", ms.name, err))
			}
			continue
		}
		ms.clearCooldown()
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
	// Manual reconnect is explicit user intent: dial on the next Tools()
	// call regardless of any armed failure cooldown.
	ms.clearCooldown()
	ms.setStatus("idle", "")
	return nil
}

// ServerConfig returns the effective (merged) config for one server.
func (m *MCPServerManager) ServerConfig(name string) (*config.MCPServerConfig, bool) {
	m.mu.Lock()
	ms, ok := m.servers[name]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	copy := *ms.cfg
	return &copy, true
}

// UpsertServer adds a new server or replaces an existing one. The full entry
// is persisted to the user registry (which overrides the project config), any
// prior removal is undone, and the effective registry is reloaded so the
// change applies on the next tool fetch.
func (m *MCPServerManager) UpsertServer(name string, srv *config.MCPServerConfig) error {
	if name == "" {
		return fmt.Errorf("server name is required")
	}
	if err := srv.Validate(); err != nil {
		return fmt.Errorf("invalid mcp server %q: %w", name, err)
	}
	copy := *srv
	err := config.UpdateMCPUserRegistry(func(reg *config.MCPUserRegistry) error {
		if reg.Servers == nil {
			reg.Servers = make(map[string]*config.MCPServerConfig)
		}
		reg.Servers[name] = &copy
		reg.Removed = removeString(reg.Removed, name)
		reg.Disabled = removeString(reg.Disabled, name)
		return nil
	})
	if err != nil {
		return fmt.Errorf("persisting mcp server: %w", err)
	}
	return m.reload()
}

// RemoveServer deletes a server entirely. For user-registry servers the entry
// is dropped (a project definition of the same name comes back); for
// project-config servers the name is added to the removed list so it stays
// hidden across restarts. Re-adding via UpsertServer undoes the removal. The
// effective registry is reloaded immediately.
func (m *MCPServerManager) RemoveServer(name string) error {
	err := config.UpdateMCPUserRegistry(func(reg *config.MCPUserRegistry) error {
		hadUserEntry := false
		if reg.Servers != nil {
			if _, ok := reg.Servers[name]; ok {
				hadUserEntry = true
				delete(reg.Servers, name)
			}
		}
		// Only hide a project-config server (no user entry) via the removed
		// list. A user override's deletion just restores the project default.
		if !hadUserEntry && !containsString(reg.Removed, name) {
			reg.Removed = append(reg.Removed, name)
		}
		reg.Disabled = removeString(reg.Disabled, name)
		return nil
	})
	if err != nil {
		return fmt.Errorf("persisting mcp server removal: %w", err)
	}
	return m.reload()
}

// mcpHTTPTimeout resolves the per-request timeout for streamable HTTP
// transports: explicit timeout_ms when positive, else 10s. It bounds
// connect + initialize + tools/list so a hanging endpoint fails fast into
// the failure cooldown instead of stalling a model call.
func mcpHTTPTimeout(timeoutMs int) time.Duration {
	if timeoutMs > 0 {
		return time.Duration(timeoutMs) * time.Millisecond
	}
	return 10 * time.Second
}

// buildMCPServerToolset constructs the ADK toolset for one server. stdio
// servers run the configured command with HAKASE_*-scrubbed env plus the
// server's env block; http servers use a streamable HTTP transport with
// optional headers. Connection is lazy: nothing spawns or dials here.
func buildMCPServerToolset(name string, cfg *config.MCPServerConfig) (tool.Toolset, error) {
	switch {
	case cfg.Type == "http" || (cfg.Type == "" && cfg.URL != "" && len(cfg.Command) == 0):
		client := &http.Client{Timeout: mcpHTTPTimeout(cfg.TimeoutMs)}
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
		cmd := &exec.Cmd{Path: argv[0], Args: append([]string(nil), argv...)}
		// Mirror the stdlib os/exec PATH resolution of a bare name: the
		// CommandTransport starts this process itself via cmd.Start, which
		// returns cmd.Err when the lookup failed.
		if filepath.Base(argv[0]) == argv[0] {
			if lp, lerr := exec.LookPath(argv[0]); lp != "" {
				cmd.Path = lp
				if lerr != nil {
					cmd.Err = lerr
				}
			} else if lerr != nil {
				cmd.Err = lerr
			}
		}
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

// containsString reports whether list contains target.
func containsString(list []string, target string) bool {
	return indexOfString(list, target) >= 0
}

// removeString returns a new slice with every occurrence of target removed.
func removeString(list []string, target string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s != target {
			out = append(out, s)
		}
	}
	return out
}
