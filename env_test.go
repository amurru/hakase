// env_test.go - tests for runtime environment detection and rendering
// (env.go, env_linux.go, env_other.go).
package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sampleSystemInfo returns a fully-populated SystemInfo for rendering tests,
// independent of the host machine.
func sampleSystemInfo() *SystemInfo {
	return &SystemInfo{
		OS:               "linux",
		Architecture:     "amd64",
		KernelVersion:    "6.18.39-1-lts",
		DistroID:         "arch",
		DistroPretty:     "Arch Linux",
		PackageManager:   "pacman",
		Shell:            "zsh",
		Locale:           "en_US.UTF-8",
		Timezone:         "Asia/Damascus",
		TZOffset:         "+03:00",
		Username:         "amurru",
		HomeDir:          "/home/amurru",
		Hostname:         "omnibase",
		WorkspaceRoot:    "/home/amurru/Projects/hakase",
		DiskFreeHuman:    "412 GiB",
		MemoryTotalHuman: "15.5 GiB",
		Tools: []ToolInfo{
			{Name: "go", Path: "/usr/bin/go", Version: "1.26.5"},
			{Name: "python3", Path: "/usr/bin/python3", Version: "3.14.6"},
		},
	}
}

func TestParseOSReleaseContent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("parseOSReleaseContent is linux-specific")
	}
	cases := []struct {
		name     string
		content  string
		id, ver  string
		codename string
		pretty   string
	}{
		{
			name: "arch",
			content: `NAME="Arch Linux"
PRETTY_NAME="Arch Linux"
ID=arch
BUILD_ID=rolling
ANSI_COLOR="38;2;23;147;209"
HOME_URL="https://archlinux.org/"`,
			id: "arch", pretty: "Arch Linux",
		},
		{
			name: "ubuntu with codename and quotes",
			content: `PRETTY_NAME="Ubuntu 24.04.2 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.2 LTS (Noble Numbat)"
VERSION_CODENAME=noble
ID=ubuntu
ID_LIKE=debian
HOME_URL="https://www.ubuntu.com/"`,
			id: "ubuntu", ver: "24.04", codename: "noble", pretty: "Ubuntu 24.04.2 LTS",
		},
		{
			name: "comments and blank lines",
			content: `# some comment

ID=debian

VERSION_ID="12"
# another comment
VERSION_CODENAME=bookworm`,
			id: "debian", ver: "12", codename: "bookworm",
		},
		{
			name:    "empty",
			content: "",
		},
		{
			name:    "malformed no equals",
			content: "THIS IS NOT KEY=VALUE\nID=busybox\n",
			id:      "busybox",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, ver, codename, pretty := parseOSReleaseContent(tc.content)
			if id != tc.id {
				t.Errorf("id = %q, want %q", id, tc.id)
			}
			if ver != tc.ver {
				t.Errorf("version = %q, want %q", ver, tc.ver)
			}
			if codename != tc.codename {
				t.Errorf("codename = %q, want %q", codename, tc.codename)
			}
			if pretty != tc.pretty {
				t.Errorf("pretty = %q, want %q", pretty, tc.pretty)
			}
		})
	}
}

func TestPackageManagerFromDistro(t *testing.T) {
	cases := map[string]string{
		"arch":     "pacman",
		"manjaro":  "pacman",
		"ubuntu":   "apt",
		"debian":   "apt",
		"fedora":   "dnf",
		"rocky":    "dnf",
		"alpine":   "apk",
		"opensuse": "zypper",
		"nixos":    "nix-env",
		"void":     "xbps",
		"unknown":  "",
		"":         "",
	}
	for distro, want := range cases {
		if got := packageManagerFromDistro(distro); got != want {
			t.Errorf("packageManagerFromDistro(%q) = %q, want %q", distro, got, want)
		}
	}
}

func TestFindExecutableAndPATHProbe(t *testing.T) {
	// Create a temp dir with an executable named "pacman" on PATH.
	dir := t.TempDir()
	bin := filepath.Join(dir, "pacman")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatalf("write fake pacman: %v", err)
	}
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	if got := findExecutable("pacman"); got != bin {
		t.Errorf("findExecutable(pacman) = %q, want %q", got, bin)
	}
	// A distro-ID probe should now return the PATH hit, not the fallback.
	if got := detectPackageManager("ubuntu"); got != "pacman" {
		t.Errorf("detectPackageManager with fake pacman on PATH = %q, want %q", got, "pacman")
	}
}

func TestSanitizeVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  git version 2.47.1\n", "git version 2.47.1"},
		{"go1.26.5\r\n", "go1.26.5"},
		{"\x1b[31mred\x1b[0m 1.0", "red 1.0"},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := sanitizeVersion(tc.in); got != tc.want {
			t.Errorf("sanitizeVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Long version is capped at 60 runes.
	long := strings.Repeat("x", 100)
	got := sanitizeVersion(long)
	if len([]rune(got)) != 63 { // 60 + "..."
		t.Errorf("sanitizeVersion long cap = %d runes, want 63", len([]rune(got)))
	}
}

func TestCleanVersionLine(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"gcc", "gcc (GCC) 14.2.1 20240910", "(GCC) 14.2.1 20240910"},
		{"python3", "Python 3.13.2", "3.13.2"},
		{"git", "git version 2.47.1", "2.47.1"},
		{"node", "v22.14.0", "v22.14.0"},
		{"clang", "clang version 18.1.8", "18.1.8"},
	}
	for _, tc := range cases {
		if got := cleanVersionLine(tc.name, tc.in); got != tc.want {
			t.Errorf("cleanVersionLine(%q, %q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestVersionCore(t *testing.T) {
	cases := []struct{ in, want string }{
		{"go1.26.5-X:nodwarf5 linux/amd64", "1.26.5"},
		{"29.6.2, build dfc4efb1e2", "29.6.2"},
		{"(GCC) 16.1.1 20260625", "16.1.1"},
		{"v24.18.0", "24.18.0"},
		{"3.14.6", "3.14.6"},
		{"2.55.0", "2.55.0"},
	}
	for _, tc := range cases {
		if got := versionCore(tc.in); got != tc.want {
			t.Errorf("versionCore(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, ""},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{10 * 1024 * 1024, "10.0 MiB"},
		{15 * 1024 * 1024 * 1024, "15.0 GiB"},
		{2 * 1024 * 1024 * 1024 * 1024, "2.0 TiB"},
	}
	for _, tc := range cases {
		if got := humanizeBytes(tc.in); got != tc.want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildEnvironmentReminderFull(t *testing.T) {
	block := buildEnvironmentReminder(sampleSystemInfo(), 0)
	for _, want := range []string{
		"### SYSTEM REMINDER - RUNTIME ENVIRONMENT:",
		"- OS: linux/amd64 (Arch Linux)",
		"- Kernel: 6.18.39-1-lts",
		"- Package manager: pacman",
		"Shell: zsh",
		"Locale: en_US.UTF-8",
		"Timezone: Asia/Damascus (+03:00)",
		"User: amurru",
		"Workspace: /home/amurru/Projects/hakase",
		"Disk free (workspace): 412 GiB",
		"Memory: 15.5 GiB total",
		"Toolchains: go 1.26.5, python3 3.14.6",
		"use system_exec",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q\nblock:\n%s", want, block)
		}
	}
	// Default cap: block must fit under defaultSystemEnvMaxChars.
	if len(block) > defaultSystemEnvMaxChars {
		t.Errorf("block length %d exceeds default cap %d", len(block), defaultSystemEnvMaxChars)
	}
}

func TestBuildEnvironmentReminderOmitEmpty(t *testing.T) {
	// A minimal info with most fields empty must not emit placeholder lines.
	info := &SystemInfo{OS: "linux", Architecture: "arm64"}
	block := buildEnvironmentReminder(info, 0)
	if strings.Contains(block, "Package manager:") {
		t.Errorf("empty package manager should be omitted:\n%s", block)
	}
	if strings.Contains(block, "Kernel:") {
		t.Errorf("empty kernel should be omitted:\n%s", block)
	}
	if !strings.Contains(block, "- OS: linux/arm64") {
		t.Errorf("OS line missing:\n%s", block)
	}
}

func TestBuildEnvironmentReminderNil(t *testing.T) {
	if got := buildEnvironmentReminder(nil, 0); got != "" {
		t.Errorf("nil info should render empty, got %q", got)
	}
}

func TestBuildEnvironmentReminderTruncation(t *testing.T) {
	info := sampleSystemInfo()
	full := buildEnvironmentReminder(info, 0)
	if len(full) <= 100 {
		t.Fatalf("fixture too small for truncation test (%d chars)", len(full))
	}
	short := buildEnvironmentReminder(info, 100)
	if len(short) > 100 {
		t.Errorf("truncated block length %d exceeds cap 100", len(short))
	}
	if !strings.Contains(short, "[truncated]") {
		t.Errorf("truncated block missing marker:\n%s", short)
	}
}

func TestDistroLabel(t *testing.T) {
	// PRETTY_NAME wins.
	info := &SystemInfo{DistroID: "ubuntu", DistroVersion: "24.04", DistroCodename: "noble", DistroPretty: "Ubuntu 24.04.2 LTS"}
	if got := distroLabel(info); got != "Ubuntu 24.04.2 LTS" {
		t.Errorf("distroLabel with PRETTY_NAME = %q", got)
	}
	// Without PRETTY_NAME, compose ID + version + codename.
	info2 := &SystemInfo{DistroID: "debian", DistroVersion: "12", DistroCodename: "bookworm"}
	if got := distroLabel(info2); got != "debian 12 bookworm" {
		t.Errorf("distroLabel composed = %q", got)
	}
	if got := distroLabel(nil); got != "" {
		t.Errorf("distroLabel(nil) = %q, want empty", got)
	}
}

func TestSystemEnvEnabled(t *testing.T) {
	// Absent config = enabled.
	if !systemEnvEnabled(nil) {
		t.Error("systemEnvEnabled(nil) should default to enabled")
	}
	cfg := &Config{}
	if !systemEnvEnabled(cfg) {
		t.Error("absent enabled field should default to enabled")
	}
	f := false
	cfg.SystemEnv.Enabled = &f
	if systemEnvEnabled(cfg) {
		t.Error("enabled:false should disable")
	}
	tf := true
	cfg.SystemEnv.Enabled = &tf
	if !systemEnvEnabled(cfg) {
		t.Error("enabled:true should enable")
	}
}

func TestSystemEnvMaxChars(t *testing.T) {
	if got := systemEnvMaxChars(nil); got != defaultSystemEnvMaxChars {
		t.Errorf("systemEnvMaxChars(nil) = %d, want default %d", got, defaultSystemEnvMaxChars)
	}
	cfg := &Config{SystemEnv: SystemEnvConfig{MaxChars: 400}}
	if got := systemEnvMaxChars(cfg); got != 400 {
		t.Errorf("systemEnvMaxChars(cfg) = %d, want 400", got)
	}
}

func TestBuildEnvironmentReminderBubblewrapNote(t *testing.T) {
	info := sampleSystemInfo()
	info.ExecSandbox = string(SandboxModeBubblewrap)
	block := buildEnvironmentReminder(info, 0)
	if !strings.Contains(block, "bubblewrap sandbox") {
		t.Errorf("bubblewrap mode should add exec note:\n%s", block)
	}
	// paths/off mode carries no note.
	info.ExecSandbox = string(SandboxModePaths)
	if strings.Contains(buildEnvironmentReminder(info, 0), "bubblewrap sandbox") {
		t.Error("paths mode should not add bubblewrap note")
	}
	info.ExecSandbox = string(SandboxModeOff)
	if strings.Contains(buildEnvironmentReminder(info, 0), "bubblewrap sandbox") {
		t.Error("off mode should not add bubblewrap note")
	}
}

func TestEnvBlockApplyTo(t *testing.T) {
	block := buildEnvironmentReminder(sampleSystemInfo(), 0)
	// Empty apply_to = all agents receive the block.
	for _, agent := range []string{"orchestrator", "web_researcher", "code_interpreter", "general_purpose"} {
		if got := contextBlockFor(agent, block, nil); got != block {
			t.Errorf("nil apply_to: %s should receive block", agent)
		}
	}
	// Restricted apply_to: only the named agents receive it.
	applyTo := []string{"orchestrator"}
	if got := contextBlockFor("orchestrator", block, applyTo); got != block {
		t.Error("orchestrator should receive block with apply_to=[orchestrator]")
	}
	for _, agent := range []string{"web_researcher", "code_interpreter", "general_purpose"} {
		if got := contextBlockFor(agent, block, applyTo); got != "" {
			t.Errorf("apply_to=[orchestrator]: %s should NOT receive block", agent)
		}
	}
}

func TestEnvChanged(t *testing.T) {
	gb := uint64(1 << 30)
	cases := []struct {
		name     string
		baseline uint64
		current  uint64
		want     bool
	}{
		{"no change", gb, gb, false},
		{"small absolute under 1GiB and under 5%", 100 * gb, 100*gb + 100<<20, false}, // +100 MiB on 100 GiB (0.1%)
		{"big absolute over 1GiB", gb, gb + 2*gb, true},                                // +2 GiB
		{"5% relative at threshold", 20 * gb, 21 * gb, true},                           // +5%
		{"under 5% relative", 20 * gb, 20*gb + 900<<20, false},                         // +4.4%
		{"zero baseline nonzero current", 0, gb, true},
		{"both zero", 0, 0, false},
		{"shrink big", 3 * gb, gb, true}, // -2 GiB
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envChanged(tc.baseline, tc.current); got != tc.want {
				t.Errorf("envChanged(%d, %d) = %v, want %v", tc.baseline, tc.current, got, tc.want)
			}
		})
	}
}

func TestEnvStalenessNotice(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("staleness notice reads Linux disk/mem state")
	}
	oldInfo := currentSystemInfo
	oldBaselineSet := envBaselineSet
	oldDisk := envBaselineDiskFree
	oldMem := envBaselineMemAvail
	defer func() {
		currentSystemInfo = oldInfo
		envBaselineSet = oldBaselineSet
		envBaselineDiskFree = oldDisk
		envBaselineMemAvail = oldMem
	}()

	info := &SystemInfo{OS: "linux", Architecture: "amd64", WorkspaceRoot: t.TempDir()}
	currentSystemInfo = info
	envBaselineSet = false

	// First call seeds the baseline and returns no notice.
	if got := envStalenessNotice(); got != "" {
		t.Fatalf("baseline call should return empty, got %q", got)
	}
	// Unchanged state returns nothing.
	if got := envStalenessNotice(); got != "" {
		t.Fatalf("unchanged call should return empty, got %q", got)
	}
	// Force a large disk change and expect a notice.
	realDisk := diskFreeBytes(info.WorkspaceRoot)
	envBaselineDiskFree = realDisk + 2*(1<<30) // pretend baseline was 2 GiB higher
	if got := envStalenessNotice(); got == "" || !strings.Contains(got, "ENVIRONMENT UPDATE") {
		t.Fatalf("material disk change should produce notice, got %q", got)
	}
}

func TestDetectSystemInfoPopulatesPortableFields(t *testing.T) {
	info := detectSystemInfo("/tmp", nil)
	if info == nil {
		t.Fatal("detectSystemInfo returned nil")
	}
	if info.OS != runtime.GOOS {
		t.Errorf("OS = %q, want %q", info.OS, runtime.GOOS)
	}
	if info.Architecture != runtime.GOARCH {
		t.Errorf("Architecture = %q, want %q", info.Architecture, runtime.GOARCH)
	}
	if info.WorkspaceRoot != "/tmp" {
		t.Errorf("WorkspaceRoot = %q, want /tmp", info.WorkspaceRoot)
	}
	// The block must render without error on the host machine too.
	if block := buildEnvironmentReminder(info, 0); block == "" {
		t.Error("host environment block rendered empty")
	}
}
