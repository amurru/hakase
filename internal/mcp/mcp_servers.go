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
	"context"
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

	// Tool-list cache. Tools() runs before EVERY model call, and the web
	// search fallback probes the manager a second time per call, so a healthy
	// server was paying a connect+list round trip twice per turn - a process
	// spawn for stdio, a network round trip for http. The tool list of a
	// connected server is near-static, so cache it briefly. A TTL rather
	// than invalidation-on-change because the SDK exposes no change hook.
	//
	// Successes only. The failure path is the cooldown gate's job, and
	// caching an error here would block recovery: Tools() would keep
	// reporting the stale error, the caller would re-arm the cooldown on
	// every call, and a server that came back would stay "failed" until
	// the TTL expired.
	cachedTools     []tool.Tool
	cachedToolsAt   time.Time
	toolsCachedOnce bool
}

// Failure-cooldown backoff bounds how often a dead MCP server is re-dialled.
// Vars, not consts, so tests can shrink the window and exercise the
// slow-failure path; see TestCooldownSurvivesASlowFailure.
var (
	failureCooldownBase = 30 * time.Second
	failureCooldownMax  = 5 * time.Minute
)

// toolListTTL bounds how stale a cached tool list may be. Short enough that
// a server restarted with new tools is picked up promptly in an interactive
// session, long enough that a multi-turn run does not re-list per turn.
const toolListTTL = 60 * time.Second

// defaultToolsListBudget bounds the TOTAL time spent listing one server's
// tools. It exists because timeout_ms is a per-request bound and the go-sdk
// retries a failed initialize up to 5 times: a server that is down cost
// 5 x timeout_ms on the critical path of every model call. A trace
// (logs/hakase-debug-20260927T151220) caught one taking 77s, twice in a single
// turn, for 155s of a 191s one-line answer.
//
// A healthy server's initialize + tools/list is well under a second, so this
// is generous for real endpoints while cutting an unreachable one by an order
// of magnitude. An explicit larger timeout_ms is honoured rather than
// truncated, so a deliberately slow server still works.
//
// A var so tests can shrink it; production never reassigns it.
var defaultToolsListBudget = 6 * time.Second

// toolsListBudget returns the total listing budget for this server.
func (ms *managedServer) toolsListBudget() time.Duration {
	if ms.cfg != nil && ms.cfg.TimeoutMs > 0 {
		if d := time.Duration(ms.cfg.TimeoutMs)*time.Millisecond + 2*time.Second; d > defaultToolsListBudget {
			return d
		}
	}
	return defaultToolsListBudget
}

// boundedReadonlyContext caps the deadline of a ReadonlyContext while keeping
// its ADK-specific accessors. ReadonlyContext embeds context.Context, so
// overriding the deadline trio is enough to bound a call without losing
// UserContent/InvocationID/ReadonlyState.
type boundedReadonlyContext struct {
	agent.ReadonlyContext
	ctx context.Context
}

func (c boundedReadonlyContext) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c boundedReadonlyContext) Done() <-chan struct{}       { return c.ctx.Done() }
func (c boundedReadonlyContext) Err() error                  { return c.ctx.Err() }

// toolsCached returns the server's tool list, hitting the SDK only when there
// is no cached copy or it is older than toolListTTL. Only successful lists are
// cached; an error always re-probes, leaving retry pacing to the cooldown gate.
//
// The SDK call runs behind a hard deadline because it sits on the critical path
// of every model call and cannot be trusted to return promptly. A context
// deadline alone is not enough: it does bound the HTTP work (a black-hole
// server's requests are cancelled on schedule), but the go-sdk then sleeps
// through a retry backoff that never observes the context, adding a further
// ~5s with no traffic at all. So the call is also raced against the budget and
// abandoned when it expires.
//
// Abandoning leaks one goroutine per dead server until the SDK gives up on its
// own (a few seconds). That is a deliberate trade: the failure cooldown means
// this happens a handful of times per dead server per session, not per turn,
// and a hard bound on the critical path is worth more than tidiness. The
// result channel is buffered so the abandoned goroutine can always exit.
func (ms *managedServer) toolsCached(ctx agent.ReadonlyContext, now time.Time) ([]tool.Tool, error) {
	ms.mu.Lock()
	if ms.toolsCachedOnce && now.Sub(ms.cachedToolsAt) < toolListTTL {
		tools := ms.cachedTools
		ms.mu.Unlock()
		return tools, nil
	}
	ms.mu.Unlock()

	dctx, cancel := context.WithTimeout(ctx, ms.toolsListBudget())
	defer cancel()

	type listResult struct {
		tools []tool.Tool
		err   error
	}
	res := make(chan listResult, 1)
	go func() {
		tools, err := ms.toolset.Tools(boundedReadonlyContext{ReadonlyContext: ctx, ctx: dctx})
		res <- listResult{tools: tools, err: err}
	}()

	select {
	case r := <-res:
		if r.err != nil {
			// Do not cache the failure, and drop any stale success so a
			// server that has gone bad cannot keep serving a cached list.
			ms.mu.Lock()
			ms.toolsCachedOnce = false
			ms.cachedTools = nil
			ms.mu.Unlock()
			return nil, r.err
		}
		ms.mu.Lock()
		ms.cachedTools, ms.cachedToolsAt = r.tools, now
		ms.toolsCachedOnce = true
		ms.mu.Unlock()
		return r.tools, nil

	case <-dctx.Done():
		ms.mu.Lock()
		ms.toolsCachedOnce = false
		ms.cachedTools = nil
		ms.mu.Unlock()
		return nil, dctx.Err()
	}
}

// invalidateToolCache drops the cached tool list, so the next Tools() call
// re-lists. Called when a server's configuration or connection changes.
func (ms *managedServer) invalidateToolCache() {
	ms.mu.Lock()
	ms.toolsCachedOnce = false
	ms.cachedTools = nil
	ms.mu.Unlock()
}

// startCooldown arms the skip window after a failed attempt: the next
// cooldown window doubles per consecutive failure, capped at
// failureCooldownMax, so a dead server is re-probed roughly once per window
// (recovery is detected on the first probe after expiry) instead of on every
// model call.
//
// failedAt is the time the failure was OBSERVED and attemptStarted is when the
// dial began. Using the attempt's start time for both is what made the backoff
// useless: Tools() captured one `now` before the dial loop, so for a server
// that takes longer to fail than the window being armed, failedUntil was
// already in the past by the time it was written, cooldownRemaining returned 0,
// and the next model call re-dialled and paid the full cost again. A trace
// (logs/hakase-debug-20260927T151220) caught this: a server that took 77s to
// fail was dialled twice in one turn, 155s of a 191s one-line answer.
//
// The window is also floored at the attempt's own duration, so a server that
// reliably takes 77s to fail is never re-probed sooner than that.
func (ms *managedServer) startCooldown(failedAt, attemptStarted time.Time) {
	shift := ms.consecFails
	if shift > 4 {
		shift = 4
	}
	cool := failureCooldownBase << shift
	if cool > failureCooldownMax {
		cool = failureCooldownMax
	}
	if d := failedAt.Sub(attemptStarted); d > cool {
		cool = d
	}
	ms.mu.Lock()
	ms.consecFails++
	ms.failedUntil = failedAt.Add(cool)
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
	// Drop cached OAuth handlers for servers that vanished or changed shape;
	// stale token sources would keep authorizing against the old config.
	oauthFingerprints := map[string]string{}
	for name, srvCfg := range reg.Servers {
		if srvCfg.OAuth != nil {
			oauthFingerprints[name] = oauthHandlerFingerprint(srvCfg.URL, srvCfg.OAuth)
		}
	}
	dropStaleOAuthHandlers(oauthFingerprints)
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

// rawTools fetches all post-filter tools from all connected MCP servers.
func (m *MCPServerManager) rawTools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
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
		// keep rendering while a server is unreachable. The attempt start is
		// captured per server, not once for the whole loop, so a server that
		// follows a slow one is not credited with the earlier server's dial.
		attemptStarted := time.Now()
		tsTools, err := ms.toolsCached(ctx, attemptStarted)
		if err != nil {
			ms.startCooldown(time.Now(), attemptStarted)
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

// Tools implements tool.Toolset. Returns every enabled server's tools,
// namespaced as mcp_<server>_<tool> and filtered by per-server include/exclude
// lists. A failed server is skipped (logged + status recorded), never an error.
// If total post-filter tools > budget and gateway is enabled, returns gateway toolset instead.
func (m *MCPServerManager) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	raw, err := m.rawTools(ctx)
	if err != nil {
		return nil, nil
	}

	if m.cfg != nil {
		gwCfg := m.cfg.MCPServers.Gateway
		if gwCfg.Enabled {
			budget := gwCfg.Budget
			if budget <= 0 {
				budget = 40
			}
			if len(raw) > budget {
				gt := newGatewayToolset(m, gwCfg.HotTools)
				return gt.gatewayTools(ctx, raw)
			}
		}
	}

	return raw, nil
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
	// The whole point of this call is to re-run connect and tools/list, so
	// the tool-list cache must not satisfy the next Tools() with a stale copy.
	ms.invalidateToolCache()
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
		transport := &mcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: client}
		if cfg.OAuth != nil {
			handler, err := oauthHandlerFor(name, cfg.URL, cfg.OAuth, mcpHTTPTimeout(cfg.TimeoutMs))
			if err != nil {
				return nil, err
			}
			transport.OAuthHandler = handler
		}
		return mcptoolset.New(mcptoolset.Config{
			Client:    newElicitingClient(name),
			Transport: transport,
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
			Client:    newElicitingClient(name),
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

// ServerDiagnostic is one server's measured reachability, as reported by
// Diagnose. Unlike MCPServerStatus it carries the timings, because "is it up"
// is not the question that matters - "how long does Tools() block on it" is,
// since ADK calls Tools() before every model call.
type ServerDiagnostic struct {
	Name      string
	Type      string
	Transport string
	Disabled  bool
	ToolCount int
	OK        bool
	Error     string

	// Dial is the wall time a fresh connect + tools/list took. It bypasses
	// the tool-list cache and the failure cooldown so it reports the real
	// cost rather than a warm reading.
	Dial time.Duration
	// Budget is the ceiling the dial was held to (toolsListBudget).
	Budget time.Duration
	// Cached reports that a healthy result was served from the tool-list
	// cache instead of re-dialled, which is the steady state for a working
	// server.
	Cached bool
}

// StatusWord is a short human label for the server's state, for CLI output.
func (d ServerDiagnostic) StatusWord() string {
	switch {
	case d.Disabled:
		return "disabled"
	case !d.OK:
		return "UNREACHABLE"
	case d.Cached:
		return "ok (cached)"
	default:
		return "ok"
	}
}

// Diagnose measures every configured server once and reports how long each
// dial took, bypassing the tool-list cache and the failure cooldown. It exists
// because a trace (logs/hakase-debug-20260927T151220) showed a single
// unreachable server consuming 155s of a 191s turn, and nothing in the UI
// distinguished "connected slowly" from "unreachable".
//
// Servers are probed sequentially and each is bounded by its list budget, so
// the whole call is bounded by len(servers) * budget.
func (m *MCPServerManager) Diagnose(ctx agent.ReadonlyContext) []ServerDiagnostic {
	m.mu.Lock()
	servers := make([]*managedServer, 0, len(m.servers))
	for _, ms := range m.servers {
		servers = append(servers, ms)
	}
	m.mu.Unlock()
	sort.Slice(servers, func(i, j int) bool { return servers[i].name < servers[j].name })

	out := make([]ServerDiagnostic, 0, len(servers))
	for _, ms := range servers {
		d := ServerDiagnostic{
			Name:      ms.name,
			Transport: serverTransportLabel(ms.cfg),
			Disabled:  ms.cfg != nil && ms.cfg.Disabled,
			Budget:    ms.toolsListBudget(),
		}
		if ms.cfg != nil {
			d.Type = ms.cfg.Type
		}
		switch {
		case d.Disabled:
			out = append(out, d)
			continue
		case ms.toolset == nil:
			d.Error = "failed to build toolset (check the command or URL in config)"
			out = append(out, d)
			continue
		}

		// Warm check first: if a valid cached list is already there, the
		// steady-state cost of this server is zero and re-dialling would only
		// measure the network again.
		ms.mu.Lock()
		warm := ms.toolsCachedOnce && time.Since(ms.cachedToolsAt) < toolListTTL
		cachedTools := ms.cachedTools
		ms.mu.Unlock()
		if warm {
			d.OK, d.Cached, d.ToolCount = true, true, len(cachedTools)
			out = append(out, d)
			continue
		}

		start := time.Now()
		tools, err := ms.toolsCached(ctx, start)
		d.Dial = time.Since(start)
		if err != nil {
			d.Error = err.Error()
		} else {
			d.OK, d.ToolCount = true, len(tools)
		}
		out = append(out, d)
	}
	return out
}

// serverTransportLabel renders a human-readable endpoint for diagnostics.
func serverTransportLabel(cfg *config.MCPServerConfig) string {
	if cfg == nil {
		return ""
	}
	if cfg.URL != "" {
		return cfg.URL
	}
	if len(cfg.Command) > 0 {
		return strings.Join(cfg.Command, " ")
	}
	return ""
}
