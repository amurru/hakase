package mcp

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	return plan
}

// UnzipMCPB unzips a local .mcpb bundle and extracts its stdio command and entry point.
func UnzipMCPB(mcpbPath string, destDir string) (*config.MCPServerConfig, error) {
	r, err := zip.OpenReader(mcpbPath)
	if err != nil {
		return nil, fmt.Errorf("opening .mcpb zip file: %w", err)
	}
	defer r.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating target directory: %w", err)
	}

	var entryScript string
	for _, f := range r.File {
		path := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(path, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("illegal file path in zip: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.Mode())
			continue
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}

		rc, err := f.Open()
		if err != nil {
			return nil, err
		}

		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return nil, err
		}

		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return nil, err
		}

		if strings.HasSuffix(f.Name, "index.js") || strings.HasSuffix(f.Name, "main.js") || strings.HasSuffix(f.Name, "index.py") || strings.HasSuffix(f.Name, "main.py") {
			entryScript = path
		}
	}

	if entryScript == "" {
		entryScript = destDir
	}

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
		// Registry look up
		name = opts.Ref
		client := NewRegistryClient("")
		regSrv, err := client.GetServer(ctx, opts.Ref)
		if err != nil {
			return nil, err
		}

		serverCfg = &config.MCPServerConfig{
			Env: make(map[string]string),
		}

		if opts.HTTP || (regSrv.URL != "" && !opts.Stdio) {
			serverCfg.Type = "http"
			serverCfg.URL = regSrv.URL
		} else {
			serverCfg.Type = "stdio"
			serverCfg.Command = regSrv.Command
			if opts.Pin != "" && len(serverCfg.Command) > 0 {
				lastIdx := len(serverCfg.Command) - 1
				if !strings.Contains(serverCfg.Command[lastIdx], "@") {
					serverCfg.Command[lastIdx] = serverCfg.Command[lastIdx] + "@" + opts.Pin
				}
			}
		}

		for k := range regSrv.Env {
			// Environment keys are named via ExpandEnv reference, never secrets in plain text
			serverCfg.Env[k] = fmt.Sprintf("${%s}", k)
		}
	}

	// Apply provided env options as ExpandEnv references (never store raw secrets)
	if serverCfg.Env == nil {
		serverCfg.Env = make(map[string]string)
	}
	for k := range opts.AllowEnv {
		serverCfg.Env[k] = fmt.Sprintf("${%s}", k)
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
		_ = err
	}

	return serverCfg, nil
}
