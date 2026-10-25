// pip_profile_test.go - S22 pip bwrap profile flags.
package sandbox

import (
	"os"
	"strings"
	"testing"
)

// TestS22PipBwrapProfile pins the pip sandbox posture (S22): the workspace
// binds read-only, only .venv stays writable, and the network is explicitly
// open for index egress. The S18 allowlist + approval + name-shape gates
// above are the compensating controls, not the net jail.
func TestS22PipBwrapProfile(t *testing.T) {
	tmpDir := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModeBubblewrap,
		WorkspaceRoots: []string{tmpDir},
	}
	venv := tmpDir + "/.venv"
	if err := os.MkdirAll(venv, 0755); err != nil {
		t.Fatalf("create .venv fixture: %v", err)
	}

	argv, err := buildBwrapArgv(sb, []string{"pip", "install", "requests"}, tmpDir, true, []string{venv}, true)
	if err != nil {
		t.Fatalf("buildBwrapArgv pip profile: %v", err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--ro-bind "+tmpDir+" "+tmpDir) {
		t.Errorf("pip profile must bind the workspace read-only; got: %s", joined)
	}
	if strings.Contains(joined, "--bind "+tmpDir+" "+tmpDir) {
		t.Errorf("pip profile must not bind the workspace writable; got: %s", joined)
	}
	if !strings.Contains(joined, "--bind "+venv) {
		t.Errorf("pip profile must keep .venv writable; got: %s", joined)
	}
	if strings.Contains(joined, "--unshare-net") {
		t.Errorf("pip profile must leave the network open for the index; got: %s", joined)
	}

	// Non-pip profiles are unchanged: workspace writable, net jailed.
	argv, err = buildBwrapArgv(sb, []string{"python3", "script.py"}, tmpDir, false, nil, false)
	if err != nil {
		t.Fatalf("buildBwrapArgv default profile: %v", err)
	}
	joined = strings.Join(argv, " ")
	if !strings.Contains(joined, "--bind "+tmpDir+" "+tmpDir) {
		t.Errorf("default profile must bind the workspace writable; got: %s", joined)
	}
	if !strings.Contains(joined, "--unshare-net") {
		t.Errorf("default profile must jail the network; got: %s", joined)
	}
}
