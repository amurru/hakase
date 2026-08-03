package main

import (
	"os"
	"strings"
	"testing"
)

func TestBwrapPath(t *testing.T) {
	// Reset cache so the test is hermetic.
	bwrapCachedPath = ""
	bwrapCachedErr = nil

	p, err := bwrapPath()
	if err != nil {
		t.Skipf("bwrap not installed on this machine: %v", err)
	}
	if p == "" {
		t.Fatal("bwrapPath returned empty path with no error")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("bwrap path %q does not exist: %v", p, err)
	}
}

func TestBuildBwrapArgv(t *testing.T) {
	tmpDir := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModeBubblewrap,
		WorkspaceRoots: []string{tmpDir},
	}

	argv, err := buildBwrapArgv(sb, []string{"echo", "hello"}, tmpDir, false, nil)
	if err != nil {
		t.Fatalf("buildBwrapArgv: %v", err)
	}

	// Must contain namespace isolation flags.
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-user", "--cap-drop", "--unshare-net"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bwrap argv missing %q; got: %s", want, joined)
		}
	}

	// Must contain the workspace bind.
	if !strings.Contains(joined, "--bind "+tmpDir+" "+tmpDir) {
		t.Errorf("bwrap argv missing workspace bind for %s; got: %s", tmpDir, joined)
	}

	// Must contain the proc/dev/tmp/run mounts.
	for _, want := range []string{"--proc /proc", "--dev /dev", "--tmpfs /tmp", "--tmpfs /run"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bwrap argv missing %q; got: %s", want, joined)
		}
	}

	// Must end with "-- echo hello".
	if !strings.HasSuffix(joined, "-- echo hello") {
		t.Errorf("bwrap argv must terminate with '-- echo hello'; got: %s", joined)
	}

	// chdir must point to the workspace.
	if !strings.Contains(joined, "--chdir "+tmpDir) {
		t.Errorf("bwrap argv missing --chdir %s; got: %s", tmpDir, joined)
	}
}

func TestBuildBwrapArgvNetwork(t *testing.T) {
	sb := &SandboxConfig{
		Mode:           SandboxModeBubblewrap,
		WorkspaceRoots: []string{t.TempDir()},
	}

	// needsNetwork=true: must NOT contain --unshare-net.
	argv, _ := buildBwrapArgv(sb, []string{"curl"}, "", true, nil)
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "--unshare-net") {
		t.Errorf("needsNetwork=true but argv contains --unshare-net; got: %s", joined)
	}

	// needsNetwork=false: MUST contain --unshare-net.
	argv, _ = buildBwrapArgv(sb, []string{"curl"}, "", false, nil)
	joined = strings.Join(argv, " ")
	if !strings.Contains(joined, "--unshare-net") {
		t.Errorf("needsNetwork=false but argv missing --unshare-net; got: %s", joined)
	}
}

func TestBuildBwrapArgvExtraBinds(t *testing.T) {
	venvDir := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModeBubblewrap,
		WorkspaceRoots: []string{t.TempDir()},
	}

	// Extra bind in "src:dst" form.
	argv, err := buildBwrapArgv(sb, []string{"python3"}, "", false, []string{venvDir + ":/opt/venv"})
	if err != nil {
		t.Fatalf("buildBwrapArgv: %v", err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--bind "+venvDir+" /opt/venv") {
		t.Errorf("extra bind missing; got: %s", joined)
	}
}

func TestBuildBwrapArgvErrors(t *testing.T) {
	// Nil config.
	if _, err := buildBwrapArgv(nil, []string{"ls"}, "", false, nil); err == nil {
		t.Error("expected error for nil config")
	}
	// Empty innerArgv.
	sb := &SandboxConfig{Mode: SandboxModeBubblewrap, WorkspaceRoots: []string{"."}}
	if _, err := buildBwrapArgv(sb, nil, "", false, nil); err == nil {
		t.Error("expected error for empty innerArgv")
	}
}

func TestWrapBwrapCmdIntegration(t *testing.T) {
	bwrapCachedPath = ""
	bwrapCachedErr = nil
	p, err := bwrapPath()
	if err != nil {
		t.Skipf("bwrap not installed: %v", err)
	}

	sb := &SandboxConfig{
		Mode:           SandboxModeBubblewrap,
		WorkspaceRoots: []string{t.TempDir()},
	}
	cmd, err := wrapBwrapCmd(sb, []string{"echo", "hello-from-bwrap"}, "", false, nil)
	if err != nil {
		t.Fatalf("wrapBwrapCmd: %v", err)
	}
	if cmd.Path != p {
		t.Errorf("cmd.Path = %q, want %q", cmd.Path, p)
	}
	if len(cmd.Args) < 2 {
		t.Fatalf("cmd.Args too short: %v", cmd.Args)
	}
	if cmd.Args[0] != p || cmd.Args[1] != "--unshare-pid" {
		t.Errorf("unexpected cmd.Args[0:2]: %v", cmd.Args[:2])
	}
}
