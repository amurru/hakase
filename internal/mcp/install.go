package mcp

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"amurru/hakase/internal/config"
)

// InstallOptions contains parameters for installing an MCP server.
type InstallOptions struct {
	Ref      string            // registry name or .mcpb path
	Stdio    bool              // force stdio transport
	HTTP     bool              // force http transport
	Pin      string            // version pin
	Scope    string            // "project" or "user"
	AllowEnv map[string]string // env key=value pairs provided at install
	Yes      bool              // skip prompt confirmation
}

// CredentialPlan details required environment variables and their resolution.
type CredentialPlan struct {
	Required []string
	Provided map[string]string
	Missing  []string
}

// PlanCredentials calculates missing credentials given server env requirements and user provided values.
func PlanCredentials(envReq map[string]string, provided map[string]string) CredentialPlan {
	plan := CredentialPlan{
		Provided: make(map[string]string),
	}
	for k, v := range provided {
		plan.Provided[k] = v
	}

	for k, v := range envReq {
		plan.Required = append(plan.Required, k)
		if _, ok := plan.Provided[k]; !ok {
			// Check if ambient env has it
			if envVal, exists := os.LookupEnv(k); exists && envVal != "" {
				plan.Provided[k] = envVal
			} else if v != "" && !strings.HasPrefix(v, "${") {
				plan.Provided[k] = v
			} else {
				plan.Missing = append(plan.Missing, k)
			}
		}
	}
	sort.Strings(plan.Required)
	sort.Strings(plan.Missing)
	return plan
}

// MaxMCPBEntries caps zip entries, MaxMCPBBytes caps total uncompressed output.
const (
	MaxMCPBEntries = 1000
	MaxMCPBBytes   = 50 << 20
	MaxMCPBFile    = 20 << 20
)

var pinRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// dangerousEnvKeys are never persisted into a stdio child even if declared.
var dangerousEnvKeys = map[string]bool{
	"LD_PRELOAD": true, "LD_LIBRARY_PATH": true,
	"DYLD_INSERT_LIBRARIES": true, "DYLD_LIBRARY_PATH": true,
	"NODE_OPTIONS": true, "PYTHONPATH": true, "PATH": true, "IFS": true,
}

// splitRef splits "name[@version]" into name and version pin.
func splitRef(ref string) (name, pin string) {
	if i := strings.LastIndex(ref, "@"); i > 0 && !strings.Contains(ref[i:], "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// hasVersionPin reports whether an npm-style spec already carries a version.
func hasVersionPin(arg string) bool {
	// Scoped packages start with @: need a second @ for the version.
	// Unscoped: a single @ means pinned.
	if strings.HasPrefix(arg, "@") {
		return strings.Count(arg, "@") >= 2
	}
	return strings.Contains(arg, "@")
}

// isShellArgv reports shell-wrapping commands that install must gate.
func isShellArgv(cmd []string) bool {
	for _, arg := range cmd {
		if arg == "sh" || arg == "bash" || arg == "cmd" || arg == "powershell" {
			return true
		}
		if strings.Contains(arg, "sh -c") {
			return true
		}
	}
	return false
}

// UnzipMCPB unzips a local .mcpb bundle and extracts its stdio command and entry point.
func UnzipMCPB(mcpbPath string, destDir string) (*config.MCPServerConfig, error) {
	r, err := zip.OpenReader(mcpbPath)
	if err != nil {
		return nil, fmt.Errorf("opening .mcpb zip file: %w", err)
	}
	defer r.Close()

	if len(r.File) > MaxMCPBEntries {
		return nil, fmt.Errorf("refusing .mcpb with %d entries (limit %d)", len(r.File), MaxMCPBEntries)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating target directory: %w", err)
	}

	var totalBytes uint64
	type candidate struct {
		path  string
		depth int
		rank  int
	}
	var best *candidate
	for _, f := range r.File {
		if f.UncompressedSize64 > MaxMCPBFile {
			return nil, fmt.Errorf("refusing .mcpb entry %s larger than %d bytes", f.Name, MaxMCPBFile)
		}
		totalBytes += f.UncompressedSize64
		if totalBytes > MaxMCPBBytes {
			return nil, fmt.Errorf("refusing .mcpb larger than %d bytes total", MaxMCPBBytes)
		}
		path := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(path, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("illegal file path in zip: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(path, 0o755)
			continue
		}
		if f.FileInfo().Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlink entry in .mcpb: %s", f.Name)
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}

		rc, err := f.Open()
		if err != nil {
			return nil, err
		}

		mode := f.Mode()
		// Mask to safe bits: dirs/files without setuid/setgid/world-writable.
		mode &^= 0o2222 &^ os.ModeSetuid &^ os.ModeSetgid
		if mode.Perm() == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
		if err != nil {
			rc.Close()
			return nil, err
		}

		_, err = io.Copy(out, io.LimitReader(rc, MaxMCPBFile+1))
		out.Close()
		rc.Close()
		if err != nil {
			return nil, err
		}

		rank := -1
		switch {
		case strings.HasSuffix(f.Name, "index.js"), strings.HasSuffix(f.Name, "index.py"):
			rank = 0
		case strings.HasSuffix(f.Name, "main.js"), strings.HasSuffix(f.Name, "main.py"):
			rank = 1
		}
		if rank >= 0 {
			depth := strings.Count(filepath.ToSlash(f.Name), "/")
			if best == nil || depth < best.depth || (depth == best.depth && rank < best.rank) {
				best = &candidate{path: path, depth: depth, rank: rank}
			}
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no entry script (index.js/main.js/index.py/main.py) found in .mcpb")
	}
	entryScript := best.path

	var cmd []string
	if strings.HasSuffix(entryScript, ".py") {
		cmd = []string{"python3", entryScript}
	} else if strings.HasSuffix(entryScript, ".js") {
		cmd = []string{"node", entryScript}
	} else {
		cmd = []string{entryScript}
	}

	return &config.MCPServerConfig{
		Type:    "stdio",
		Command: cmd,
	}, nil
}

// InstallServer installs an MCP server into the manager and persists configuration.
func InstallServer(ctx context.Context, mgr *MCPServerManager, opts InstallOptions) (*config.MCPServerConfig, error) {
	if opts.Scope != "" && opts.Scope != "user" && opts.Scope != "project" {
		return nil, fmt.Errorf("invalid scope %q: must be user or project", opts.Scope)
	}
	if opts.Scope == "project" {
		return nil, fmt.Errorf("project scope is not yet supported (user registry only)")
	}
	if opts.Pin != "" && !pinRe.MatchString(opts.Pin) {
		return nil, fmt.Errorf("invalid version pin %q", opts.Pin)
	}
	var serverCfg *config.MCPServerConfig
	var name string

	if strings.HasSuffix(opts.Ref, ".mcpb") {
		// Local MCPB file
		base := filepath.Base(opts.Ref)
		name = strings.TrimSuffix(base, ".mcpb")
		home := config.HakaseHome()
		if home == "" {
			home = ".hakase"
		}
		dest := filepath.Join(home, "mcpb", name)
		var err error
		serverCfg, err = UnzipMCPB(opts.Ref, dest)
		if err != nil {
			return nil, fmt.Errorf("installing .mcpb: %w", err)
		}
	} else {
		// Registry look up (ref may carry @version)
		refName, refPin := splitRef(opts.Ref)
		pin := opts.Pin
		if pin == "" {
			pin = refPin
		}
		name = refName
		client := NewRegistryClient("")
		regSrv, err := client.GetServer(ctx, refName)
		if err != nil {
			return nil, err
		}

		serverCfg = &config.MCPServerConfig{
			Env: make(map[string]string),
		}

		if pin != "" && regSrv.URL != "" && len(regSrv.Command) == 0 {
			return nil, fmt.Errorf("version pin %q does not apply to http server %q", pin, refName)
		}

		if opts.HTTP || (regSrv.URL != "" && !opts.Stdio) {
			serverCfg.Type = "http"
			serverCfg.URL = regSrv.URL
		} else {
			serverCfg.Type = "stdio"
			serverCfg.Command = regSrv.Command
			if pin != "" && len(serverCfg.Command) > 0 {
				lastIdx := len(serverCfg.Command) - 1
				if !hasVersionPin(serverCfg.Command[lastIdx]) {
					serverCfg.Command[lastIdx] = serverCfg.Command[lastIdx] + "@" + pin
				}
			}
		}

		for k := range regSrv.Env {
			if dangerousEnvKeys[k] {
				return nil, fmt.Errorf("registry declares dangerous env key %q, refusing to install", k)
			}
			// Environment keys are named via ExpandEnv reference, never secrets in plain text
			serverCfg.Env[k] = fmt.Sprintf("${%s}", k)
		}
	}

	// Apply provided env options as ExpandEnv references (never store raw secrets).
	// Only keys declared by the registry (or none for .mcpb) are accepted.
	if serverCfg.Env == nil {
		serverCfg.Env = make(map[string]string)
	}
	for k := range opts.AllowEnv {
		if dangerousEnvKeys[k] {
			return nil, fmt.Errorf("refusing dangerous env key %q", k)
		}
		if _, declared := serverCfg.Env[k]; !declared && !strings.HasSuffix(opts.Ref, ".mcpb") {
			return nil, fmt.Errorf("env key %q not declared by server %q", k, name)
		}
		serverCfg.Env[k] = fmt.Sprintf("${%s}", k)
	}

	// Shell gate: registry/.mcpb commands wrapping a shell need explicit --yes.
	if isShellArgv(serverCfg.Command) && !opts.Yes {
		return nil, fmt.Errorf("server %q runs via shell execution (%s): pass --yes to install anyway", name, strings.Join(serverCfg.Command, " "))
	}

	// Credential planning check
	plan := PlanCredentials(serverCfg.Env, opts.AllowEnv)
	if len(plan.Missing) > 0 && !opts.Yes {
		return nil, fmt.Errorf("missing required environment variable(s) for server %q: %s", name, strings.Join(plan.Missing, ", "))
	}

	// Clean name
	name = config.SanitizeMCPServerName(name)

	// Static audit check
	if err := serverCfg.Validate(); err != nil {
		return nil, fmt.Errorf("static audit validation failed: %w", err)
	}

	// Persist via UpsertServer
	if err := mgr.UpsertServer(name, serverCfg); err != nil {
		return nil, fmt.Errorf("upserting server: %w", err)
	}

	if err := mgr.Reconnect(name); err != nil {
		return serverCfg, fmt.Errorf("installed %q but reconnect failed: %w", name, err)
	}

	return serverCfg, nil
}
