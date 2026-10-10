package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/mcp"
	"amurru/hakase/internal/sandbox"
)

func isolateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("HAKASE_HOME", filepath.Join(dir, ".hakase"))
	_ = os.MkdirAll(filepath.Join(dir, ".hakase"), 0700)
	return dir
}

func captureDoctorOutput(f func() int) (int, string) {
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stdout = w
	os.Stderr = w

	code := f()

	_ = w.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()

	return code, buf.String()
}

func TestRunDoctorCLI_Usage(t *testing.T) {
	code, out := captureDoctorOutput(func() int {
		return RunDoctorCLI([]string{"--invalid-flag"})
	})
	if code != 2 {
		t.Errorf("expected exit code 2 on flag error, got %d", code)
	}
	if !strings.Contains(out, "flag provided but not defined") {
		t.Errorf("expected flag error in output, got: %s", out)
	}

	code, out = captureDoctorOutput(func() int {
		return RunDoctorCLI([]string{"unexpected_arg"})
	})
	if code != 2 {
		t.Errorf("expected exit code 2 on extra arg, got %d", code)
	}
	if !strings.Contains(out, "unexpected arguments") {
		t.Errorf("expected unexpected arguments in output, got: %s", out)
	}
}

func TestDoctor_AllPass(t *testing.T) {
	home := isolateHome(t)

	origLookPath := lookPathFn
	origLoadConfig := loadConfigFn
	origResolveConfig := resolveConfigPathFn
	origValidateSandbox := validateSandboxConfigFn
	origStartupWarning := sandboxStartupWarningFn
	origHakaseHome := hakaseHomeFn
	origHTTPProbe := httpProbeFn
	origMCPDiagnose := mcpDiagnoseFn
	defer func() {
		lookPathFn = origLookPath
		loadConfigFn = origLoadConfig
		resolveConfigPathFn = origResolveConfig
		validateSandboxConfigFn = origValidateSandbox
		sandboxStartupWarningFn = origStartupWarning
		hakaseHomeFn = origHakaseHome
		httpProbeFn = origHTTPProbe
		mcpDiagnoseFn = origMCPDiagnose
	}()

	lookPathFn = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	loadConfigFn = func(path string) (*config.Config, error) {
		return &config.Config{
			Provider:  "gemini",
			APIKey:    "test-key",
			ModelName: "gemini-3.7-flash",
		}, nil
	}
	resolveConfigPathFn = func(local string) string { return "/tmp/config.json" }
	validateSandboxConfigFn = func(sb *sandbox.SandboxConfig) error { return nil }
	sandboxStartupWarningFn = func(sb *sandbox.SandboxConfig) string { return "" }
	hakaseHomeFn = func() string { return filepath.Join(home, ".hakase") }
	httpProbeFn = func(ctx context.Context, url string) error { return nil }
	mcpDiagnoseFn = func(ctx context.Context, cfg *config.Config) []mcp.ServerDiagnostic {
		return nil
	}

	credPath := filepath.Join(home, ".hakase", "credentials.json")
	_ = os.WriteFile(credPath, []byte(`{"username":"admin","argon2_hash":"$argon2id$v=19$m=65536,t=1,p=4$abc$def"}`), 0600)
	jwtPath := filepath.Join(home, ".hakase", "jwt-secret")
	_ = os.WriteFile(jwtPath, []byte("supersecret"), 0600)

	code, out := captureDoctorOutput(func() int {
		return RunDoctorCLI([]string{})
	})

	if code != 0 {
		t.Errorf("expected exit code 0 for all-pass, got %d. Output: %s", code, out)
	}
	if !strings.Contains(out, "Result: ALL CHECKS PASSED") {
		t.Errorf("expected 'Result: ALL CHECKS PASSED' in output, got: %s", out)
	}
}

func TestDoctor_JSONFormat(t *testing.T) {
	home := isolateHome(t)

	origLookPath := lookPathFn
	origLoadConfig := loadConfigFn
	origResolveConfig := resolveConfigPathFn
	origHakaseHome := hakaseHomeFn
	defer func() {
		lookPathFn = origLookPath
		loadConfigFn = origLoadConfig
		resolveConfigPathFn = origResolveConfig
		hakaseHomeFn = origHakaseHome
	}()

	lookPathFn = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	loadConfigFn = func(path string) (*config.Config, error) {
		return &config.Config{Provider: "gemini", APIKey: "key"}, nil
	}
	resolveConfigPathFn = func(local string) string { return "config.json" }
	hakaseHomeFn = func() string { return filepath.Join(home, ".hakase") }

	_ = os.WriteFile(filepath.Join(home, ".hakase", "credentials.json"), []byte(`{"username":"admin","argon2_hash":"hash"}`), 0600)
	_ = os.WriteFile(filepath.Join(home, ".hakase", "jwt-secret"), []byte("secret"), 0600)

	code, out := captureDoctorOutput(func() int {
		return RunDoctorCLI([]string{"--format", "json", "--skip-net"})
	})

	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}

	var report DoctorReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("failed to parse json report: %v, raw: %s", err, out)
	}

	if !report.OverallPassed {
		t.Errorf("expected report.OverallPassed to be true")
	}
	if len(report.Checks) == 0 {
		t.Errorf("expected checks in json report")
	}
}

func TestDoctor_MissingConfig(t *testing.T) {
	isolateHome(t)

	origLoadConfig := loadConfigFn
	defer func() { loadConfigFn = origLoadConfig }()

	loadConfigFn = func(path string) (*config.Config, error) {
		return nil, config.ErrNoConfig
	}

	code, out := captureDoctorOutput(func() int {
		return RunDoctorCLI([]string{"--skip-net"})
	})

	if code != 1 {
		t.Errorf("expected exit code 1 when config missing, got %d", code)
	}
	if !strings.Contains(out, "no configuration found") {
		t.Errorf("expected 'no configuration found' in output, got: %s", out)
	}
	if !strings.Contains(out, "Run 'hakase init'") {
		t.Errorf("expected 'Run 'hakase init'' hint in output, got: %s", out)
	}
}

func TestDoctor_MissingCoreTool(t *testing.T) {
	isolateHome(t)

	origLookPath := lookPathFn
	origLoadConfig := loadConfigFn
	defer func() {
		lookPathFn = origLookPath
		loadConfigFn = origLoadConfig
	}()

	lookPathFn = func(file string) (string, error) {
		if file == "python3" {
			return "", fmt.Errorf("not found")
		}
		return "/usr/bin/" + file, nil
	}
	loadConfigFn = func(path string) (*config.Config, error) {
		return &config.Config{Provider: "gemini", APIKey: "key"}, nil
	}

	code, out := captureDoctorOutput(func() int {
		return RunDoctorCLI([]string{"--skip-net"})
	})

	if code != 1 {
		t.Errorf("expected exit code 1 when core tool missing, got %d", code)
	}
	if !strings.Contains(out, "[FAIL]   python3") {
		t.Errorf("expected python3 failure line, got: %s", out)
	}
}

func TestDoctor_MissingOptionalTool(t *testing.T) {
	home := isolateHome(t)

	origLookPath := lookPathFn
	origLoadConfig := loadConfigFn
	origHakaseHome := hakaseHomeFn
	defer func() {
		lookPathFn = origLookPath
		loadConfigFn = origLoadConfig
		hakaseHomeFn = origHakaseHome
	}()

	lookPathFn = func(file string) (string, error) {
		if file == "ffmpeg" {
			return "", fmt.Errorf("not found")
		}
		return "/usr/bin/" + file, nil
	}
	loadConfigFn = func(path string) (*config.Config, error) {
		return &config.Config{Provider: "gemini", APIKey: "key"}, nil
	}
	hakaseHomeFn = func() string { return filepath.Join(home, ".hakase") }

	_ = os.WriteFile(filepath.Join(home, ".hakase", "credentials.json"), []byte(`{"username":"admin","argon2_hash":"hash"}`), 0600)
	_ = os.WriteFile(filepath.Join(home, ".hakase", "jwt-secret"), []byte("secret"), 0600)

	code, out := captureDoctorOutput(func() int {
		return RunDoctorCLI([]string{"--skip-net"})
	})

	if code != 0 {
		t.Errorf("expected exit code 0 when only optional tool missing, got %d. Output: %s", code, out)
	}
	if !strings.Contains(out, "[WARN]   ffmpeg") {
		t.Errorf("expected ffmpeg warning line, got: %s", out)
	}
}

func TestDoctor_LandlockSandboxRefusal(t *testing.T) {
	isolateHome(t)

	origLoadConfig := loadConfigFn
	origValidateSandbox := validateSandboxConfigFn
	defer func() {
		loadConfigFn = origLoadConfig
		validateSandboxConfigFn = origValidateSandbox
	}()

	loadConfigFn = func(path string) (*config.Config, error) {
		return &config.Config{
			Provider: "gemini",
			APIKey:   "key",
			Sandbox:  &sandbox.SandboxJSON{Mode: "landlock"},
		}, nil
	}
	validateSandboxConfigFn = func(sb *sandbox.SandboxConfig) error {
		return fmt.Errorf("sandbox mode 'landlock' is reserved but unimplemented; set mode to 'paths', 'bubblewrap', or 'off'")
	}

	code, out := captureDoctorOutput(func() int {
		return RunDoctorCLI([]string{"--skip-net"})
	})

	if code != 1 {
		t.Errorf("expected exit code 1 on invalid sandbox mode, got %d", code)
	}
	if !strings.Contains(out, "sandbox mode 'landlock' is reserved but unimplemented") {
		t.Errorf("expected landlock refusal error in output, got: %s", out)
	}
}

func TestDoctor_MCPAndCredentialsFailures(t *testing.T) {
	home := isolateHome(t)

	origLookPath := lookPathFn
	origLoadConfig := loadConfigFn
	origHakaseHome := hakaseHomeFn
	origMCPDiagnose := mcpDiagnoseFn
	defer func() {
		lookPathFn = origLookPath
		loadConfigFn = origLoadConfig
		hakaseHomeFn = origHakaseHome
		mcpDiagnoseFn = origMCPDiagnose
	}()

	lookPathFn = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	loadConfigFn = func(path string) (*config.Config, error) {
		return &config.Config{
			Provider: "gemini",
			APIKey:   "key",
			MCPServers: config.MCPConfig{
				Servers: map[string]*config.MCPServerConfig{
					"broken": {URL: "http://localhost:9999"},
				},
			},
		}, nil
	}
	hakaseHomeFn = func() string { return filepath.Join(home, ".hakase") }

	mcpDiagnoseFn = func(ctx context.Context, cfg *config.Config) []mcp.ServerDiagnostic {
		return []mcp.ServerDiagnostic{
			{Name: "broken", OK: false, Error: "connection refused"},
		}
	}

	code, out := captureDoctorOutput(func() int {
		return RunDoctorCLI([]string{})
	})

	if code != 1 {
		t.Errorf("expected exit code 1 due to broken MCP server, got %d. Output: %s", code, out)
	}
	if !strings.Contains(out, "[FAIL]   mcp:broken") {
		t.Errorf("expected mcp:broken failure in output, got: %s", out)
	}
	if !strings.Contains(out, "[WARN]   admin credentials") {
		t.Errorf("expected admin credentials warning in output, got: %s", out)
	}
}
