// doctor.go - `hakase doctor`: preflight diagnostic command (DR-001..DR-008).
// Checks toolchain, config validity, sandbox mode, provider reachability,
// MCP server status, and credentials/JWT presence.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"amurru/hakase/internal/auth"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/mcp"
	"amurru/hakase/internal/sandbox"
)

// CheckResult status types
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
)

// DoctorCheck represents the outcome of a single preflight diagnostic check.
type DoctorCheck struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Status   string `json:"status"` // "ok" | "warn" | "fail"
	Detail   string `json:"detail"`
	FixHint  string `json:"fix_hint,omitempty"`
}

// DoctorReport holds the complete doctor output.
type DoctorReport struct {
	OverallPassed bool          `json:"overall_passed"`
	Checks        []DoctorCheck `json:"checks"`
}

// Injectable probes for hermetic testing.
var (
	lookPathFn              = exec.LookPath
	loadConfigFn            = config.LoadConfig
	resolveConfigPathFn     = config.ResolveConfigPath
	validateSandboxConfigFn = sandbox.ValidateSandboxConfig
	sandboxStartupWarningFn = sandbox.SandboxStartupWarning
	hakaseHomeFn            = config.HakaseHome
	httpProbeFn             = defaultHTTPProbe
	mcpDiagnoseFn           = defaultMCPDiagnose
)

func defaultHTTPProbe(ctx context.Context, targetURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, targetURL, nil)
	if err != nil {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			return err
		}
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, targetURL)
	}
	return nil
}

func defaultMCPDiagnose(ctx context.Context, cfg *config.Config) []mcp.ServerDiagnostic {
	mgr, err := mcp.NewMCPServerManager(cfg, nil)
	if err != nil {
		return nil
	}
	return mgr.Diagnose(mcpReadonlyCtx{Context: ctx})
}

// mcpReadonlyCtx adapts context.Context to agent.ReadonlyContext for mcp.Diagnose.
type mcpReadonlyCtx = mcpDoctorCtx

// RunDoctorCLI dispatches `hakase doctor` and returns the process exit code.
func RunDoctorCLI(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var format string
	var skipNet bool
	fs.StringVar(&format, "format", "table", "output format (table or json)")
	fs.BoolVar(&skipNet, "skip-net", false, "skip network reachability probes")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if format != "table" && format != "json" {
		fmt.Fprintf(os.Stderr, "format must be table|json\n")
		return 2
	}

	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected arguments: %v\n\n", fs.Args())
		fmt.Fprintf(os.Stderr, "Usage: hakase doctor [--format table|json] [--skip-net]\n")
		return 2
	}

	ctx := context.Background()
	report := runDoctorChecks(ctx, skipNet)

	if format == "json" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting json: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
	} else {
		printDoctorTable(report)
	}

	if !report.OverallPassed {
		return 1
	}
	return 0
}

func runDoctorChecks(ctx context.Context, skipNet bool) DoctorReport {
	var checks []DoctorCheck
	overallPassed := true

	// 1. Toolchain Check (DR-002)
	toolchainChecks, tcPassed := checkToolchain()
	checks = append(checks, toolchainChecks...)
	if !tcPassed {
		overallPassed = false
	}

	// 2. Config Check (DR-003)
	cfgPath := resolveConfigPathFn("config.json")
	cfg, cfgCheck, cfgPassed := checkConfig(cfgPath)
	checks = append(checks, cfgCheck)
	if !cfgPassed {
		overallPassed = false
	}

	// 3. Sandbox Check (DR-004)
	if cfg != nil {
		sbCheck, sbPassed := checkSandbox(cfg)
		checks = append(checks, sbCheck)
		if !sbPassed {
			overallPassed = false
		}
	} else {
		checks = append(checks, DoctorCheck{
			Category: "sandbox",
			Name:     "sandbox mode",
			Status:   StatusWarn,
			Detail:   "skipped (no config loaded)",
		})
	}

	// 4. Provider Check (DR-005)
	if cfg != nil {
		provChecks, provPassed := checkProvider(ctx, cfg, skipNet)
		checks = append(checks, provChecks...)
		if !provPassed {
			overallPassed = false
		}
	} else {
		checks = append(checks, DoctorCheck{
			Category: "provider",
			Name:     "provider configuration",
			Status:   StatusWarn,
			Detail:   "skipped (no config loaded)",
		})
	}

	// 5. MCP Check (DR-006)
	if cfg != nil {
		mcpChecks, mcpPassed := checkMCP(ctx, cfg, skipNet)
		checks = append(checks, mcpChecks...)
		if !mcpPassed {
			overallPassed = false
		}
	} else {
		checks = append(checks, DoctorCheck{
			Category: "mcp",
			Name:     "mcp servers",
			Status:   StatusOK,
			Detail:   "skipped (no config loaded)",
		})
	}

	// 6. Credentials Check (DR-007)
	credChecks, credPassed := checkCredentials()
	checks = append(checks, credChecks...)
	if !credPassed {
		overallPassed = false
	}

	return DoctorReport{
		OverallPassed: overallPassed,
		Checks:        checks,
	}
}

type toolInfo struct {
	name     string
	required bool
	hint     string
}

func checkToolchain() ([]DoctorCheck, bool) {
	tools := []toolInfo{
		{name: "go", required: true, hint: "Install Go 1.26+ from https://go.dev/doc/install"},
		{name: "node", required: true, hint: "Install Node.js 22+ from https://nodejs.org"},
		{name: "python3", required: true, hint: "Install Python 3 from https://python.org or system package manager"},
		{name: "pnpm", required: false, hint: "Install pnpm (npm install -g pnpm) to build/test the web UI"},
		{name: "ffmpeg", required: false, hint: "Install ffmpeg via system package manager for audio/voice features"},
		{name: "whisper-cli", required: false, hint: "Install whisper.cpp (whisper-cli) for local speech-to-text voice notes"},
		{name: "piper", required: false, hint: "Install piper (pip install piper-tts) for local text-to-speech voice notes"},
	}

	var checks []DoctorCheck
	passed := true

	for _, t := range tools {
		path, err := lookPathFn(t.name)
		if err != nil {
			status := StatusWarn
			if t.required {
				status = StatusFail
				passed = false
			}
			checks = append(checks, DoctorCheck{
				Category: "toolchain",
				Name:     t.name,
				Status:   status,
				Detail:   "not found on PATH",
				FixHint:  t.hint,
			})
		} else {
			checks = append(checks, DoctorCheck{
				Category: "toolchain",
				Name:     t.name,
				Status:   StatusOK,
				Detail:   path,
			})
		}
	}

	return checks, passed
}

func checkConfig(cfgPath string) (*config.Config, DoctorCheck, bool) {
	cfg, err := loadConfigFn(cfgPath)
	if err != nil {
		if config.IsNoConfig(err) {
			return nil, DoctorCheck{
				Category: "config",
				Name:     "config.json",
				Status:   StatusFail,
				Detail:   "no configuration found",
				FixHint:  "Run 'hakase init' to create a configuration file.",
			}, false
		}
		return nil, DoctorCheck{
			Category: "config",
			Name:     "config.json",
			Status:   StatusFail,
			Detail:   fmt.Sprintf("config error: %v", err),
			FixHint:  "Fix errors in config.json or re-run 'hakase init'.",
		}, false
	}

	return cfg, DoctorCheck{
		Category: "config",
		Name:     "config.json",
		Status:   StatusOK,
		Detail:   fmt.Sprintf("loaded from %s (provider: %s, model: %s)", cfgPath, cfg.Provider, cfg.EffectiveModelName()),
	}, true
}

func checkSandbox(cfg *config.Config) (DoctorCheck, bool) {
	sb := sandbox.LoadSandboxConfig(cfg.Sandbox)
	if err := validateSandboxConfigFn(sb); err != nil {
		return DoctorCheck{
			Category: "sandbox",
			Name:     "sandbox config",
			Status:   StatusFail,
			Detail:   fmt.Sprintf("invalid sandbox mode: %v", err),
			FixHint:  "Update 'sandbox.mode' in config.json to 'paths', 'bubblewrap', or 'off'.",
		}, false
	}

	detail := fmt.Sprintf("mode: %s", sb.Mode)
	if warn := sandboxStartupWarningFn(sb); warn != "" {
		return DoctorCheck{
			Category: "sandbox",
			Name:     "sandbox config",
			Status:   StatusWarn,
			Detail:   fmt.Sprintf("mode: %s (%s)", sb.Mode, warn),
			FixHint:  "Ensure required sandboxing tools (e.g. bwrap) are installed if using bubblewrap mode.",
		}, true
	}

	return DoctorCheck{
		Category: "sandbox",
		Name:     "sandbox config",
		Status:   StatusOK,
		Detail:   detail,
	}, true
}

func checkProvider(ctx context.Context, cfg *config.Config, skipNet bool) ([]DoctorCheck, bool) {
	var checks []DoctorCheck
	passed := true

	provider := cfg.Provider
	if provider == "" {
		provider = "gemini"
	}

	// Validate provider configuration requirements
	switch provider {
	case "gemini", "openai":
		if cfg.APIKey == "" {
			checks = append(checks, DoctorCheck{
				Category: "provider",
				Name:     provider + " credentials",
				Status:   StatusFail,
				Detail:   "api_key is missing",
				FixHint:  fmt.Sprintf("Set 'api_key' in config.json or export HAKASE_API_KEY for provider %s.", provider),
			})
			passed = false
		} else {
			checks = append(checks, DoctorCheck{
				Category: "provider",
				Name:     provider + " credentials",
				Status:   StatusOK,
				Detail:   "api_key is configured",
			})
		}
	case "openai-compatible":
		if cfg.BaseURL == "" {
			checks = append(checks, DoctorCheck{
				Category: "provider",
				Name:     "openai-compatible endpoint",
				Status:   StatusFail,
				Detail:   "base_url is missing",
				FixHint:  "Set 'base_url' in config.json (e.g. http://localhost:11434/v1) for provider openai-compatible.",
			})
			passed = false
		} else {
			checks = append(checks, DoctorCheck{
				Category: "provider",
				Name:     "openai-compatible endpoint",
				Status:   StatusOK,
				Detail:   fmt.Sprintf("base_url set to %s", cfg.BaseURL),
			})
		}
	default:
		checks = append(checks, DoctorCheck{
			Category: "provider",
			Name:     "provider type",
			Status:   StatusFail,
			Detail:   fmt.Sprintf("unsupported provider %q", provider),
			FixHint:  "Set 'provider' in config.json to 'gemini', 'openai', or 'openai-compatible'.",
		})
		return checks, false
	}

	if skipNet {
		checks = append(checks, DoctorCheck{
			Category: "provider",
			Name:     provider + " reachability",
			Status:   StatusOK,
			Detail:   "skipped (--skip-net)",
		})
		return checks, passed
	}

	// Network reachability probe
	targetURL := cfg.BaseURL
	if targetURL == "" {
		if provider == "gemini" {
			targetURL = "https://generativelanguage.googleapis.com"
		} else if provider == "openai" {
			targetURL = "https://api.openai.com"
		}
	}

	if targetURL != "" {
		if err := httpProbeFn(ctx, targetURL); err != nil {
			checks = append(checks, DoctorCheck{
				Category: "provider",
				Name:     provider + " reachability",
				Status:   StatusWarn,
				Detail:   fmt.Sprintf("endpoint %s unreachable: %v", targetURL, err),
				FixHint:  "Check network connection, base_url, or provider status.",
			})
		} else {
			checks = append(checks, DoctorCheck{
				Category: "provider",
				Name:     provider + " reachability",
				Status:   StatusOK,
				Detail:   fmt.Sprintf("endpoint %s reachable", targetURL),
			})
		}
	}

	return checks, passed
}

func checkMCP(ctx context.Context, cfg *config.Config, skipNet bool) ([]DoctorCheck, bool) {
	if cfg == nil || len(cfg.MCPServers.Servers) == 0 {
		return []DoctorCheck{{
			Category: "mcp",
			Name:     "mcp servers",
			Status:   StatusOK,
			Detail:   "no MCP servers configured",
		}}, true
	}

	if skipNet {
		return []DoctorCheck{{
			Category: "mcp",
			Name:     "mcp servers",
			Status:   StatusOK,
			Detail:   fmt.Sprintf("%d server(s) configured (probes skipped via --skip-net)", len(cfg.MCPServers.Servers)),
		}}, true
	}

	diags := mcpDiagnoseFn(ctx, cfg)
	var checks []DoctorCheck
	passed := true

	for _, d := range diags {
		if d.Disabled {
			checks = append(checks, DoctorCheck{
				Category: "mcp",
				Name:     "mcp:" + d.Name,
				Status:   StatusOK,
				Detail:   "disabled",
			})
			continue
		}
		if !d.OK {
			passed = false
			checks = append(checks, DoctorCheck{
				Category: "mcp",
				Name:     "mcp:" + d.Name,
				Status:   StatusFail,
				Detail:   fmt.Sprintf("unreachable or failed: %s", d.Error),
				FixHint:  fmt.Sprintf("Check MCP server %q command/URL and configuration.", d.Name),
			})
		} else {
			checks = append(checks, DoctorCheck{
				Category: "mcp",
				Name:     "mcp:" + d.Name,
				Status:   StatusOK,
				Detail:   fmt.Sprintf("ok (%d tools, dial %v)", d.ToolCount, d.Dial.Round(time.Millisecond)),
			})
		}
	}

	return checks, passed
}

func checkCredentials() ([]DoctorCheck, bool) {
	home := hakaseHomeFn()
	if home == "" {
		return []DoctorCheck{{
			Category: "credentials",
			Name:     "hakase home",
			Status:   StatusFail,
			Detail:   "cannot determine HAKASE_HOME directory",
			FixHint:  "Set HAKASE_HOME environment variable or configure user home directory.",
		}}, false
	}

	var checks []DoctorCheck
	passed := true

	// credentials.json check
	credsPath := filepath.Join(home, "credentials.json")
	if _, err := os.Stat(credsPath); os.IsNotExist(err) {
		checks = append(checks, DoctorCheck{
			Category: "credentials",
			Name:     "admin credentials",
			Status:   StatusWarn,
			Detail:   "credentials.json missing",
			FixHint:  "Run 'hakase auth set-password' to configure web UI admin login.",
		})
	} else {
		// Try loading auth credential
		_, err := auth.LoadCredentials(credsPath)
		if err != nil {
			checks = append(checks, DoctorCheck{
				Category: "credentials",
				Name:     "admin credentials",
				Status:   StatusFail,
				Detail:   fmt.Sprintf("credentials.json parse error: %v", err),
				FixHint:  "Re-run 'hakase auth set-password' to recreate credentials.json.",
			})
			passed = false
		} else {
			checks = append(checks, DoctorCheck{
				Category: "credentials",
				Name:     "admin credentials",
				Status:   StatusOK,
				Detail:   credsPath + " present and valid",
			})
		}
	}

	// jwt-secret check
	jwtPath := filepath.Join(home, "jwt-secret")
	if _, err := os.Stat(jwtPath); os.IsNotExist(err) {
		checks = append(checks, DoctorCheck{
			Category: "credentials",
			Name:     "jwt secret",
			Status:   StatusWarn,
			Detail:   "jwt-secret missing (will be auto-generated when running hakase web)",
			FixHint:  "Run 'hakase web' to initialize the JWT secret.",
		})
	} else {
		checks = append(checks, DoctorCheck{
			Category: "credentials",
			Name:     "jwt secret",
			Status:   StatusOK,
			Detail:   jwtPath + " present",
		})
	}

	return checks, passed
}

func printDoctorTable(report DoctorReport) {
	fmt.Println("Hakase Doctor Diagnostics")
	fmt.Println("=========================")
	fmt.Println()

	currCat := ""
	for _, c := range report.Checks {
		if c.Category != currCat {
			currCat = c.Category
			fmt.Printf("[%s]\n", strings.ToUpper(currCat))
		}
		statusTag := "[OK]"
		if c.Status == StatusWarn {
			statusTag = "[WARN]"
		} else if c.Status == StatusFail {
			statusTag = "[FAIL]"
		}
		fmt.Printf("  %-8s %-25s : %s\n", statusTag, c.Name, c.Detail)
		if c.FixHint != "" {
			fmt.Printf("           -> Fix hint: %s\n", c.FixHint)
		}
	}

	fmt.Println()
	if report.OverallPassed {
		fmt.Println("Result: ALL CHECKS PASSED")
	} else {
		fmt.Println("Result: ONE OR MORE CHECKS FAILED")
	}
}
