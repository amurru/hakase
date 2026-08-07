// env.go - runtime environment awareness for the agent.
//
// At session start hakase detects the host it runs on (OS, architecture,
// kernel, distro, package manager, shell, locale, timezone, user/home,
// workspace, disk/memory state, available toolchains) and injects a compact
// block into every agent's system instruction so the model stops guessing:
// "install foo" on Arch resolves to pacman, system commands are grounded in
// the real linux/amd64 + kernel, and execution strategy can account for which
// compilers/interpreters actually exist.
//
// Design notes (see .omo/plans/system-environment-awareness.md):
//   - Detection is pure Go (no external deps): runtime.GOOS/GOARCH, os/user,
//     $SHELL/$LANG/$LC_ALL, time zone, and Linux-specific facts (/etc/os-release,
//     syscall.Uname, /proc/meminfo, syscall.Statfs) via build-tagged helpers.
//   - Linux is the primary platform; on darwin/windows the portable fields are
//     filled and the Linux-specific ones degrade to empty - never fatal.
//   - Package managers and toolchains are resolved by PATH availability first
//     (handles hybrids like Nix-on-Ubuntu, dnf5), with a distro-ID map fallback.
//   - Version probes run with a 1s timeout, are never fatal, and are cached per
//     session (detection runs once in setupRunner).
//   - The rendered block is a startup snapshot; live system state (processes,
//     current disk/memory, network) is deferred to system_exec.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

// defaultSystemEnvMaxChars caps the rendered environment block. Mirrors
// defaultContextFileMaxChars: content beyond the cap is truncated via
// truncateContextFile so the block can never bloat the prompt.
const defaultSystemEnvMaxChars = 800

// systemEnvBlockTokens is the token estimate of the rendered environment
// block, set once in setupRunner and folded into the compaction reserve in
// context.go (same accounting as contextBlockTokens).
var systemEnvBlockTokens int

// currentSystemInfo holds the session's detected environment (set in
// setupRunner). Read-only after startup.
var currentSystemInfo *SystemInfo

// ToolInfo describes an available compiler/interpreter/toolchain binary.
type ToolInfo struct {
	Name    string // go, gcc, clang, rustc, python3, node, git, docker
	Path    string // resolved absolute path ("" when absent)
	Version string // first line of `--version`, sanitized ("" when unknown)
}

// SystemInfo is the detected runtime environment of the host machine.
// Linux-specific fields (KernelVersion, Distro*, Memory, Disk) are empty on
// non-Linux platforms; the portable fields are always populated.
type SystemInfo struct {
	OS              string // runtime.GOOS: linux, darwin, windows
	Architecture    string // runtime.GOARCH: amd64, arm64, ...
	KernelVersion   string // syscall.Uname().Release (Linux)
	DistroID        string // /etc/os-release ID: arch, ubuntu, debian
	DistroVersion   string // /etc/os-release VERSION_ID (e.g. "24.04")
	DistroCodename  string // /etc/os-release VERSION_CODENAME (when present)
	DistroPretty    string // /etc/os-release PRETTY_NAME (e.g. "Arch Linux")
	PackageManager  string // resolved: pacman, apt, dnf, zypper, apk, brew, ...
	Shell           string // basename($SHELL): zsh, bash, fish
	Locale          string // $LC_ALL or $LANG, e.g. en_US.UTF-8
	Timezone        string // time.Now().Zone() name, e.g. Asia/Damascus
	TZOffset        string // current UTC offset, e.g. +03:00
	Username        string // current OS user
	HomeDir         string // current user's home directory
	Hostname        string // machine hostname
	WorkspaceRoot   string // cwd at startup
	DiskFreeHuman   string // free space on the workspace filesystem, humanized
	MemoryTotalHuman string // total physical memory, humanized
	MemoryAvailHuman string // currently available memory, humanized (for staleness)
	ExecSandbox     string // sandbox mode for system_exec: "paths", "bubblewrap", "off"
	Tools           []ToolInfo // available toolchains (versioned)
}

// pkgManagerCandidates is the PATH-availability probe order for package
// manager detection. apt-get is probed before apt (its core binary presence is
// the more reliable signal); brew/nix-env cover macOS and NixOS.
var pkgManagerCandidates = []string{
	"apt-get", "apt", "dnf", "yum", "pacman", "zypper", "apk", "brew", "nix-env", "port",
}

// distroPkgManagers maps /etc/os-release distro IDs to their native package
// manager, used only when no candidate is found on PATH (e.g. /usr/sbin not in
// PATH). ID_LIKE aliases are folded in (pop, linuxmint -> ubuntu).
var distroPkgManagers = map[string]string{
	"arch": "pacman", "manjaro": "pacman", "endeavouros": "pacman",
	"debian": "apt", "ubuntu": "apt", "pop": "apt", "linuxmint": "apt",
	"raspbian": "apt", "elementary": "apt", "zorin": "apt",
	"fedora": "dnf", "rhel": "dnf", "centos": "dnf", "rocky": "dnf",
	"almalinux": "dnf", "ol": "dnf", "amzn": "dnf",
	"opensuse": "zypper", "opensuse-leap": "zypper",
	"opensuse-tumbleweed": "zypper", "sles": "zypper",
	"alpine": "apk",
	"nixos": "nix-env",
	"gentoo": "emerge", "exherbo": "paludis", "void": "xbps",
}

// toolchainCandidates is the set of compilers/interpreters/VCS the agent may
// need to choose an execution strategy. Keep this list small; each entry adds a
// PATH lookup and a version probe.
var toolchainCandidates = []string{
	"go", "gcc", "clang", "rustc", "python3", "node", "git", "docker",
}

// findExecutable resolves an executable by PATH, then common sbin locations
// (some distros keep root tools out of the user PATH). Returns "" when absent.
func findExecutable(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin", "/usr/local/bin", "/usr/local/sbin"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// detectSystemInfo detects the runtime environment once per session. Failures
// in any field are degraded to empty values and logged; detection never fails
// startup. cwd is the workspace root captured at startup.
func detectSystemInfo(cwd string, log LogFunc) *SystemInfo {
	info := &SystemInfo{
		OS:            runtime.GOOS,
		Architecture:  runtime.GOARCH,
		WorkspaceRoot: cwd,
	}

	// Linux-specific facts (build-tagged helpers; empty elsewhere).
	info.KernelVersion = kernelRelease()
	info.DistroID, info.DistroVersion, info.DistroCodename, info.DistroPretty = osRelease()
	info.DiskFreeHuman = humanizeBytes(diskFreeBytes(cwd))
	info.MemoryTotalHuman = humanizeBytes(memTotalBytes())
	info.MemoryAvailHuman = humanizeBytes(memAvailableBytes())

	// The sandbox mode for system_exec is loaded before detection in
	// setupRunner (currentSandbox). A bubblewrap sandbox isolates subprocesses
	// from the host filesystem, so host-detected toolchains may be unreachable
	// inside; the rendered block notes this when it applies.
	if currentSandbox != nil {
		info.ExecSandbox = string(currentSandbox.Mode)
	}

	info.PackageManager = detectPackageManager(info.DistroID)
	info.Shell = filepath.Base(os.Getenv("SHELL"))
	info.Locale = firstNonEmpty(os.Getenv("LC_ALL"), os.Getenv("LANG"))
	if zone, offset := time.Now().Zone(); zone != "" {
		info.Timezone = zone
		info.TZOffset = time.Now().Format("-07:00")
		_ = offset
	}
	if u, err := user.Current(); err == nil {
		info.Username = u.Username
		info.HomeDir = u.HomeDir
	}
	if info.HomeDir == "" {
		if h, err := os.UserHomeDir(); err == nil {
			info.HomeDir = h
		}
	}
	if h, err := os.Hostname(); err == nil {
		info.Hostname = h
	}
	info.Tools = detectToolchains(log)
	return info
}

// detectPackageManager resolves the default package manager: first by PATH
// availability (handles hybrids and non-default installs), then by distro ID.
func detectPackageManager(distroID string) string {
	for _, name := range pkgManagerCandidates {
		if findExecutable(name) != "" {
			return name
		}
	}
	return packageManagerFromDistro(distroID)
}

// packageManagerFromDistro maps an /etc/os-release distro ID to its native
// package manager. Returns "" for unknown distros.
func packageManagerFromDistro(distroID string) string {
	if distroID == "" {
		return ""
	}
	return distroPkgManagers[strings.ToLower(distroID)]
}

// detectToolchains resolves available toolchains and their versions via
// version probes (1s timeout each, never fatal).
func detectToolchains(log LogFunc) []ToolInfo {
	var out []ToolInfo
	for _, name := range toolchainCandidates {
		path := findExecutable(name)
		if path == "" {
			continue
		}
		v := toolVersion(name, path)
		if v == "" && log != nil {
			log(fmt.Sprintf("[env] toolchain %s: version probe returned nothing", name))
		}
		out = append(out, ToolInfo{Name: name, Path: path, Version: v})
	}
	return out
}

// toolVersion probes a toolchain binary for its version. Flags differ per
// tool (`--version`, `-version`, or a bare `version` subcommand, with some
// writing to stderr), so it tries in order and returns the first non-empty
// first line, cleaned and sanitized. Bounded by a 1s timeout.
func toolVersion(name, path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, flag := range []string{"--version", "-version", "version"} {
		cmd := exec.CommandContext(ctx, path, flag)
		out, err := cmd.CombinedOutput()
		if err != nil {
			continue
		}
		line := strings.SplitN(string(out), "\n", 2)[0]
		line = versionCore(cleanVersionLine(name, line))
		if v := sanitizeVersion(line); v != "" {
			return v
		}
	}
	return ""
}

// versionCore reduces a version string to its compact semantic core so the
// toolchains line stays short and readable:
//
//	"go1.26.5-X:nodwarf5 linux/amd64" -> "1.26.5"
//	"29.6.2, build dfc4efb1e2"        -> "29.6.2"
//	"(GCC) 16.1.1"                    -> "16.1.1"
//	"v24.18.0"                        -> "24.18.0"
//
// Falls back to the input unchanged when no version token is found.
func versionCore(s string) string {
	// Drop parenthetical details and trailing build metadata.
	if i := strings.Index(s, " ("); i > 0 {
		s = s[:i]
	}
	if i := strings.Index(s, ", build"); i > 0 {
		s = s[:i]
	}
	for _, tok := range strings.Fields(s) {
		t := strings.Trim(tok, ",;")
		// Strip a short leading alphabetic prefix (go, v, clang, ...).
		u := t
		for len(u) > 0 && ((u[0] >= 'a' && u[0] <= 'z') || (u[0] >= 'A' && u[0] <= 'Z')) {
			u = u[1:]
		}
		if len(u) > 0 && u[0] >= '0' && u[0] <= '9' && strings.Contains(u, ".") {
			if j := strings.IndexByte(u, '-'); j > 0 {
				u = u[:j]
			}
			return u
		}
	}
	return s
}

// cleanVersionLine strips the tool-name prefix from a version line so the
// rendered list reads "gcc (GCC) 16.1.1" instead of "gcc gcc (GCC) 16.1.1".
// Handles case differences (python3 -> "Python 3.14.6"), a trailing digit in
// the name (python3 -> python), and a leading "version " keyword (git).
func cleanVersionLine(name, line string) string {
	if line == "" {
		return ""
	}
	lower := strings.ToLower(line)
	for _, candidate := range []string{strings.ToLower(name), strings.ToLower(strings.TrimRight(name, "0123456789"))} {
		if candidate != "" && strings.HasPrefix(lower, candidate) {
			line = strings.TrimLeft(line[len(candidate):], ": ")
			lower = strings.ToLower(line)
			break
		}
	}
	if strings.HasPrefix(lower, "version ") {
		line = line[len("version "):]
	}
	return line
}

// sanitizeVersion trims whitespace, strips ANSI escape sequences and control
// characters, and caps the result at 60 runes.
func sanitizeVersion(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := rune(s[i]), 1
		if r >= 0x80 {
			r, size = utf8.DecodeRuneInString(s[i:])
		}
		switch {
		case r == 0x1b:
			// ANSI escape: skip the introducer byte (e.g. '[' for CSI),
			// then consume until the terminating byte (0x40-0x7e).
			i++
			if i < len(s) {
				i++ // introducer
			}
			for i < len(s) {
				c := s[i]
				i++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
			continue
		case r < 0x20 || r == 0x7f:
			// Other control characters are dropped.
		default:
			b.WriteRune(r)
		}
		i += size
	}
	s = b.String()
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60]) + "..."
	}
	return s
}

// buildEnvironmentReminder renders the environment block injected into agent
// system instructions. Mirrors buildTimeReminder(): a "### SYSTEM REMINDER"
// section the model should treat as ground truth. The block is a startup
// snapshot - live system state is deferred to system_exec. Empty fields are
// omitted line-by-line. Returns "" only for a nil info.
func buildEnvironmentReminder(info *SystemInfo, maxChars int) string {
	if info == nil {
		return ""
	}
	var lines []string

	osLine := fmt.Sprintf("- OS: %s/%s", info.OS, info.Architecture)
	if label := distroLabel(info); label != "" {
		osLine += " (" + label + ")"
	}
	lines = append(lines, osLine)

	if info.KernelVersion != "" {
		lines = append(lines, "- Kernel: "+info.KernelVersion)
	}
	if info.PackageManager != "" {
		lines = append(lines, "- Package manager: "+info.PackageManager)
	}

	var sess []string
	if info.Shell != "" {
		sess = append(sess, "Shell: "+info.Shell)
	}
	if info.Locale != "" {
		sess = append(sess, "Locale: "+info.Locale)
	}
	if info.Timezone != "" {
		tz := "Timezone: " + info.Timezone
		// Omit the offset when the zone name already carries it: UTC, a
		// numeric abbreviation ("+03"), or the offset itself.
		hasOffset := info.TZOffset != "" && info.TZOffset != info.Timezone
		if hasOffset && !strings.HasPrefix(info.Timezone, "+") && !strings.HasPrefix(info.Timezone, "-") {
			tz += " (" + info.TZOffset + ")"
		}
		sess = append(sess, tz)
	}
	if len(sess) > 0 {
		lines = append(lines, "- "+strings.Join(sess, " | "))
	}

	var ident []string
	if info.Username != "" {
		ident = append(ident, "User: "+info.Username)
	}
	if info.HomeDir != "" {
		ident = append(ident, "Home: "+info.HomeDir)
	}
	if len(ident) > 0 {
		lines = append(lines, "- "+strings.Join(ident, " | "))
	}
	if info.WorkspaceRoot != "" {
		lines = append(lines, "- Workspace: "+info.WorkspaceRoot)
	}

	var res []string
	if info.DiskFreeHuman != "" {
		res = append(res, "Disk free (workspace): "+info.DiskFreeHuman)
	}
	if info.MemoryTotalHuman != "" {
		res = append(res, "Memory: "+info.MemoryTotalHuman+" total")
	}
	if len(res) > 0 {
		lines = append(lines, "- "+strings.Join(res, " | "))
	}

	if info.ExecSandbox == string(SandboxModeBubblewrap) {
		lines = append(lines, "- Note: system commands run inside a bubblewrap sandbox; host toolchains above may be unreachable there - prefer python_interpreter for code execution")
	}

	if len(info.Tools) > 0 {
		tools := make([]string, 0, len(info.Tools))
		for _, t := range info.Tools {
			if t.Version != "" {
				tools = append(tools, t.Name+" "+t.Version)
			} else {
				tools = append(tools, t.Name)
			}
		}
		lines = append(lines, "- Toolchains: "+strings.Join(tools, ", "))
	}

	block := "\n### SYSTEM REMINDER - RUNTIME ENVIRONMENT:\n" +
		"You run on the user's machine. Environment snapshot taken at session start; for live system state (processes, current disk/memory, network) use system_exec:\n" +
		strings.Join(lines, "\n") +
		"\n- When installing software or running system commands, prefer the package manager and toolchain versions above over assumptions."
	if maxChars <= 0 {
		maxChars = defaultSystemEnvMaxChars
	}
	return truncateContextFile(block, maxChars)
}

// distroLabel composes the human-readable distro identity for the OS line:
// PRETTY_NAME when present (it already carries version/codename), otherwise
// ID + VERSION_ID + VERSION_CODENAME joined.
func distroLabel(info *SystemInfo) string {
	if info == nil {
		return ""
	}
	if info.DistroPretty != "" {
		return info.DistroPretty
	}
	parts := make([]string, 0, 3)
	if info.DistroID != "" {
		parts = append(parts, info.DistroID)
	}
	if info.DistroVersion != "" {
		parts = append(parts, info.DistroVersion)
	}
	if info.DistroCodename != "" {
		parts = append(parts, info.DistroCodename)
	}
	return strings.Join(parts, " ")
}

// Summary returns a one-line environment summary for the TUI status/log pane,
// e.g. "linux/amd64 (Arch Linux), pacman".
func (s *SystemInfo) Summary() string {
	if s == nil {
		return "unknown"
	}
	osLine := fmt.Sprintf("%s/%s", s.OS, s.Architecture)
	if label := distroLabel(s); label != "" {
		osLine += " (" + label + ")"
	}
	if s.PackageManager != "" {
		osLine += ", " + s.PackageManager
	}
	return osLine
}

// humanizeBytes formats a byte count with binary units (KiB/MiB/GiB/TiB).
// Returns "" for 0.
func humanizeBytes(b uint64) string {
	if b == 0 {
		return ""
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// firstNonEmpty returns the first non-empty argument.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Environment staleness: the env block is a startup snapshot, but disk free
// and available memory drift as the agent works. Following the Codex world-
// state diff pattern, envStalenessNotice compares live values against the last
// reported baseline (seeded at the first model call) and emits a one-shot
// notice only when a value changed materially, so the model never acts on a
// stale "disk free: 412 GiB" from hours ago without a hint to re-check.
// ---------------------------------------------------------------------------

// envStalenessMinBytes is the absolute change (in bytes) that triggers a
// staleness notice regardless of relative size.
const envStalenessMinBytes = uint64(1 << 30) // 1 GiB

// envStalenessMinRatio is the relative change threshold (0.05 = 5%).
const envStalenessMinRatio = 0.05

// Session-scoped staleness baseline, seeded on the first envStalenessNotice
// call (the first model call of the session, via BeforeModelCallback).
var (
	envBaselineSet      bool
	envBaselineDiskFree uint64
	envBaselineMemAvail uint64
)

// envStalenessNotice compares live disk-free and available-memory against the
// session baseline. On the first call it only records the baseline. On a
// material change it returns a one-shot notice for the model (and updates the
// baseline so the notice fires at most once per change). Returns "" when
// nothing changed or detection is unavailable (non-Linux, no workspace).
func envStalenessNotice() string {
	if currentSystemInfo == nil || currentSystemInfo.WorkspaceRoot == "" {
		return ""
	}
	diskFree := diskFreeBytes(currentSystemInfo.WorkspaceRoot)
	memAvail := memAvailableBytes()

	if !envBaselineSet {
		envBaselineSet = true
		envBaselineDiskFree = diskFree
		envBaselineMemAvail = memAvail
		return ""
	}

	var changes []string
	if envChanged(envBaselineDiskFree, diskFree) {
		changes = append(changes, fmt.Sprintf("disk free: %s -> %s",
			humanizeBytes(envBaselineDiskFree), humanizeBytes(diskFree)))
		envBaselineDiskFree = diskFree
	}
	if envChanged(envBaselineMemAvail, memAvail) {
		changes = append(changes, fmt.Sprintf("available memory: %s -> %s",
			humanizeBytes(envBaselineMemAvail), humanizeBytes(memAvail)))
		envBaselineMemAvail = memAvail
	}
	if len(changes) == 0 {
		return ""
	}
	return "\n### ENVIRONMENT UPDATE:\n" +
		"The system state changed since the environment snapshot at session start: " +
		strings.Join(changes, "; ") +
		". Re-check live state with system_exec before relying on the old values."
}

// envChanged reports whether the current value drifted materially from the
// baseline: at least envStalenessMinBytes absolute, or envStalenessMinRatio
// relative. A zero baseline (unavailable measurement) only reports on nonzero
// current values, so a measurement coming online is not treated as a change.
func envChanged(baseline, current uint64) bool {
	if baseline == current {
		return false
	}
	if baseline == 0 {
		return current != 0
	}
	delta := current - min(current, baseline)
	if baseline > current {
		delta = baseline - current
	}
	if delta >= envStalenessMinBytes {
		return true
	}
	return float64(delta)/float64(baseline) >= envStalenessMinRatio
}
