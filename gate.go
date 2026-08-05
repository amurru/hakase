package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/shlex"
)

// CommandRisk classifies a parsed command's danger level.
type CommandRisk int

const (
	RiskLow CommandRisk = iota
	RiskMedium
	RiskHigh
	RiskUnknown
)

func (r CommandRisk) String() string {
	switch r {
	case RiskLow:
		return "low"
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "high"
	case RiskUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// GateAction is the policy outcome.
type GateAction string

const (
	ActionAllow GateAction = "allow" // run without asking
	ActionAsk   GateAction = "ask"   // require user approval
	ActionDeny  GateAction = "deny"  // hard block, never run
)

// GateDecision is the outcome of evaluating one command.
type GateDecision struct {
	Action GateAction
	Risk   CommandRisk
	Reason string // human-readable reason for deny/ask
}

// parseCommandArgv splits a shell command line into argv the way the shell
// would evaluate it (quote removal, whitespace). Returns nil on parse error.
func parseCommandArgv(command string) []string {
	argv, err := shlex.Split(command)
	if err != nil {
		return nil
	}
	return argv
}

// detectShellTricks returns a list of evasion techniques present in the raw
// command line. Empty when clean.
func detectShellTricks(command string) []string {
	var tricks []string

	// Chaining: && ; | (but not || which is also chaining)
	if strings.Contains(command, "&&") || strings.Contains(command, ";") {
		// Avoid flagging pipe alone as chaining - check for multi-command patterns
		tricks = append(tricks, "chaining")
	}
	// Subshell
	if strings.Contains(command, "$(") || strings.Contains(command, "${") {
		// ${} is parameter expansion, not subshell. Only $( ... ) is subshell.
		// Check for $( pattern specifically.
		if strings.Contains(command, "$(") {
			tricks = append(tricks, "subshell")
		}
	}
	// Backticks
	if strings.Contains(command, "`") {
		tricks = append(tricks, "backticks")
	}
	// IFS tricks
	if strings.Contains(command, "$IFS") || strings.Contains(command, "${IFS}") {
		tricks = append(tricks, "ifs")
	}
	// Heredoc
	if strings.Contains(command, "<<") && !strings.Contains(command, "<<<") {
		tricks = append(tricks, "heredoc")
	}
	// Piped-shell
	if matched, _ := regexp.MatchString(`(curl|wget)\s+.*\|\s*(sh|bash)`, command); matched {
		tricks = append(tricks, "piped-shell")
	}
	if matched, _ := regexp.MatchString(`base64\s+.*\|\s*(sh|bash)`, command); matched {
		tricks = append(tricks, "piped-shell")
	}
	if matched, _ := regexp.MatchString(`python\d?\s+-c\s+.*\|\s*(sh|bash)`, command); matched {
		tricks = append(tricks, "piped-shell")
	}
	// Env-prefix: FOO=bar cmd (but not inside quoted strings)
	if matched, _ := regexp.MatchString(`^\w+=\S+\s+\S+`, strings.TrimSpace(command)); matched {
		tricks = append(tricks, "env-prefix")
	}
	// Redirect to device
	if matched, _ := regexp.MatchString(`>\s*/dev/sd[a-z]`, command); matched {
		tricks = append(tricks, "redirect")
	}
	if matched, _ := regexp.MatchString(`>\s*/dev/disk`, command); matched {
		tricks = append(tricks, "redirect")
	}

	return tricks
}

// classifyRisk maps argv[0] (basename, lowercased) to a risk level. See the
// risk table in the contract. Unknown binaries -> RiskUnknown.
func classifyRisk(argv []string) CommandRisk {
	if len(argv) == 0 {
		return RiskUnknown
	}

	basename := strings.ToLower(filepath.Base(argv[0]))

	// HIGH risk binaries (check first - destructive/privileged)
	highRisk := map[string]bool{
		"rm": true, "rmdir": true, "dd": true, "mkfs": true,
		"fdisk": true, "parted": true, "shred": true, "format": true,
		"sudo": true, "su": true, "doas": true, "killall": true, "pkill": true,
		"truncate": true, "reboot": true, "shutdown": true, "poweroff": true,
		"halt": true, "init": true, "mount": true, "umount": true,
		"chroot": true, "mkswap": true, "swapon": true, "swapoff": true,
		"blkdiscard": true,
		"mkfs.ext2":  true, "mkfs.ext3": true, "mkfs.ext4": true,
		"mkfs.xfs": true, "mkfs.btrfs": true, "mkfs.vfat": true,
		"mkfs.fat": true, "mkfs.ntfs": true, "mkfs.exfat": true,
	}

	// MEDIUM risk binaries
	mediumRisk := map[string]bool{
		"touch": true, "mkdir": true, "cp": true, "mv": true,
		"chmod": true, "chown": true, "ln": true,
		"tar": true, "zip": true, "unzip": true, "gzip": true,
		"xz": true, "bzip2": true,
		"pip": true, "pip3": true, "npm": true, "npx": true,
		"apt": true, "apt-get": true, "dnf": true, "yum": true,
		"pacman": true, "go": true, "cargo": true, "make": true,
		"cmake": true, "curl": true, "wget": true,
		"python": true, "python3": true, "node": true,
		"ruby": true, "perl": true, "php": true,
		"tee": true, "systemctl": true, "service": true,
		"docker": true, "podman": true, "nohup": true,
	}

	// LOW risk binaries
	lowRisk := map[string]bool{
		"ls": true, "cat": true, "echo": true, "true": true, "false": true,
		"grep": true, "head": true, "tail": true, "wc": true,
		"sort": true, "uniq": true, "which": true, "whereis": true,
		"pwd": true, "printf": true, "date": true, "stat": true,
		"du": true, "df": true, "env": true, "printenv": true,
		"id": true, "whoami": true, "hostname": true, "uname": true,
		"file": true, "diff": true, "cmp": true, "md5sum": true, "sha256sum": true,
	}

	// mkfs.* prefix match
	if strings.HasPrefix(basename, "mkfs.") {
		return RiskHigh
	}

	// rm* prefix match (rm, rmdir already in highRisk, but this catches
	// any rm-derived binary)
	if strings.HasPrefix(basename, "rm") && !highRisk[basename] {
		return RiskHigh
	}

	if highRisk[basename] {
		return RiskHigh
	}

	// Git special handling: risk depends on subcommand and flags.
	if basename == "git" {
		return classifyGitRisk(argv)
	}

	// Sed special handling: -i flag upgrades to MEDIUM.
	if basename == "sed" {
		if hasFlag(argv, "-i") || hasFlag(argv, "--in-place") {
			return RiskMedium
		}
		return RiskLow
	}

	// Awk special handling: -i flag upgrades to MEDIUM.
	if basename == "awk" {
		if hasFlag(argv, "-i") || hasFlag(argv, "--in-place") {
			return RiskMedium
		}
		return RiskLow
	}

	// Tar special handling: list operation -> LOW, otherwise MEDIUM.
	if basename == "tar" {
		if hasTarListFlag(argv) {
			return RiskLow
		}
		return RiskMedium
	}

	// Kill special handling: -9 -> HIGH, otherwise MEDIUM.
	if basename == "kill" {
		if hasFlag(argv, "-9") || hasFlag(argv, "-KILL") || hasFlag(argv, "-SIGKILL") {
			return RiskHigh
		}
		return RiskMedium
	}

	// chmod -R 777 / -> HIGH (also caught by hard-deny).
	// General chmod -> MEDIUM.
	if basename == "chmod" {
		if hasRecursiveChmod777(argv) {
			return RiskHigh
		}
		return RiskMedium
	}

	// chown -R / -> HIGH (also caught by hard-deny).
	// General chown -> MEDIUM.
	if basename == "chown" {
		if hasRecursiveFlag(argv) && hasRootTarget(argv) {
			return RiskHigh
		}
		return RiskMedium
	}

	if mediumRisk[basename] {
		return RiskMedium
	}

	if lowRisk[basename] {
		return RiskLow
	}

	return RiskUnknown
}

// classifyGitRisk returns risk level for git subcommands based on argv.
func classifyGitRisk(argv []string) CommandRisk {
	if len(argv) < 2 {
		// plain "git" without subcommand is harmless
		return RiskLow
	}

	sub := argv[1]
	// Strip leading dashes in case of --version or similar
	sub = strings.TrimLeft(sub, "-")

	// HIGH risk git operations.
	if sub == "push" {
		if hasForceFlag(argv) {
			return RiskHigh
		}
	}
	if sub == "reset" {
		if hasFlag(argv, "--hard") {
			return RiskHigh
		}
	}
	if sub == "clean" {
		if hasFlag(argv, "-fdx") || hasFlag(argv, "-dfx") || hasFlag(argv, "-xdf") ||
			hasFlag(argv, "-xfd") || (hasFlag(argv, "-f") && hasFlag(argv, "-d") && hasFlag(argv, "-x")) {
			return RiskHigh
		}
	}
	if sub == "checkout" {
		if hasFlag(argv, "--force") || hasFlag(argv, "-f") {
			return RiskHigh
		}
	}

	// LOW risk git subcommands (read-only).
	lowGitSubs := map[string]bool{
		"status": true, "log": true, "diff": true, "show": true,
		"branch": true, "remote": true,
	}
	if lowGitSubs[sub] {
		return RiskLow
	}

	// MEDIUM risk git subcommands (add, commit, push without force,
	// reset, checkout, merge, and everything else).
	return RiskMedium
}

// hasForceFlag checks argv for --force, -f, --force-with-lease.
func hasForceFlag(argv []string) bool {
	for _, a := range argv {
		if a == "--force" || a == "-f" || a == "--force-with-lease" {
			return true
		}
	}
	return false
}

// hasFlag checks whether a specific flag appears in argv.
func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

// hasTarListFlag checks whether tar argv contains a list-only operation (-t, --list).
func hasTarListFlag(argv []string) bool {
	for _, a := range argv {
		if a == "-t" || a == "--list" {
			return true
		}
		// Combined short flags like -tvf, -xvf, etc.
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			if strings.Contains(a, "t") {
				return true
			}
		}
	}
	return false
}

// hasRecursiveFlag checks argv for -R, -r, --recursive.
func hasRecursiveFlag(argv []string) bool {
	for _, a := range argv {
		if a == "-R" || a == "-r" || a == "--recursive" {
			return true
		}
	}
	return false
}

// hasRootTarget checks if argv contains a root path target.
func hasRootTarget(argv []string) bool {
	for _, a := range argv {
		if a == "/" {
			return true
		}
	}
	return false
}

// hasRecursiveChmod777 checks for chmod -R 777 targeting root paths.
func hasRecursiveChmod777(argv []string) bool {
	foundRecursive := false
	found777 := false
	for _, a := range argv {
		if a == "-R" || a == "-r" || a == "--recursive" {
			foundRecursive = true
		}
		if a == "777" {
			found777 = true
		}
	}
	if !foundRecursive || !found777 {
		return false
	}
	for _, a := range argv {
		if a == "/" || a == "/etc" || a == "/usr" || a == "/boot" ||
			a == "/var" || a == "/home" || a == "/root" {
			return true
		}
	}
	return false
}

// ---- Hard-deny ---- //

// systemRoots is the set of paths that trigger hard-deny when targeted by
// recursive deletion.
var systemRoots = []string{
	"/", "~", "$HOME", "${HOME}",
	"/root", "/home", "/etc", "/boot", "/usr", "/var", "/dev",
}

// systemRootSet for O(1) lookup.
var systemRootSet = func() map[string]bool {
	m := make(map[string]bool, len(systemRoots))
	for _, r := range systemRoots {
		m[r] = true
	}
	return m
}()

// forbiddenGlobs are forbidden target patterns for rm.
var forbiddenGlobs = []string{"/.*", "/*"}

// devicePatterns are device paths that trigger hard-deny when targeted
// by dd, mkfs, shred, fdisk, parted, or redirect.
var devicePrefixes = []string{
	"/dev/sd", "/dev/nvme", "/dev/mmcblk", "/dev/disk",
}

// hardDenyReason returns a non-empty reason when the command matches the
// hard-deny circuit-breaker set. Substitution-aware: also scans the raw
// command string inside $(), backticks, and $VAR references.
// "" means not hard-denied.
func hardDenyReason(command string, argv []string) string {
	// Collect all views of the command: the raw command, normalized,
	// plus extracted $(...) and backtick subcommands.
	views := collectViews(command)

	if reason := checkHardDeny(argv, views); reason != "" {
		return reason
	}

	// Check the raw command string with $IFS normalization for bypass attempts.
	normalized := normalizeShellEvasion(command)
	if normalized != command {
		if reason := checkHardDeny(nil, []string{normalized}); reason != "" {
			return reason + " (detected after shell evasion normalization)"
		}
	}

	return ""
}

// collectViews extracts all command views from the raw string:
// the raw command, $(...) subcommands, backtick subcommands.
func collectViews(command string) []string {
	var views []string
	views = append(views, command)

	// Extract $(...) subcommands (non-greedy, supports nested by
	// extracting innermost first).
	subshellRe := regexp.MustCompile(`\$\(([^()]*(?:\([^()]*\)[^()]*)*)\)`)
	for _, match := range subshellRe.FindAllStringSubmatch(command, -1) {
		if len(match) > 1 {
			views = append(views, match[1])
		}
	}

	// Extract backtick subcommands.
	backtickRe := regexp.MustCompile("`([^`]*)`")
	for _, match := range backtickRe.FindAllStringSubmatch(command, -1) {
		if len(match) > 1 {
			views = append(views, match[1])
		}
	}

	return views
}

// checkHardDeny runs hard-deny checks against argv and a set of
// normalized command-string views.
func checkHardDeny(argv []string, views []string) string {
	// Check parsed argv if available.
	if len(argv) > 0 {
		bin := strings.ToLower(filepath.Base(argv[0]))

		// 1. Recursive deletion of system/home roots.
		if bin == "rm" {
			if reason := checkRmHardDeny(argv); reason != "" {
				return reason
			}
		}

		// 2. Raw device destruction.
		if reason := checkDeviceDestruction(argv); reason != "" {
			return reason
		}

		// 4. chmod -R 777 /, chown -R <user> /.
		if bin == "chmod" && hasRecursiveChmod777(argv) {
			return "recursive world-writable permission on system directory"
		}
		if bin == "chown" && hasRecursiveFlag(argv) && hasRootTarget(argv) {
			return "recursive ownership change on root filesystem"
		}

		// Fork bombs in argv: :(){ :|:& };:
		if reason := detectForkBomb(argv); reason != "" {
			return reason
		}
	}

	// Check each view (normalized raw commands) for hard-deny patterns.
	for _, view := range views {
		normalized := normalizeShellEvasion(view)

		// Fork bomb detection in strings.
		if reason := detectForkBombInString(normalized); reason != "" {
			return reason
		}

		// Check for rm -rf patterns in views.
		if reason := checkRmInString(normalized); reason != "" {
			return reason
		}

		// Check for device destruction in strings.
		if reason := checkDeviceInString(normalized); reason != "" {
			return reason
		}

		// Check for chmod -R 777 / and chown -R / in strings.
		if reason := checkChmodChownInString(normalized); reason != "" {
			return reason
		}
	}

	return ""
}

// normalizeShellEvasion replaces common shell evasion tricks:
// $IFS -> space, ${IFS} -> space, $'\t'/etc -> space,
// $HOME -> path marker, ~ -> path marker.
func normalizeShellEvasion(s string) string {
	s = strings.ReplaceAll(s, "${IFS}", " ")
	s = strings.ReplaceAll(s, "$IFS", " ")
	s = strings.ReplaceAll(s, "$'\\t'", " ")
	s = strings.ReplaceAll(s, "$'\\n'", " ")
	s = strings.ReplaceAll(s, "$'\\r'", " ")
	s = strings.ReplaceAll(s, "$'\\x20'", " ")
	s = strings.ReplaceAll(s, "${HOME}", "/home/user")
	s = strings.ReplaceAll(s, "$HOME", "/home/user")
	s = strings.ReplaceAll(s, "~", "/home/user")
	return s
}

// checkRmHardDeny checks argv for rm -rf targeting forbidden paths.
func checkRmHardDeny(argv []string) string {
	// Must have recursive flag (-r, -R, --recursive) AND force flag (-f, --force).
	hasRecursive := false
	hasForce := false
	var targets []string

	for _, a := range argv[1:] {
		if a == "-r" || a == "-R" || a == "--recursive" {
			hasRecursive = true
		} else if a == "-f" || a == "--force" {
			hasForce = true
		} else if strings.HasPrefix(a, "-") && len(a) > 1 && !strings.HasPrefix(a, "--") {
			// Combined short flags like -rf, -fr.
			if strings.Contains(a, "r") || strings.Contains(a, "R") {
				hasRecursive = true
			}
			if strings.Contains(a, "f") {
				hasForce = true
			}
		} else if !strings.HasPrefix(a, "-") {
			targets = append(targets, a)
		}
	}

	if !hasRecursive || !hasForce {
		return ""
	}

	for _, t := range targets {
		// Check exact match against system roots.
		if systemRootSet[t] {
			return fmt.Sprintf("recursive force-deletion of %s", t)
		}
		// Expand ~ and $HOME variants.
		expanded := normalizeShellEvasion(t)
		if systemRootSet[expanded] {
			return fmt.Sprintf("recursive force-deletion of %s", t)
		}
		// Check /* and /.* patterns.
		for _, glob := range forbiddenGlobs {
			matched, _ := filepath.Match(glob, t)
			if matched {
				return fmt.Sprintf("recursive force-deletion with glob %s", t)
			}
		}
		// Check if target is a device.
		for _, prefix := range devicePrefixes {
			if strings.HasPrefix(t, prefix) {
				return fmt.Sprintf("recursive force-deletion of device %s", t)
			}
		}
	}

	return ""
}

// checkDeviceDestruction checks argv for device destruction patterns
// (dd, mkfs, shred, fdisk, parted).
func checkDeviceDestruction(argv []string) string {
	bin := strings.ToLower(filepath.Base(argv[0]))

	switch bin {
	case "dd":
		for _, a := range argv[1:] {
			if strings.HasPrefix(a, "of=") {
				target := strings.TrimPrefix(a, "of=")
				for _, prefix := range devicePrefixes {
					if strings.HasPrefix(target, prefix) {
						return fmt.Sprintf("raw device write to %s via dd", target)
					}
				}
			}
		}
	case "mkfs", "mkfs.ext2", "mkfs.ext3", "mkfs.ext4",
		"mkfs.xfs", "mkfs.btrfs", "mkfs.vfat", "mkfs.fat",
		"mkfs.ntfs", "mkfs.exfat":
		for _, a := range argv[1:] {
			for _, prefix := range devicePrefixes {
				if strings.HasPrefix(a, prefix) {
					return fmt.Sprintf("filesystem creation on device %s", a)
				}
			}
		}
	case "shred":
		for _, a := range argv[1:] {
			for _, prefix := range devicePrefixes {
				if strings.HasPrefix(a, prefix) {
					return fmt.Sprintf("secure deletion of device %s", a)
				}
			}
		}
	case "fdisk", "parted":
		for _, a := range argv[1:] {
			for _, prefix := range devicePrefixes {
				if strings.HasPrefix(a, prefix) {
					return fmt.Sprintf("partition table modification on device %s", a)
				}
			}
		}
	}

	return ""
}

// detectForkBomb checks argv for fork bomb patterns.
func detectForkBomb(argv []string) string {
	joined := strings.Join(argv, " ")
	if isForkBomb(joined) {
		return "fork bomb detected"
	}
	return ""
}

// detectForkBombInString checks a string for fork bomb patterns.
func detectForkBombInString(s string) string {
	if isForkBomb(s) {
		return "fork bomb detected"
	}
	return ""
}

// isForkBomb checks for the classic fork bomb pattern.
func isForkBomb(s string) bool {
	// Match: :(){ :|:& };:  or  :(){ :|: & };:
	s = strings.ReplaceAll(s, " ", "")
	return strings.Contains(s, ":(){:|:&};:") ||
		strings.Contains(s, ":(){:|:&};:")
}

// checkRmInString checks a normalized string for rm -rf targeting forbidden paths.
func checkRmInString(s string) string {
	// Look for rm with recursive and force flags targeting system paths.
	rmRe := regexp.MustCompile(`rm\s+((?:-[^\s]*[rR][^\s]*f[^\s]*\s+)|(?:-[^\s]*f[^\s]*[rR][^\s]*\s+)|(?:--recursive\s+--force\s+)|(?:--force\s+--recursive\s+)|(?:-r\s+-f\s+)|(?:-f\s+-r\s+)|(?:-R\s+-f\s+)|(?:-f\s+-R\s+))(.+)`)
	matches := rmRe.FindStringSubmatch(s)
	if len(matches) < 3 {
		return ""
	}
	tail := matches[2]
	targets := strings.Fields(tail)
	for _, t := range targets {
		if systemRootSet[t] {
			return fmt.Sprintf("recursive force-deletion of %s", t)
		}
		for _, glob := range forbiddenGlobs {
			matched, _ := filepath.Match(glob, t)
			if matched {
				return fmt.Sprintf("recursive force-deletion with glob %s", t)
			}
		}
		for _, prefix := range devicePrefixes {
			if strings.HasPrefix(t, prefix) {
				return fmt.Sprintf("recursive force-deletion of device %s", t)
			}
		}
	}
	return ""
}

// checkDeviceInString checks a normalized string for device destruction patterns.
func checkDeviceInString(s string) string {
	// dd of=/dev/sd* pattern.
	ddRe := regexp.MustCompile(`dd\s+.*\bof=(/dev/(?:sd[a-z]\d*|nvme\d+n\d+|mmcblk\d+p?\d*|disk\S*))`)
	if m := ddRe.FindStringSubmatch(s); len(m) > 1 {
		return fmt.Sprintf("raw device write to %s via dd", m[1])
	}

	// mkfs targeting /dev/* pattern.
	mkfsRe := regexp.MustCompile(`mkfs\S*\s+.*(/dev/(?:sd[a-z]\d*|nvme\d+n\d+|mmcblk\d+p?\d*|disk\S*))`)
	if m := mkfsRe.FindStringSubmatch(s); len(m) > 1 {
		return fmt.Sprintf("filesystem creation on device %s", m[1])
	}

	// shred targeting /dev/* pattern.
	shredRe := regexp.MustCompile(`shred\s+.*(/dev/(?:sd[a-z]\d*|nvme\d+n\d+|mmcblk\d+p?\d*|disk\S*))`)
	if m := shredRe.FindStringSubmatch(s); len(m) > 1 {
		return fmt.Sprintf("secure deletion of device %s", m[1])
	}

	// fdisk/parted targeting /dev/* pattern.
	fdiskRe := regexp.MustCompile(`(?:fdisk|parted)\s+.*(/dev/(?:sd[a-z]\d*|nvme\d+n\d+|mmcblk\d+p?\d*|disk\S*))`)
	if m := fdiskRe.FindStringSubmatch(s); len(m) > 1 {
		return fmt.Sprintf("partition table modification on device %s", m[1])
	}

	// Redirect to device: > /dev/sd*
	redirectRe := regexp.MustCompile(`[>]+\s*(/dev/(?:sd[a-z]\d*|nvme\d+n\d+|mmcblk\d+p?\d*|disk\S*))`)
	if m := redirectRe.FindStringSubmatch(s); len(m) > 1 {
		return fmt.Sprintf("redirect to device %s", m[1])
	}

	return ""
}

// checkChmodChownInString checks a normalized string for chmod/chown -R on root.
func checkChmodChownInString(s string) string {
	// chmod -R 777 / (or /etc, /usr).
	chmodRe := regexp.MustCompile(`chmod\s+[^ ]*[rR][^ ]*\s+777\s+(/\S*)`)
	if m := chmodRe.FindStringSubmatch(s); len(m) > 1 {
		target := m[1]
		for _, root := range []string{"/", "/etc", "/usr", "/boot", "/var", "/home", "/root"} {
			if target == root || strings.HasPrefix(target, root+"/") {
				// Only flag if target is exactly a system root OR a direct subpath
				// of /etc, /usr, /boot, /var, /home, /root.
				// But per the contract: chmod -R 777 /, /etc, /usr specifically.
				// Let's be strict: only exact matches.
				if target == root {
					return "recursive world-writable permission on system directory"
				}
			}
		}
	}

	// chown -R <user> /.
	chownRe := regexp.MustCompile(`chown\s+[^ ]*[rR][^ ]*\s+\S+\s+(/\S*)`)
	if m := chownRe.FindStringSubmatch(s); len(m) > 1 {
		target := m[1]
		if target == "/" {
			return "recursive ownership change on root filesystem"
		}
	}

	return ""
}

// effectiveRiskThreshold returns the ask-threshold for the given sandbox:
// - explicit sb.RiskThreshold ("low"|"medium"|"high"|"unknown") when set
// - otherwise bubblewrap mode -> RiskHigh, paths/off/nil -> RiskMedium
func effectiveRiskThreshold(sb *SandboxConfig) CommandRisk {
	if sb != nil && sb.RiskThreshold != "" {
		switch strings.ToLower(sb.RiskThreshold) {
		case "low":
			return RiskLow
		case "medium":
			return RiskMedium
		case "high":
			return RiskHigh
		case "unknown":
			return RiskUnknown
		default:
			return RiskMedium
		}
	}
	if sb == nil || sb.Mode == SandboxModeOff || sb.Mode == SandboxModePaths {
		return RiskMedium
	}
	// Bubblewrap -> RiskHigh.
	return RiskHigh
}

// evaluateCommand applies the full policy to one system command.
// argv is parseCommandArgv(command) when len(args)==0, else
// append([]string{command}, args...).
func evaluateCommand(sb *SandboxConfig, command string, args []string) GateDecision {
	var argv []string
	if len(args) == 0 {
		argv = parseCommandArgv(command)
		if argv == nil || len(argv) == 0 {
			// Parse failed or empty; treat as unknown risk.
			return GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: "unable to parse command"}
		}
	} else {
		argv = make([]string, 0, len(args)+1)
		argv = append(argv, command)
		argv = append(argv, args...)
	}

	// 1. Permission: sb.permitted("system_exec") == "deny" -> ActionDeny.
	if sb != nil {
		if action, ok := sb.permitted("system_exec"); ok && action == "deny" {
			return GateDecision{Action: ActionDeny, Risk: RiskUnknown, Reason: "system_exec is denied by sandbox permissions"}
		}
	}

	// 2. Hard-deny circuit breaker.
	if reason := hardDenyReason(command, argv); reason != "" {
		// Determine risk for logging; use HIGH for hard-denied commands.
		risk := classifyRisk(argv)
		debugWarn("gate_hard_deny", "command", command, "reason", reason, "risk", risk.String())
		return GateDecision{Action: ActionDeny, Risk: risk, Reason: reason}
	}

	// 3. DenyPatterns regex match against raw command line.
	if sb != nil {
		for _, pattern := range sb.DenyPatterns {
			matched, err := regexp.MatchString(pattern, command)
			if err == nil && matched {
				return GateDecision{Action: ActionDeny, Risk: classifyRisk(argv), Reason: fmt.Sprintf("command matches deny pattern: %s", pattern)}
			}
		}
	}

	// 4. Allowlist: if AllowedCommands is non-empty and argv[0] basename not in it -> ActionDeny.
	if sb != nil && len(sb.AllowedCommands) > 0 {
		bin := filepath.Base(argv[0])
		allowed := false
		for _, a := range sb.AllowedCommands {
			if a == bin {
				allowed = true
				break
			}
		}
		if !allowed {
			return GateDecision{Action: ActionDeny, Risk: classifyRisk(argv), Reason: fmt.Sprintf("command %q is not in the allowlist", bin)}
		}
	}

	risk := classifyRisk(argv)

	// 5. Risk == RiskUnknown -> ActionAsk (fail-closed on unknown binaries).
	// Per semantics note: RiskUnknown ALWAYS asks, regardless of permission mode.
	if risk == RiskUnknown {
		return GateDecision{Action: ActionAsk, Risk: risk, Reason: "unknown command requires user approval"}
	}

	// 6. Permission "allow" -> ActionAllow (hard-deny still applied at step 2).
	if sb != nil {
		if action, ok := sb.permitted("system_exec"); ok && action == "allow" {
			return GateDecision{Action: ActionAllow, Risk: risk, Reason: ""}
		}
	}

	// 7. Risk >= effectiveRiskThreshold(sb) -> ActionAsk.
	threshold := effectiveRiskThreshold(sb)
	if risk >= threshold {
		return GateDecision{Action: ActionAsk, Risk: risk, Reason: fmt.Sprintf("command classified as %s risk (threshold: %s)", risk.String(), threshold.String())}
	}

	// 8. Otherwise -> ActionAllow.
	return GateDecision{Action: ActionAllow, Risk: risk, Reason: ""}
}
