package agent

import (
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/util"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
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
	// PolicyRule cites the permissions rule behind an ActionDeny/Ask
	// from step 4b (nil when the risk gate decided on its own).
	PolicyRule *permissions.Rule
}

// ruleSuffix renders the matching permissions rule for gate reasons
// (audit metadata). Empty when the default applied.
func ruleSuffix(rule *permissions.Rule) string {
	if rule == nil {
		return ""
	}
	return fmt.Sprintf(" (rule: %s %q -> %s)", rule.Action, rule.Resource, rule.Effect)
}

// atFileOperands extracts best-effort @-file body operands from a shell
// command line and its args (M4): tokens starting with "@" whose tail
// looks like a path (contains "/" or "."). Quoting and trailing commas
// are stripped. Never errors: unparseable input yields no operands.
func atFileOperands(command string, args []string) []string {
	var toks []string
	toks = append(toks, strings.Fields(command)...)
	toks = append(toks, args...)
	var out []string
	for _, tok := range toks {
		t := strings.Trim(strings.TrimSpace(tok), "\"',")
		if len(t) < 2 || !strings.HasPrefix(t, "@") {
			continue
		}
		p := t[1:]
		if p == "" || strings.ContainsAny(p, " \t\n<>|&;()") {
			continue
		}
		if !strings.ContainsAny(p, "/.") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// checkAtFileFlow applies the best-effort @-file body operand rule: a
// shell command referencing a file the flow policy denies (or asks about
// on a narrowed label) escalates here. Returns nil when no operand
// escalates. Flow only escalates, never widens. M6: operands match via
// the dual canonical+raw legs (flowPathResources) and the fired leg is
// audited debug-gated so rule authors can tighten toward canonical.
func checkAtFileFlow(label *permissions.SessionLabel, command string, args []string) *GateDecision {
	ops := atFileOperands(command, args)
	for _, p := range ops {
		res := flowPathResources(p)
		eff, rule, detail, ok := permissions.LookupWithLabel(label, "read", res...)
		if !ok {
			continue
		}
		flowAuditPathLeg(label, "read", res, rule, detail)
		switch eff {
		case permissions.EffectDeny:
			reason := fmt.Sprintf("file operand %q denied by permissions policy", p) + ruleSuffix(rule)
			if detail != "" {
				reason = detail + " by permissions policy"
			}
			return &GateDecision{Action: ActionDeny, Risk: RiskUnknown, Reason: reason, PolicyRule: rule}
		case permissions.EffectAsk:
			if detail != "" {
				return &GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: detail + " by permissions policy", PolicyRule: rule}
			}
		}
	}
	return nil
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

// hasBackgroundChaining reports a single & background separator outside
// any && pair or &> / &>> / >& / 2>& redirects (H1, H2). Quote-blind like
// the other chaining checks: any raw & that sequences commands must
// escalate. A & with prev rune '>' is an FD-duplication/close redirect
// (>&2, 2>&1, 2>&-), not a separator; a & with next rune '>' is an
// &> / &>> redirect. Escape-aware: a backslash escapes the next rune, so
// an escaped & is literal and an escaped > is not a redirect (echo \>&
// separates like echo &).
func hasBackgroundChaining(s string) bool {
	rs := []rune(s)
	// escaped[i] reports whether rune i is escaped by a preceding
	// backslash (quote-blind; `\` escapes the next rune).
	escaped := make([]bool, len(rs))
	esc := false
	for i, c := range rs {
		if esc {
			escaped[i] = true
			esc = false
			continue
		}
		if c == '\\' {
			esc = true
		}
	}
	for i, c := range rs {
		if c != '&' || escaped[i] {
			continue
		}
		if i+1 < len(rs) && rs[i+1] == '&' && !escaped[i+1] {
			continue
		}
		if i > 0 && rs[i-1] == '&' && !escaped[i-1] {
			continue
		}
		if i+1 < len(rs) && rs[i+1] == '>' && !escaped[i+1] {
			continue
		}
		if i > 0 && rs[i-1] == '>' && !escaped[i-1] {
			continue
		}
		return true
	}
	return false
}

// hasUnquotedHeredoc reports a << heredoc operator outside single/double
// quotes (L3: quoted "<<EOF" text is not a heredoc). A triple <<< is a
// herestring, not a heredoc, and does not count.
func hasUnquotedHeredoc(s string) bool {
	inSingle, inDouble, escaped := false, false, false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if inSingle {
			if c == '\'' {
				inSingle = false
			}
			continue
		}
		if escaped {
			escaped = false
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '\'':
			if !inDouble {
				inSingle = true
			}
		case '"':
			inDouble = !inDouble
		case '<':
			if inDouble {
				continue
			}
			if i+1 < len(rs) && rs[i+1] == '<' {
				if i+2 < len(rs) && rs[i+2] == '<' {
					i += 2
					continue
				}
				return true
			}
		}
	}
	return false
}

// detectShellTricks returns a list of evasion techniques present in the raw
// command line. Empty when clean.
func detectShellTricks(command string) []string {
	var tricks []string

	// Chaining / pipelines: && ; || single | and single & all sequence
	// multiple commands (sh -c evaluates every segment, not just argv[0]).
	// Single & is a background separator except inside && or &> / &>>
	// redirects (H1). |& is covered by the "|" leg.
	if strings.Contains(command, "&&") || strings.Contains(command, ";") ||
		strings.Contains(command, "|") || hasBackgroundChaining(command) {
		tricks = append(tricks, "chaining")
	}
	// Subshell: only $( ... ) is a subshell; ${} is parameter expansion.
	if strings.Contains(command, "$(") {
		tricks = append(tricks, "subshell")
	}
	// Process substitution <(...) / >(...) runs its body in a subshell.
	if strings.Contains(command, "<(") || strings.Contains(command, ">(") {
		tricks = append(tricks, "process-substitution")
	}
	// Backticks
	if strings.Contains(command, "`") {
		tricks = append(tricks, "backticks")
	}
	// IFS tricks
	if strings.Contains(command, "$IFS") || strings.Contains(command, "${IFS}") {
		tricks = append(tricks, "ifs")
	}
	// Heredoc (unquoted << only; quoted text and <<< herestrings excluded).
	if hasUnquotedHeredoc(command) {
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
	// Redirect to device (covers the same /dev prefixes as hard-deny:
	// sd, vd, hd, xvd, ubd, nvme, mmcblk, loop, dm-, mapper, disk).
	if matched, _ := regexp.MatchString(`>\s*/dev/(?:sd|vd|hd|xvd|ubd|nvme|mmcblk|loop|dm-|mapper|disk)`, command); matched {
		tricks = append(tricks, "redirect")
	}

	return tricks
}

// splitShellSegments splits a shell command line on unquoted chaining and
// pipeline operators (&&, ||, ;, |, single &) and newlines (F2, H1).
// Quote-aware: text inside single/double quotes or behind a backslash
// never splits. Single & is a background separator except as && or the
// &> / &>> redirect; |& is one pipe operator. Substitution bodies
// ($(...) and backticks) are not descended into here; their payloads are
// covered by collectViews hard-deny and the subshell/backtick escalation.
// Empty segments are dropped.
//
// Lexer contract: this is the gate-side segment lexer. The sandbox
// pre-gate workdir audit uses splitShellSegmentsWithSeps
// (internal/sandbox), a deliberate subset that has no &>/>& redirect
// handling and no escaped-operator tracking because it only needs
// boundaries and cd operands, never full shell semantics (a `2>&1`
// FD-duplication splits there but not here). The divergence is pinned by
// TestSplitShellSegmentsLexerParity (gate) and
// TestSplitShellSegmentsLiteOps (sandbox). Do not "fix" one lexer
// without updating the other and its pin test.
func splitShellSegments(command string) []string {
	var segs []string
	var cur strings.Builder
	inSingle, inDouble, escaped := false, false, false
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}
	runes := []rune(command)
	prevEscaped := false
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if inSingle {
			cur.WriteRune(c)
			if c == '\'' {
				inSingle = false
			}
			prevEscaped = false
			continue
		}
		if escaped {
			cur.WriteRune(c)
			escaped = false
			prevEscaped = true
			continue
		}
		switch c {
		case '\\':
			escaped = true
			cur.WriteRune(c)
			prevEscaped = false
		case '\'':
			if !inDouble {
				inSingle = true
			}
			cur.WriteRune(c)
			prevEscaped = false
		case '"':
			inDouble = !inDouble
			cur.WriteRune(c)
			prevEscaped = false
		case '&':
			if inDouble {
				cur.WriteRune(c)
				prevEscaped = false
			} else if i+1 < len(runes) && runes[i+1] == '&' {
				flush()
				i++
				prevEscaped = false
			} else if i+1 < len(runes) && runes[i+1] == '>' {
				// &> / &>> redirect, not a separator.
				cur.WriteRune(c)
				prevEscaped = false
			} else if i > 0 && runes[i-1] == '>' && !prevEscaped {
				// >&2, 2>&1, 2>&- FD-duplication/close redirect,
				// not a separator. An escaped > (\>) is literal,
				// so \&-style \>& still separates.
				cur.WriteRune(c)
				prevEscaped = false
			} else {
				flush()
				prevEscaped = false
			}
		case '|':
			if !inDouble {
				flush()
				// || and |& are single operators.
				if i+1 < len(runes) && (runes[i+1] == '|' || runes[i+1] == '&') {
					i++
				}
				prevEscaped = false
			} else {
				cur.WriteRune(c)
				prevEscaped = false
			}
		case ';', '\n', '\r':
			if !inDouble {
				flush()
				prevEscaped = false
			} else {
				cur.WriteRune(c)
				prevEscaped = false
			}
		default:
			cur.WriteRune(c)
			prevEscaped = false
		}
	}
	flush()
	return segs
}

// shellSegmentArgvs parses every shell segment into argv (F2). Segments
// that fail to parse become single-token sentinels carrying the raw text:
// classifyRisk rates them unknown and the allowlist denies them, so
// unparseable segments fail closed instead of being skipped.
func shellSegmentArgvs(command string) [][]string {
	var out [][]string
	for _, seg := range splitShellSegments(command) {
		if argv := parseCommandArgv(seg); len(argv) > 0 {
			out = append(out, argv)
		} else {
			out = append(out, []string{seg})
		}
	}
	return out
}

// classifyShellSegments returns the maximum risk across all shell segments
// (F2). Any unknown segment keeps the maximum at RiskUnknown, which the
// gate always asks on (fail-closed). No segments -> RiskUnknown.
func classifyShellSegments(command string) CommandRisk {
	max := RiskUnknown
	found := false
	for _, seg := range shellSegmentArgvs(command) {
		r := classifyRisk(seg)
		if !found || r > max {
			max = r
			found = true
		}
	}
	return max
}

// shellTrickEscalation returns a non-empty ask reason when a shell-form
// command uses deception tricks that hide the real payload from
// argv[0]-only classification (F2, M4): command substitution, process
// substitution, backticks, IFS evasion, heredocs, and piped-shell
// downloads. Chaining alone needs no forced ask (segment-max risk already
// escalates dangerous tails while benign multi-low chains stay allowed);
// env-prefix and plain redirects are covered by the env trampoline and
// device-destruction checks.
func shellTrickEscalation(command string) string {
	tricks := detectShellTricks(command)
	if len(tricks) == 0 {
		return ""
	}
	for _, t := range tricks {
		switch t {
		case "subshell", "process-substitution", "backticks", "ifs", "heredoc", "piped-shell":
			return fmt.Sprintf("shell evasion technique %q requires user approval (%s)", t, strings.Join(tricks, ", "))
		}
	}
	return ""
}

// classifyRisk maps argv[0] (basename, lowercased) to a risk level. See the
// risk table in the contract. Unknown binaries -> RiskUnknown.
func classifyRisk(argv []string) CommandRisk {
	if len(argv) == 0 {
		return RiskUnknown
	}

	// F1: env trampoline - classify the real utility, not the trampoline.
	// Bare env (no utility) stays low-risk via the table below.
	if eff, isEnv := unwrapEnvTrampoline(argv); isEnv {
		if len(eff) == 0 {
			return RiskLow
		}
		return classifyRisk(eff)
	}

	basename := strings.ToLower(resolveBinaryBase(stripLeadBackslashes(argv[0])))

	// HIGH risk binaries (check first - destructive/privileged). del/rd are
	// Windows entries (cmd built-ins; they appear as inner tokens of
	// string-form commands, which is where the gate evaluates them).
	highRisk := map[string]bool{
		"rm": true, "rmdir": true, "dd": true, "mkfs": true,
		"fdisk": true, "parted": true, "shred": true, "format": true,
		"sudo": true, "su": true, "doas": true, "killall": true, "pkill": true,
		"truncate": true, "reboot": true, "shutdown": true, "poweroff": true,
		"halt": true, "init": true, "mount": true, "umount": true,
		"chroot": true, "mkswap": true, "swapon": true, "swapoff": true,
		"blkdiscard": true,
		"del":        true, "rd": true,
		"mkfs.ext2": true, "mkfs.ext3": true, "mkfs.ext4": true,
		"mkfs.xfs": true, "mkfs.btrfs": true, "mkfs.vfat": true,
		"mkfs.fat": true, "mkfs.ntfs": true, "mkfs.exfat": true,
	}

	// MEDIUM risk binaries. reg/regsvr32/schtasks/sc/netsh/diskpart/
	// certutil/bitsadmin are Windows entries (registry, services, firewall,
	// task scheduling, certificates, transfer jobs).
	mediumRisk := map[string]bool{
		"touch": true, "mkdir": true, "cp": true, "mv": true,
		"chmod": true, "chown": true, "ln": true,
		"tar": true, "zip": true, "unzip": true, "gzip": true,
		"xz": true, "bzip2": true,
		"pip": true, "pip3": true, "npm": true, "npx": true,
		"apt": true, "apt-get": true, "dnf": true, "yum": true,
		"pacman": true, "go": true, "cargo": true, "make": true, "gmake": true,
		"cmake": true, "curl": true, "wget": true,
		"python": true, "python3": true, "node": true,
		"ruby": true, "perl": true, "php": true,
		"tee": true, "systemctl": true, "service": true,
		"docker": true, "podman": true, "nohup": true,
		"reg": true, "regsvr32": true, "schtasks": true, "sc": true,
		"netsh": true, "diskpart": true, "certutil": true, "bitsadmin": true,
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
	// gtar/bsdtar are the same family under their GNU/BSD names, so they
	// take the same list-vs-write split instead of falling through to
	// RiskUnknown.
	if tarFamilyBases[basename] {
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

	if sub == "remote" {
		mutatingRemoteCmds := map[string]bool{
			"add": true, "remove": true, "rm": true, "set-url": true,
			"rename": true, "prune": true, "set-head": true, "set-branches": true,
			"update": true,
		}
		for _, arg := range argv[2:] {
			if mutatingRemoteCmds[arg] {
				return RiskMedium
			}
		}
		return RiskLow
	}

	if sub == "branch" {
		if hasFlag(argv, "-d") || hasFlag(argv, "-D") ||
			hasFlag(argv, "-m") || hasFlag(argv, "-M") ||
			hasFlag(argv, "-c") || hasFlag(argv, "-C") ||
			hasFlag(argv, "-f") || hasFlag(argv, "--force") ||
			hasFlag(argv, "--delete") || hasFlag(argv, "--move") ||
			hasFlag(argv, "--copy") || hasFlag(argv, "--edit-description") ||
			hasFlag(argv, "-u") || hasFlag(argv, "-t") ||
			hasFlag(argv, "--track") || hasFlag(argv, "--unset-upstream") ||
			hasFlag(argv, "--set-upstream-to") ||
			hasFlagPrefix(argv, "--set-upstream-to=") ||
			hasFlagPrefix(argv, "--track=") {
			return RiskMedium
		}
		if hasFlag(argv, "--list") || hasFlag(argv, "-l") ||
			hasFlag(argv, "--contains") || hasFlag(argv, "--no-contains") ||
			hasFlag(argv, "--merged") || hasFlag(argv, "--no-merged") {
			return RiskLow
		}
		for _, arg := range argv[2:] {
			if !strings.HasPrefix(arg, "-") {
				return RiskMedium
			}
		}
		return RiskLow
	}

	// LOW risk git subcommands (read-only).
	lowGitSubs := map[string]bool{
		"status": true, "log": true, "diff": true, "show": true,
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

// hasFlagPrefix checks whether any element in argv has the given prefix.
func hasFlagPrefix(argv []string, prefix string) bool {
	for _, a := range argv {
		if strings.HasPrefix(a, prefix) {
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

// hasRootTarget checks if argv contains a root path target
// (canonicalized: "//" Cleans to "/").
func hasRootTarget(argv []string) bool {
	for _, a := range argv {
		if a == "/" || canonicalGatePath(a) == "/" {
			return true
		}
	}
	return false
}

// chmodModeIsWorldWritable reports whether a chmod mode operand sets
// world-writable permission: "777" with any number of leading zeros
// ("0777", "00777"). Symbolic modes are out of scope for this helper.
func chmodModeIsWorldWritable(mode string) bool {
	return strings.TrimLeft(mode, "0") == "777"
}

// hasRecursiveChmod777 checks for chmod -R 777 targeting root paths.
func hasRecursiveChmod777(argv []string) bool {
	foundRecursive := false
	found777 := false
	for _, a := range argv {
		if a == "-R" || a == "-r" || a == "--recursive" {
			foundRecursive = true
		}
		if chmodModeIsWorldWritable(a) {
			found777 = true
		}
	}
	if !foundRecursive || !found777 {
		return false
	}
	for _, a := range argv {
		// Canonicalized (HIGH-1/F1): "777 /etc/" Cleans to "/etc".
		// Only exact system roots deny (subpaths stay out of scope).
		c := canonicalGatePath(a)
		if c == "/" || c == "/etc" || c == "/usr" || c == "/boot" ||
			c == "/var" || c == "/home" || c == "/root" {
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

// canonicalGatePath normalizes a deletion/chmod target before policy
// comparison (HIGH-1/F1): env expansion and Clean (collapsing /tmp/../etc
// and trailing slashes). The raw token is
// kept for the deny reason; only the comparison uses the canonical form.
func canonicalGatePath(t string) string {
	c := os.ExpandEnv(t)
	// os.ExpandEnv leaves $HOME/${HOME} behind when HOME is unset;
	// normalize those leftovers to the "~" marker already in
	// systemRootSet so the comparison still fires.
	if strings.Contains(c, "$HOME") || strings.Contains(c, "${HOME}") {
		c = normalizeShellEvasion(c)
	}
	c = filepath.Clean(c)
	return c
}

// isSystemRootTarget reports whether a raw deletion target hits a
// protected root after canonicalization (HIGH-1/F1): exact set members,
// Clean-equivalents ("/etc/", "/tmp/../etc", "~/"), and the current
// $HOME directory itself ("$HOME/").
func isSystemRootTarget(t string) bool {
	if systemRootSet[t] {
		return true
	}
	c := canonicalGatePath(t)
	if systemRootSet[c] {
		return true
	}
	if home := os.Getenv("HOME"); home != "" {
		if c == filepath.Clean(home) {
			return true
		}
	}
	// Bare "~" with a trailing separator Cleans to "~" already; a
	// "~user" form names another account, never the current home.
	return false
}

// forbiddenGlobs are forbidden target patterns for rm.
var forbiddenGlobs = []string{"/.*", "/*"}

// devicePatterns are device paths that trigger hard-deny when targeted
// by dd, mkfs, shred, fdisk, parted, or redirect.
var devicePrefixes = []string{
	"/dev/sd", "/dev/vd", "/dev/hd", "/dev/xvd", "/dev/ubd",
	"/dev/nvme", "/dev/mmcblk", "/dev/loop", "/dev/dm-",
	"/dev/mapper", "/dev/disk",
}

// deviceNameAlternation is the shared device-name pattern for every
// string/regex leg (device destruction, redirect-to-device, risky script
// scans, shell-trick redirect detection). It mirrors devicePrefixes
// above: sd, vd, hd, xvd, ubd, nvme, mmcblk, loop, dm-, mapper, disk.
// Keep the two in sync by construction.
const deviceNameAlternation = `(?:sd[a-z]\d*|vd[a-z]\d*|hd[a-z]\d*|xvd[a-z]\d*|ubd[a-z]\d*|nvme\d+n\d+[^\s]*|mmcblk\d+p?\d*|loop\d*|dm-\d+[^\s]*|mapper/[^\s]*|disk[^\s]*)`

// isDevicePath reports whether a raw target token names a raw device
// (shared prefix leg for argv checks).
func isDevicePath(t string) bool {
	for _, prefix := range devicePrefixes {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// hardDenyReason returns a non-empty reason when the command matches the
// hard-deny circuit-breaker set. Substitution-aware: also scans the raw
// command string inside $(), backticks, process substitution, and $VAR
// references.
// "" means not hard-denied.
func hardDenyReason(command string, argv []string) string {
	// Collect all views of the command: the raw command plus extracted
	// $(...), backtick, and process-substitution bodies (M4).
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
// the raw command, $(...) subcommands, backtick subcommands, and
// <(...) / >(...) process-substitution bodies (M4). Extraction is a
// fixpoint (HIGH-2/F5): bodies are re-scanned so doubly-nested payloads
// ("echo $(echo $(rm -rf /))") surface the innermost "rm -rf /" view
// instead of stopping at one level. The queue is capped at 16 views so
// adversarial nesting cannot blow up the scan.
func collectViews(command string) []string {
	views := []string{command}
	seen := map[string]bool{command: true}
	queue := []string{command}
	subshellRe := regexp.MustCompile(`\$\(([^()]*(?:\([^()]*\)[^()]*)*)\)`)
	backtickRe := regexp.MustCompile("`([^`]*)`")
	procSubRe := regexp.MustCompile(`[<>]\(([^()]*(?:\([^()]*\)[^()]*)*)\)`)
	extract := func(re *regexp.Regexp, cur string) []string {
		var out []string
		for _, match := range re.FindAllStringSubmatch(cur, -1) {
			if len(match) > 1 && match[1] != "" {
				out = append(out, match[1])
			}
		}
		return out
	}
	for len(queue) > 0 && len(views) < 16 {
		cur := queue[0]
		queue = queue[1:]
		for _, body := range extract(subshellRe, cur) {
			if !seen[body] {
				seen[body] = true
				views = append(views, body)
				queue = append(queue, body)
				if len(views) >= 16 {
					break
				}
			}
		}
		if len(views) >= 16 {
			break
		}
		for _, body := range extract(backtickRe, cur) {
			if !seen[body] {
				seen[body] = true
				views = append(views, body)
				queue = append(queue, body)
				if len(views) >= 16 {
					break
				}
			}
		}
		if len(views) >= 16 {
			break
		}
		for _, body := range extract(procSubRe, cur) {
			if !seen[body] {
				seen[body] = true
				views = append(views, body)
				queue = append(queue, body)
				if len(views) >= 16 {
					break
				}
			}
		}
	}
	return views
}

// maxEnvPositionScans is the shared work bound for every every-position env
// leg (checkEnvPositionHardDeny, envChdirAskReason, codeToolTails,
// interpreterTails). It caps the number of command-position env layers a
// scan will process, so an adversarial alternating wrapper/env chain ("env
// sudo env sudo ...") that never settles on a plain utility fails closed
// (hard-deny, or the split-string nesting Ask) in O(n) instead of paying
// per-position suffix work.
const maxEnvPositionScans = 128

// maxSettledEnvScans bounds the extra env positions rescanned after the
// unwrapped stream settles on a plain utility. A settled word may delegate
// (time) or permute its operands (make/tar getopt), so the immediately
// trailing env -C must still be inspected; a long interleaved tail is
// operands of that settled utility and is not re-expanded without bound.
const maxSettledEnvScans = 32

// envPositionSettled reports whether an env-unwrapped effective stream is
// led by a real utility rather than an assignment, wrapper prefix, or
// another env layer. Once settled, later env tokens are operands of that
// utility, not command-position trampolines, so the every-position scan can
// stop (the same command-position rule interpreterTailReachable encodes).
// An unparseable layer (unknown sentinel) is never settled: the scan keeps
// looking fail-closed, bounded by the work budget. A bare env (empty stream)
// is settled: there is no utility left to unwrap.
func envPositionSettled(eff []string) bool {
	for _, tok := range eff {
		if tok == "" || tok == "--" || envAssignRe.MatchString(tok) {
			continue
		}
		if tok == envSplitUnknown {
			return false
		}
		return !isInterpreterWrapperPrefix(gitScanBase(tok))
	}
	return true
}

// forEachEnvPosition walks seg's env(1) token positions in command position
// and invokes fn with each position's index and once-unwrapped effective
// stream. It is the single source of the every-position scan shape shared by
// the hard-deny, chdir, code-tool, and interpreter legs: coveredUntil skips
// spans an earlier unwrap already consumed, the settled/trailing rule bounds
// the tail rescanned after the stream settles on a plain utility, and the
// work budget stops adversarial never-settling chains. fn returning false
// stops the walk early.
//
// rescanSettled keeps scanning a bounded tail after settlement for legs that
// model a delegating/permuting settled utility; without it the walk stops at
// settlement. reachableOnly skips env tokens that are plain arguments of
// another utility (the interpreter/code-tool tails); skipped positions are
// still charged to the budget and advance coveredUntil, so a wrapper/env
// sandwich cannot hide work. overBudget reports a chain longer than
// maxEnvPositionScans; callers fail closed on it before doing per-position
// suffix work.
func forEachEnvPosition(seg []string, reachableOnly, rescanSettled bool, fn func(i int, eff []string) bool) (overBudget bool) {
	coveredUntil := 0
	scanned := 0
	settled := false
	trailing := 0
	for i, tok := range seg {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		if gitScanBase(tok) != "env" {
			continue
		}
		if i+1 <= coveredUntil {
			continue
		}
		if settled {
			if !rescanSettled || trailing >= maxSettledEnvScans {
				break
			}
			trailing++
		} else {
			if scanned >= maxEnvPositionScans {
				return true
			}
			scanned++
		}
		if end := i + 1 + envChainCoveredTokens(seg[i+1:]); end > coveredUntil {
			coveredUntil = end
		}
		if reachableOnly && !interpreterTailReachable(seg, i) {
			continue
		}
		eff := resolveEnvTarget(seg[i:])
		if fn != nil && !fn(i, eff) {
			return false
		}
		if len(eff) == 0 || envPositionSettled(eff) {
			settled = true
		}
	}
	return false
}

// unwrapEnvChainSettled walks the command chain once, splicing every
// -S/--split-string payload and unwrapping every env(1) layer in command
// position until the effective stream settles on a plain utility (or runs out
// of env). It returns the fully unwrapped stream so a caller can run one
// token scan instead of re-expanding each env suffix (which made an
// alternating wrapper/env chain cubic), plus the number of env layers
// consumed. overBudget reports a chain longer than maxEnvPositionScans: the
// caller fails closed in O(n) instead of scanning per position.
func unwrapEnvChainSettled(seg []string) (eff []string, layers int, overBudget bool) {
	eff = append([]string(nil), seg...)
	start := 0
	for {
		idx := -1
		for i := start; i < len(eff); i++ {
			tok := eff[i]
			if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
				continue
			}
			if gitScanBase(tok) == "env" {
				idx = i
				break
			}
		}
		if idx < 0 {
			return eff, layers, false
		}
		next, isEnv := unwrapEnvTrampoline(eff[idx:])
		if !isEnv {
			// A substitution/escape opener that gitScanBase resolves to
			// env but env(1) itself would not parse: leave it in place
			// and keep scanning after it.
			start = idx + 1
			continue
		}
		layers++
		if layers > maxEnvPositionScans {
			return eff, layers, true
		}
		// resolveEnvTarget unwraps the whole consecutive env-head chain
		// (`env env ... ls`), which the every-position scan charged as a
		// single position.
		next = resolveEnvTarget(eff[idx:])
		eff = append(append([]string(nil), eff[:idx]...), next...)
		if len(next) == 0 || envPositionSettled(next) {
			return eff, layers, false
		}
		// The resolved prefix before idx contains no env token; rescan
		// the new stream from idx (the utility follows any wrapper
		// prefix).
		start = idx
	}
}

// checkEnvPositionHardDeny unwraps env(1) at env token positions in command
// position (H1/F1): wrapper prefixes (sudo, nice, ...) place env after
// argv[0], so an argv[0]-only resolveEnvTarget misses sudo env -S "rm -rf /"
// and sudo env -S "bash -c ...". The command chain is walked once
// (unwrapEnvChainSettled) and the full checkArgvHardDeny runs on the fully
// unwrapped stream, instead of re-running it on every env suffix (which was
// cubic on an alternating wrapper/env chain). The -S splice keeps assignments
// visible to the git/tar option audit, which drops env overlays when
// unwrapping. Quoted payloads stay single tokens, so echo "rm -rf /" stays
// green. A work budget fails closed on adversarial chains that never settle.
func checkEnvPositionHardDeny(argv []string) string {
	return checkEnvPositionHardDenyScan(argv, true)
}

// checkEnvPositionHardDenyScan is checkEnvPositionHardDeny with the sudo
// payload recursion switchable: the sudo payload leg follows nested carriers
// itself (with its own depth bound), so it calls this with sudo=false to
// avoid re-entering the quadratic suffix scan.
func checkEnvPositionHardDenyScan(argv []string, allowSudo bool) string {
	eff, layers, over := unwrapEnvChainSettled(argv)
	if over {
		return "command nests env trampolines too deeply to audit"
	}
	if layers == 0 {
		return ""
	}
	if len(eff) > 0 {
		if reason := checkArgvHardDenyScan(eff, allowSudo); reason != "" {
			return reason
		}
	}
	// The unwrap drops the VAR=val overlays env consumes, so re-scan the
	// spliced whole-argv view: an assignment hidden behind -S
	// (`env -S 'GIT_PAGER=id git status'`) still denies.
	if expanded, bad := expandEnvSplitString(argv); !bad {
		if reason := checkGitTokenScan(expanded); reason != "" {
			return reason
		}
		if reason := toolOptionEnvHardDeny(expanded); reason != "" {
			return reason
		}
	}
	return ""
}

// checkHardDeny runs hard-deny checks against argv and a set of
// normalized command-string views.
func checkHardDeny(argv []string, views []string) string {
	return checkHardDenyScan(argv, views, true)
}

// checkHardDenyScan is checkHardDeny with the sudo payload recursion
// switchable. allowSudo=false still runs every other hard-deny leg; the
// sudo payload leg (checkSudoShellPayload) follows nested carriers itself,
// iteratively and depth-bounded, so payload checks use this to avoid
// re-parsing every suffix of a flat sudo -s chain (quadratic).
func checkHardDenyScan(argv []string, views []string, allowSudo bool) string {
	// Check parsed argv if available.
	if len(argv) > 0 {
		if reason := checkArgvHardDenyScan(argv, allowSudo); reason != "" {
			return reason
		}
		// F1: env trampoline - the real utility hides behind env(1), so
		// run the same argv checks on the fully unwrapped target
		// (env rm -rf / must deny exactly like rm -rf /). Bare env
		// (no utility) has nothing further to check.
		if eff := resolveEnvTarget(argv); len(eff) > 0 {
			if reason := checkArgvHardDenyScan(eff, allowSudo); reason != "" {
				return reason
			}
		}
		// H1/F1: env behind a wrapper prefix (sudo env -S ...) hides at
		// argv[i], not argv[0]: unwrap every token position fail-closed.
		if reason := checkEnvPositionHardDenyScan(argv, allowSudo); reason != "" {
			return reason
		}
	}

	// Quote-aware rm (M3): per-segment parsed argv tails catch real
	// "ls && rm -rf ~" tails without matching quoted text like
	// 'echo "rm -rf ~"'. H1: scan every token position for an rm
	// utility (sudo/nice/nohup/timeout/xargs/find -exec/FOO=bar all
	// place rm after a wrapper prefix), not just segArgv[0].
	// Quoted payloads stay single tokens ("rm -rf /" != rm) so echo
	// "rm -rf /" stays green. Other string checks stay raw; the
	// normalized leg below covers $IFS/$HOME evasion only.
	for _, view := range views {
		for _, segArgv := range shellSegmentArgvs(view) {
			if len(segArgv) == 0 {
				continue
			}
			// Normalize a grouped segment once before the token matchers so
			// "(sudo", "{chmod" and a trailing ")" resolve like the bare
			// form for every scanner leg below.
			segArgv = normalizeHardDenySegment(segArgv)
			if reason := checkRmTokenScan(segArgv); reason != "" {
				return reason
			}
			// H1/F1: env at any token position (sudo env -S ...) with the
			// full circuit breaker (rm, shell -c, dd, chmod/chown): an
			// argv[0]-only checkRmTokenScan misses --split-string="bash
			// -c ..." tails.
			if reason := checkEnvPositionHardDenyScan(segArgv, allowSudo); reason != "" {
				return reason
			}
			if reason := checkShellCPayload(segArgv); reason != "" {
				return reason
			}
			if allowSudo {
				if reason := checkSudoShellPayload(segArgv); reason != "" {
					return reason
				}
			}
			// F4: execution-capable flags hide behind wrappers and
			// pipes ("sudo tar ...", "printf x | tar ..."), so scan
			// every token position per segment, not just segArgv[0].
			if reason := checkExecFlagTokenScan(segArgv); reason != "" {
				return reason
			}
			// TAR_OPTIONS hides the same exec flags in an option
			// stream: scan per segment like the argv leg so a piped
			// tail ("printf x | env TAR_OPTIONS=... tar") denies.
			if reason := toolOptionEnvHardDeny(segArgv); reason != "" {
				return reason
			}
			// Git config/env influence and chmod/chown hide the same
			// way ("sudo git -c core.pager=id status",
			// "sudo chmod -R 777 /").
			if reason := checkGitTokenScan(segArgv); reason != "" {
				return reason
			}
			if reason := checkChmodTokenScan(segArgv); reason != "" {
				return reason
			}
			// make -f/--file/--eval and MAKEFILES hide makefiles or
			// evaluated code the gate cannot inspect. The per-view scan
			// reaches carriers the argv leg cannot: a sudo -s/su -c
			// payload is re-lexed here as a view, and a normalized
			// $IFS spelling resolves to a real make token.
			if reason := checkMakeTokenScan(segArgv); reason != "" {
				return reason
			}
		}
		if reason := checkStringHardDenyNoRm(view); reason != "" {
			return reason
		}
		// F4 string leg: packed payloads and substitution bodies
		// tokenize with affixed closers, so the argv leg alone misses
		// them ("tar ... --checkpoint-action='exec=id')" stays one
		// token). Runs on both raw and normalized views.
		if reason := checkExecFlagInString(view); reason != "" {
			return reason
		}
		if normalized := normalizeShellEvasion(view); normalized != view {
			if reason := checkStringHardDeny(normalized); reason != "" {
				return reason
			}
			if reason := checkExecFlagInString(normalized); reason != "" {
				return reason
			}
		}
		// G1 ACE-path: bash ANSI-C $'...' / locale $"..." decode.
		// shlex keeps "$--co=gzip" as one token (misses the --co
		// prefix) and stripQuotedRegions blanks '...' as quoted, so
		// both the argv and string legs miss the executed flag.
		// Decode first, then re-run the token + string legs on the
		// executed form. Plain quoting is untouched so quoted-echo
		// greens stay green. Unbalanced openers fail closed via the
		// raw legs (flag text stays visible) and shlex Ask downstream.
		if decoded, _ := decodeANSICQuotes(view); decoded != view {
			for _, segArgv := range shellSegmentArgvs(decoded) {
				if len(segArgv) == 0 {
					continue
				}
				segArgv = normalizeHardDenySegment(segArgv)
				if reason := checkArgvHardDeny(segArgv); reason != "" {
					return reason
				}
				// H1/F1: env at any token position (sudo env -S ...)
				// with the full circuit breaker (rm, shell -c, dd,
				// chmod/chown): a checkArgvHardDeny-only decoded leg
				// misses --split-string="bash -c ..." tails, so
				// ANSI-C $'...' forms would Allow under allow where
				// the raw legs Deny. Mirrors the raw leg above.
				if reason := checkEnvPositionHardDenyScan(segArgv, allowSudo); reason != "" {
					return reason
				}
			}
			if reason := checkStringHardDenyNoRm(decoded); reason != "" {
				return reason
			}
			if reason := checkExecFlagInString(decoded); reason != "" {
				return reason
			}
			if normalized := normalizeShellEvasion(decoded); normalized != decoded {
				if reason := checkStringHardDeny(normalized); reason != "" {
					return reason
				}
				if reason := checkExecFlagInString(normalized); reason != "" {
					return reason
				}
			}
		}
	}

	return ""
}

// stripSubstPrefix removes command/process-substitution openers from a
// token (HIGH-2/F5): shlex keeps "$(rm" and "<(rm" as one token, hiding
// rm from the token scan. Quoted payloads stay single multi-word tokens
// ("rm -rf /" != rm) so echo green is preserved.
func stripSubstPrefix(tok string) string {
	for {
		switch {
		case strings.HasPrefix(tok, "$("):
			tok = tok[2:]
		case strings.HasPrefix(tok, "<(") || strings.HasPrefix(tok, ">("):
			tok = tok[2:]
		case strings.HasPrefix(tok, "`") || strings.HasPrefix(tok, "("):
			tok = tok[1:]
		default:
			return tok
		}
	}
}

// stripLeadBackslashes removes shell-escape backslashes from a token for
// comparison only: `\git` executes git (the backslash quotes the g), and
// an interior escape like `g\it` executes git exactly the same way, so
// the git/env trampoline scans must see through every backslash (M-2
// parity with the sandbox gitLedBase, which strips all). Tokenization
// is unchanged: quoted payloads stay single multi-word tokens
// ("git ..." != git) so echo green is preserved.
func stripLeadBackslashes(tok string) string {
	return strings.ReplaceAll(tok, "\\", "")
}

// gitScanBase resolves the comparison basename for git/env trampoline
// detection: substitution openers, then escape backslashes, stripped
// before the basename, lowercased.
func gitScanBase(tok string) string {
	return strings.ToLower(resolveBinaryBase(stripLeadBackslashes(stripSubstPrefix(tok))))
}

// checkRmTokenScan checks every token position for an rm utility (H1):
// wrapper prefixes (sudo, nice, nohup, timeout, xargs, find -exec,
// FOO=bar assignments) place rm after argv[0], so argv[0]-only checks
// downgrade Deny->Ask. Any token whose resolved base is rm runs
// checkRmHardDeny on its tail. Quoted payloads stay single tokens
// ("rm -rf /" != rm) so echo "rm -rf /" stays green.
func checkRmTokenScan(argv []string) string {
	for i, tok := range argv {
		if tok == "" {
			continue
		}
		if envAssignRe.MatchString(tok) {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			continue
		}
		if strings.ToLower(resolveBinaryBase(stripSubstPrefix(tok))) != "rm" {
			continue
		}
		if reason := checkRmHardDeny(argv[i:]); reason != "" {
			return reason
		}
	}
	return ""
}

// shellPayloadBases are shells whose -c payload executes as shell code.
var shellPayloadBases = map[string]bool{
	"bash": true, "sh": true, "dash": true, "zsh": true, "fish": true,
	"csh": true, "ksh": true, "rbash": true, "su": true,
}

// shellCommandPayloads returns the -c code candidates of a non-su shell
// invocation tail (argv after the shell word). It is the single source of
// the -c selection shared by the hard-deny payload walk
// (checkShellCPayload) and the Ask payload walk: standalone -c skips "--"
// separators, a short cluster containing c contributes the attached suffix
// plus the following token (and, for an o/O value-taker in the same
// cluster, the token after that), and value-taking -o/-O/--rcfile/
// --init-file options skip their argument. Like the shell itself the walk
// stops at "--" or the first plain operand. Ambiguous shapes are reported
// fail-closed rather than skipped.
func shellCommandPayloads(rest []string) []string {
	var out []string
	for j := 0; j < len(rest); j++ {
		a := rest[j]
		if a == "" {
			continue
		}
		if a == "--" {
			break
		}
		if a == "-c" {
			// Real shells skip "--" operands after -c and run the next
			// token, so the payload is the first non-"--" token.
			k := j + 1
			for k < len(rest) && rest[k] == "--" {
				k++
			}
			if k >= len(rest) || rest[k] == "" {
				break
			}
			out = append(out, rest[k])
			break
		}
		if a == "-o" || a == "-O" || a == "+o" || a == "+O" ||
			a == "--rcfile" || a == "--init-file" {
			if j+1 < len(rest) {
				j++
				continue
			}
			break
		}
		if strings.HasPrefix(a, "--rcfile=") || strings.HasPrefix(a, "--init-file=") {
			continue
		}
		if len(a) > 1 && (a[0] == '-' || a[0] == '+') && a[1] != '-' {
			cluster := a[1:]
			if cIdx := strings.IndexByte(cluster, 'c'); cIdx >= 0 {
				// Attached payload ("-c'cmd'" parses to one
				// "-ccmd" token, e.g. "-crm -rf /",
				// "-ecrm -rf /"): the suffix after c IS the
				// code. Earlier value-taking letters (o/O) do
				// not hide it fail-closed: report the suffix
				// AND the next token(s).
				if suffix := cluster[cIdx+1:]; suffix != "" {
					out = append(out, suffix)
				}
				k := j + 1
				for k < len(rest) && rest[k] == "--" {
					k++
				}
				if k >= len(rest) || rest[k] == "" {
					break
				}
				out = append(out, rest[k])
				// A value-taking o/O in the same cluster (e.g. -co,
				// -oc) consumes rest[k] as its optname, shifting
				// the real -c payload to the next token: report it
				// too fail-closed.
				if strings.ContainsAny(cluster, "oO") {
					k2 := k + 1
					for k2 < len(rest) && rest[k2] == "--" {
						k2++
					}
					if k2 < len(rest) && rest[k2] != "" {
						out = append(out, rest[k2])
					}
				}
				break
			}
			if last := cluster[len(cluster)-1]; last == 'o' || last == 'O' {
				if j+1 < len(rest) {
					j++
					continue
				}
				break
			}
			continue
		}
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+") {
			continue
		}
		break
	}
	return out
}

// checkShellCPayload descends into shell -c payloads (H1: bash -c
// 'rm -rf /' hides rm as a single quoted token, invisible to the token
// scan). Any shell token in command position followed by -c (or -c<code>,
// or a short cluster containing c like -ec/-xc) has its code argument
// re-checked with the full hard-deny circuit breaker. Attached payloads
// ("bash -c'rm -rf /'" parses to one "-crm -rf /" token, likewise
// "-ec'...'" and su "-c'...'") check the suffix after c as the payload.
// Value-taking shell options (-o/-O/+o/+O with a separate optname,
// --rcfile/--init-file with a separate path or =path) are skipped with
// their argument; unknown -/+/-- flags are scanned through (fail-closed:
// keep looking for -c). "--" separators between -c and its payload are
// skipped (real shells run the next token, so "bash -c -- 'rm -rf /'"
// still denies). Only command-position shells are carriers
// (interpreterTailReachable, so `echo su -c 'rm -rf /'` stays green:
// su there is an argument, not a command). Wrapper prefixes are handled
// by scanning every command-position shell, so "sudo bash -c 'rm -rf /'"
// is caught too. An attached subshell/group opener ("(bash", "{bash")
// classifies by its stripped form, so a grouped shell is a carrier exactly
// like its bare form. Ambiguous shapes (cluster with c, missing option value)
// check the candidate payload rather than silently skipping.
func checkShellCPayload(argv []string) string {
	for i, tok := range argv {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		if !shellPayloadBases[strings.ToLower(resolveBinaryBase(shellPrefixClassToken(tok)))] {
			continue
		}
		if i > 0 && !interpreterTailReachable(argv, i) {
			continue
		}
		// su(1) takes an optional username operand before -c
		// ("su root -c 'cmd'", "su - root -c 'cmd'"): plain operands
		// are the user, not the end of options, so scan the whole tail
		// for -c instead of stopping at the first non-flag token.
		if strings.ToLower(resolveBinaryBase(shellPrefixClassToken(tok))) == "su" {
			if reason := checkSuCPayload(argv[i+1:]); reason != "" {
				return reason
			}
			continue
		}
		for _, payload := range shellCommandPayloads(argv[i+1:]) {
			if reason := checkHardDeny(nil, []string{payload}); reason != "" {
				return reason
			}
		}
	}
	return ""
}

// checkSuCPayload descends into su(1) -c payloads: unlike a plain shell,
// su takes an optional username operand ("su root -c 'cmd'",
// "su - root -c 'cmd'"), so the scan must not stop at the first
// non-flag token. Value-taking options are driven by the shared
// wrapperValueFlagsByWrapper["su"] table (-s/--shell, -g/--group,
// -G/--supp-group, -w/--whitelist-environment): separate token or
// attached remainder like "-s/bin/csh" (the remainder is the value,
// never a -c carrier). Long options resolve by unique prefix
// (getopt_long abbreviations: --comm -> --command, --sess ->
// --session-command) via resolveSuLong. Every -c/--command/
// --session-command payload in the tail is re-checked with the full
// hard-deny circuit breaker (real su permits repeats and runs the
// last -c, so stopping at the first would let "su -c 'echo hi'
// -c 'rm -rf /'" through). Attached suffixes after c are checked too.
// Unknown -/+/-- flags are scanned through fail-closed.
func checkSuCPayload(rest []string) string {
	for j := 0; j < len(rest); j++ {
		a := rest[j]
		if a == "" {
			continue
		}
		if a == "-c" {
			k := j + 1
			for k < len(rest) && rest[k] == "--" {
				k++
			}
			if k >= len(rest) {
				break
			}
			if rest[k] == "" {
				// Empty payload ("su -c '' -c 'rm -rf /'"):
				// keep scanning the tail for a later carrier
				// instead of aborting the loop.
				continue
			}
			if strings.HasPrefix(rest[k], "-") || strings.HasPrefix(rest[k], "+") {
				// Option-shaped candidate after "--" starts a new
				// option stream (repeat -c/--command carrier: su
				// runs the last -c) - re-parse it instead of
				// consuming it as this payload.
				continue
			}
			if reason := checkHardDeny(nil, []string{rest[k]}); reason != "" {
				return reason
			}
			// Keep scanning: su runs the last -c, so a later
			// payload must also be checked.
			j = k
			continue
		}
		if strings.HasPrefix(a, "--") {
			name := strings.TrimPrefix(a, "--")
			val := ""
			hasVal := false
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				val = name[idx+1:]
				name = name[:idx]
				hasVal = true
			}
			switch resolveSuLong(name) {
			case suLongCarrier:
				if hasVal {
					if val != "" {
						if reason := checkHardDeny(nil, []string{val}); reason != "" {
							return reason
						}
					}
					continue
				}
				k := j + 1
				for k < len(rest) && rest[k] == "--" {
					k++
				}
				if k >= len(rest) {
					break
				}
				if rest[k] == "" {
					// Empty payload ("su --command '' -c 'rm -rf /'"):
					// keep scanning the tail for a later carrier
					// instead of aborting the loop.
					continue
				}
				if strings.HasPrefix(rest[k], "-") || strings.HasPrefix(rest[k], "+") {
					// Option-shaped candidate after "--" starts
					// a new option stream (repeat carrier: su
					// runs the last -c) - re-parse it instead
					// of consuming it as this payload.
					continue
				}
				if reason := checkHardDeny(nil, []string{rest[k]}); reason != "" {
					return reason
				}
				j = k
				continue
			case suLongTakesValue:
				if hasVal {
					continue
				}
				if j+1 < len(rest) {
					j++
					continue
				}
				break
			default:
				continue
			}
		}
		if wrapperTakesValue("su", a) {
			if j+1 < len(rest) {
				j++
				continue
			}
			break
		}
		if len(a) > 1 && (a[0] == '-' || a[0] == '+') && a[1] != '-' {
			cluster := a[1:]
			cIdx := strings.IndexByte(cluster, 'c')
			// A value-taking member before c claims the remainder
			// ("-s/bin/csh": "/bin/csh" is the shell, the 'c' in
			// "csh" is not a carrier; "-wFOO" likewise). Skip the
			// token and keep scanning so a later real -c is found.
			// Driven by the shared su table (s/g/G/w).
			valIdx := -1
			for k := 0; k < len(cluster); k++ {
				if wrapperShortTakesValue("su", cluster[k]) {
					valIdx = k
					break
				}
			}
			if valIdx >= 0 && (cIdx < 0 || valIdx < cIdx) {
				if valIdx+1 >= len(cluster) {
					// Trailing value-taker ("-s", "-w", ...)
					// as a cluster tail consumes the next token.
					if j+1 < len(rest) {
						j++
						continue
					}
					break
				}
				// Attached value ("-s/bin/csh", "-groot"):
				// remainder is the value, no carrier here.
				continue
			}
			if cIdx >= 0 {
				// Attached payload ("-c'cmd'" parses to one
				// "-ccmd" token): the suffix after c IS the
				// code. When a suffix is present the next
				// token starts a new option stream (a repeat
				// -c, --command/--comm/--sess, a username, or
				// $0) - consuming it here would skip a later
				// carrier ("su -c'echo hi' -c'rm -rf /'").
				// Only consume it fail-closed when it is NOT
				// option-shaped; otherwise leave it for the
				// next iteration to re-parse.
				if suffix := cluster[cIdx+1:]; suffix != "" {
					if reason := checkHardDeny(nil, []string{suffix}); reason != "" {
						return reason
					}
					k := j + 1
					for k < len(rest) && rest[k] == "--" {
						k++
					}
					if k >= len(rest) || rest[k] == "" {
						// No separate token: the suffix was
						// already checked above. Keep
						// scanning for a later -c.
						continue
					}
					if strings.HasPrefix(rest[k], "-") || strings.HasPrefix(rest[k], "+") {
						continue
					}
					if reason := checkHardDeny(nil, []string{rest[k]}); reason != "" {
						return reason
					}
					j = k
					continue
				}
				// Bare trailing c ("-mc"): the next token is
				// the payload.
				k := j + 1
				for k < len(rest) && rest[k] == "--" {
					k++
				}
				if k >= len(rest) || rest[k] == "" {
					continue
				}
				if strings.HasPrefix(rest[k], "-") || strings.HasPrefix(rest[k], "+") {
					// Option-shaped candidate after "--"
					// starts a new option stream (repeat
					// carrier: su runs the last -c) - re-parse
					// it instead of consuming it as this
					// payload.
					continue
				}
				if reason := checkHardDeny(nil, []string{rest[k]}); reason != "" {
					return reason
				}
				j = k
				continue
			}
			if wrapperClusterConsumesNext("su", cluster) {
				if j+1 < len(rest) {
					j++
					continue
				}
				break
			}
			continue
		}
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+") {
			continue
		}
		// Plain operand: the su username (or "-" login marker) - skip
		// it and keep looking for -c.
		continue
	}
	return ""
}

// sudoShellPayloads extracts shell-mode carrier payloads for one sudo
// invocation tail (argv after "sudo"): when -s/--shell or -i/--login is
// present (standalone or inside a short cluster), the remaining operands
// run as a shell command. Value-taking sudo options (shared
// wrapperValueFlagsByWrapper["sudo"] set) consume the rest of their
// cluster token or the next token. Returns each payload as a joined
// command line (at most one per sudo, empty when bare "sudo -s").
func sudoShellPayloads(tail []string) []string {
	sawShell := false
	var rest []string
	skipNext := false
	for _, a := range tail {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "" {
			continue
		}
		if a == "--" {
			continue
		}
		if a == "-s" || a == "--shell" || a == "-i" || a == "--login" {
			sawShell = true
			continue
		}
		if wrapperTakesValue("sudo", a) {
			skipNext = true
			continue
		}
		if strings.HasPrefix(a, "--") {
			// --opt=val is one token (skipped as a flag);
			// bare --long flags are boolean.
			continue
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' {
			// Short cluster: shared value-taking members
			// (u/g/p/C/D/R/r/T/t/U) consume the rest of the
			// token or the next token; s/i members mark shell
			// mode.
			consumesNext := false
			for k := 1; k < len(a); k++ {
				switch a[k] {
				case 's', 'i':
					sawShell = true
				default:
					if wrapperShortTakesValue("sudo", a[k]) {
						if k+1 < len(a) {
							k = len(a)
						} else {
							consumesNext = true
						}
					}
				}
			}
			if consumesNext {
				skipNext = true
			}
			continue
		}
		rest = append(rest, a)
	}
	if sawShell && len(rest) > 0 {
		return []string{strings.Join(rest, " ")}
	}
	return nil
}

// suCommandPayloads extracts su(1) -c/--command/--session-command
// payload strings from the tail after "su" (mirrors checkSuCPayload
// parsing, but returns the payload texts for interpreter escalation).
// Both attached ("-c'cmd'") and separate ("-c 'cmd'") forms are
// returned; "--" separators are skipped. Every carrier payload in the
// tail is collected (su runs the last -c); long options resolve by
// unique prefix (--comm, --sess); value-taking options come from the
// shared wrapperValueFlagsByWrapper["su"] table.
func suCommandPayloads(rest []string) []string {
	var out []string
	collect := func(payload string) {
		if payload != "" {
			out = append(out, payload)
		}
	}
	for j := 0; j < len(rest); j++ {
		a := rest[j]
		if a == "" {
			continue
		}
		if a == "-c" {
			k := j + 1
			for k < len(rest) && rest[k] == "--" {
				k++
			}
			if k < len(rest) && rest[k] != "" {
				if strings.HasPrefix(rest[k], "-") || strings.HasPrefix(rest[k], "+") {
					// Option-shaped candidate after "--"
					// starts a new option stream (repeat
					// carrier) - re-parse it instead of
					// collecting it as this payload.
					continue
				}
				collect(rest[k])
				j = k
			}
			continue
		}
		if strings.HasPrefix(a, "--") {
			name := strings.TrimPrefix(a, "--")
			val := ""
			hasVal := false
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				val = name[idx+1:]
				name = name[:idx]
				hasVal = true
			}
			switch resolveSuLong(name) {
			case suLongCarrier:
				if hasVal {
					collect(val)
					continue
				}
				k := j + 1
				for k < len(rest) && rest[k] == "--" {
					k++
				}
				if k < len(rest) && rest[k] != "" {
					if strings.HasPrefix(rest[k], "-") || strings.HasPrefix(rest[k], "+") {
						// Option-shaped candidate after "--"
						// starts a new option stream (repeat
						// carrier) - re-parse it instead of
						// collecting it as this payload.
						continue
					}
					collect(rest[k])
					j = k
				}
				continue
			case suLongTakesValue:
				if hasVal {
					continue
				}
				if j+1 < len(rest) {
					j++
					continue
				}
				break
			default:
				continue
			}
		}
		if wrapperTakesValue("su", a) {
			if j+1 < len(rest) {
				j++
				continue
			}
			break
		}
		if len(a) > 1 && (a[0] == '-' || a[0] == '+') && a[1] != '-' {
			cluster := a[1:]
			cIdx := strings.IndexByte(cluster, 'c')
			valIdx := -1
			for k := 0; k < len(cluster); k++ {
				if wrapperShortTakesValue("su", cluster[k]) {
					valIdx = k
					break
				}
			}
			if valIdx >= 0 && (cIdx < 0 || valIdx < cIdx) {
				if valIdx+1 >= len(cluster) {
					if j+1 < len(rest) {
						j++
						continue
					}
					break
				}
				continue
			}
			if cIdx >= 0 {
				if suffix := cluster[cIdx+1:]; suffix != "" {
					collect(suffix)
					k := j + 1
					for k < len(rest) && rest[k] == "--" {
						k++
					}
					if k >= len(rest) || rest[k] == "" {
						continue
					}
					// Attached suffix present: an
					// option-shaped next token starts a new
					// option stream - re-parse it instead of
					// collecting it as this payload.
					if strings.HasPrefix(rest[k], "-") || strings.HasPrefix(rest[k], "+") {
						continue
					}
					collect(rest[k])
					j = k
					continue
				}
				k := j + 1
				for k < len(rest) && rest[k] == "--" {
					k++
				}
				if k < len(rest) && rest[k] != "" {
					// Bare trailing c ("-mc"): an
					// option-shaped candidate after "--"
					// starts a new option stream (repeat
					// carrier) - re-parse it instead of
					// collecting it as this payload.
					if strings.HasPrefix(rest[k], "-") || strings.HasPrefix(rest[k], "+") {
						continue
					}
					collect(rest[k])
					j = k
				}
				continue
			}
			if wrapperClusterConsumesNext("su", cluster) {
				if j+1 < len(rest) {
					j++
					continue
				}
				break
			}
			continue
		}
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+") {
			continue
		}
		continue
	}
	return out
}

// shellCarrierPayloadsAt returns the joined payloads a reachable
// shell-mode carrier token at seg[i] runs: sudo -s/-i operands, su -c/
// --command commands, eval(1) operand words, and a shell -c payload. Returns
// nil when the token is not such a carrier. An attached subshell/group opener
// ("(sudo", "{bash") classifies by its stripped form, so a grouped carrier
// yields the same payload as the bare one. Callers own the command-position
// guard (interpreterTailReachable), so a prose mention ("echo sudo -s
// 'tar $1'") never contributes a payload.
func shellCarrierPayloadsAt(seg []string, i int) []string {
	if i < 0 || i >= len(seg) {
		return nil
	}
	base := strings.ToLower(resolveBinaryBase(shellPrefixClassToken(seg[i])))
	switch {
	case base == "sudo":
		return sudoShellPayloads(seg[i+1:])
	case base == "su":
		return suCommandPayloads(seg[i+1:])
	case base == "eval":
		// eval(1) joins its argument words into one command line and
		// runs it, exactly like a -c payload with no flag prefix.
		if payload := strings.Join(seg[i+1:], " "); strings.TrimSpace(payload) != "" {
			return []string{payload}
		}
		return nil
	case shellPayloadBases[base]:
		return shellCommandPayloads(seg[i+1:])
	}
	return nil
}

// shellAskLegs runs the Ask half of the escalation circuit over one shell
// segment. These are the same fail-closed leaves EvaluateCommand applies to
// top-level segments (code tools, git config/env, env/make/tar chdir,
// option-stream env, chmod subpaths, parameter-expansion tails, and an
// effective command the gate cannot resolve); the hard-deny half runs
// separately (and first), so a known exec-capable payload still Denies.
// commandPosition is resolved inside the segment, so prose ("echo tar ${X}")
// stays green. gitRisk is the risk the caller reports for the git leg (the
// segment risk at top level, RiskUnknown inside a carrier payload); the
// chmod subpath leg always reports RiskHigh and is never downgraded by the
// fold. Returns nil when no leg fires.
func shellAskLegs(seg []string, gitRisk CommandRisk) *GateDecision {
	for _, tail := range codeToolTails(seg) {
		if reason := codeToolAskReason(tail); reason != "" {
			return &GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: reason}
		}
	}
	if reason := gitTokenAskReason(seg); reason != "" {
		return &GateDecision{Action: ActionAsk, Risk: gitRisk, Reason: reason}
	}
	if reason := envChdirAskReason(seg); reason != "" {
		return &GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: reason}
	}
	if reason := utilityChdirAskReason(seg); reason != "" {
		return &GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: reason}
	}
	if reason := toolOptionEnvAskReason(seg); reason != "" {
		return &GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: reason}
	}
	if reason := chmodSubpathAskReason(seg); reason != "" {
		return &GateDecision{Action: ActionAsk, Risk: RiskHigh, Reason: reason}
	}
	if reason := paramExpansionAskReason(seg); reason != "" {
		return &GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: reason}
	}
	if reason := unresolvedCommandAskReason(seg); reason != "" {
		return &GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: reason}
	}
	return nil
}

// shellPrefixWalk is the shared prefix-classification state machine behind
// interpreterTailReachable and effectiveCommandArgv. One walk decides
// whether a token in front of a utility is transparent prefix material: a
// VAR= assignment, a separator, a shell command-opening word or group
// opener, a flag, a delegating wrapper, or that wrapper's value/positional
// operand. Keeping both callers on this iterator is what stops the hard-deny
// reachability rule and the Ask-side effective-command rule from drifting.
type shellPrefixWalk struct {
	skipNext              bool
	expectTimeoutDuration bool
	expectChrootRoot      bool
	expectGosuUser        bool
	expectFlockFile       bool
	curWrapper            string
	findExecSeen          bool
	// expansionsTransparent makes an unresolvable $VAR/${...} token
	// transparent. The interpreter-reachability walk uses true (the
	// expansion may name a wrapper or the interpreter itself, so the word
	// after it stays in command position - fail-closed); the effective-
	// command walk uses false (an expansion head cannot be resolved and
	// must Ask).
	expansionsTransparent bool
}

// transparent reports whether tok is transparent prefix material. It returns
// false at the first token that is the effective command.
func (w *shellPrefixWalk) transparent(tok string) bool {
	if w.skipNext {
		w.skipNext = false
		return true
	}
	if tok == "" || tok == "--" || envAssignRe.MatchString(tok) {
		return true
	}
	if tok == ";" || tok == "+" {
		// find(1) -exec ... \; / + terminator: closes the exec
		// command, expression operands follow.
		if w.curWrapper == "find" {
			w.findExecSeen = false
		}
		return true
	}
	// Shell command-opening words and group openers keep the following word
	// in command position without being the command themselves. Classify an
	// attached opener ("(sudo", "{command") by its stripped form so a
	// grouped command resolves like its bare form; an opener at the front of
	// the prefix is also removed wholesale by StripCdGroupOpeners, but a
	// lone opener (all-openers prefix) still reaches this check.
	if shellCommandPrefixWords[strings.ToLower(tok)] ||
		shellCommandPrefixWords[strings.ToLower(shellPrefixClassToken(tok))] {
		return true
	}
	if w.expansionsTransparent && hasParamExpansion(tok) {
		return true
	}
	if strings.HasPrefix(tok, "-") {
		// find(1) -exec/-execdir opens command position: the following
		// tokens run as a command, so a shell there is a carrier
		// ("find . -exec bash -c 'rm -rf /'").
		if (tok == "-exec" || tok == "-execdir") && w.curWrapper == "find" {
			w.findExecSeen = true
		}
		if wrapperTakesValue(w.curWrapper, tok) {
			w.skipNext = true
		} else if len(tok) > 1 && tok[0] == '-' && tok[1] != '-' && w.curWrapper != "" {
			// Short cluster under a known wrapper (e.g. sudo "-sD",
			// timeout "-sk"): a trailing value-taking member consumes
			// the next token, an attached remainder consumes nothing.
			if wrapperClusterConsumesNext(w.curWrapper, tok[1:]) {
				w.skipNext = true
			}
		}
		return true
	}
	base := strings.ToLower(resolveBinaryBase(shellPrefixClassToken(tok)))
	if isInterpreterWrapperPrefix(base) {
		// timeout(1) takes a DURATION operand, chroot(1) a NEWROOT,
		// gosu(1) a USER, flock(1) a lock FILE before the command: the
		// first plain operand is that value, not a command.
		switch base {
		case "timeout":
			w.expectTimeoutDuration = true
		case "chroot":
			w.expectChrootRoot = true
		case "gosu":
			w.expectGosuUser = true
		case "flock":
			w.expectFlockFile = true
		}
		w.curWrapper = base
		return true
	}
	if w.curWrapper == "find" && !w.findExecSeen {
		// find(1) expression operands up to -exec (paths, primaries like
		// -name/-type and their arguments) are transparent prefix
		// material, not commands.
		return true
	}
	switch {
	case w.expectTimeoutDuration:
		w.expectTimeoutDuration = false
		return true
	case w.expectChrootRoot:
		w.expectChrootRoot = false
		return true
	case w.expectGosuUser:
		w.expectGosuUser = false
		return true
	case w.expectFlockFile:
		w.expectFlockFile = false
		return true
	}
	return false
}

// shellPrefixOpaqueIndex returns the index of the first effective-command
// token, or -1 when every token is transparent prefix material.
func shellPrefixOpaqueIndex(toks []string, expansionsTransparent bool) int {
	w := shellPrefixWalk{expansionsTransparent: expansionsTransparent}
	for i, tok := range toks {
		if !w.transparent(tok) {
			return i
		}
	}
	return -1
}

// shellPrefixClassToken trims a leading subshell/group opener from tok for
// classification only; the token itself is never rewritten in a returned
// tail. It strips only '(' and '{' openers, not "$("/"<(" substitutions.
func shellPrefixClassToken(tok string) string {
	return strings.TrimLeft(tok, "({")
}

// effectiveCommandArgv returns seg from its effective command position:
// leading env assignments, flags, separators, shell command-opening words,
// group openers, and delegating wrappers (with their value-taking options
// and positional operands) are transparent, so a payload like
// "command sudo -u root tar ..." resolves the utility instead of the
// wrapper. The walk is the same shellPrefixWalk interpreterTailReachable
// uses; the first opaque token (an unresolvable expansion included) is the
// effective command. Returns nil when the segment is all prefix material.
func effectiveCommandArgv(seg []string) []string {
	// One leading env layer is resolved first so env(1)'s own value options
	// (-u/-C/-a) do not leave their operand looking like the command.
	if eff := resolveEnvTarget(seg); len(eff) > 0 {
		seg = eff
	}
	// Drop a leading subshell/group opener (standalone or attached) so a
	// grouped command resolves like its bare form. StripCdGroupOpeners also
	// trims the matching closer from the last token, which can leave a blank
	// artifact; the effective-command slice drops blank tokens below.
	if stripped, grouped := sandbox.StripCdGroupOpeners(seg); grouped {
		seg = stripped
	}
	if i := shellPrefixOpaqueIndex(seg, false); i >= 0 {
		tail := seg[i:]
		out := make([]string, 0, len(tail))
		for _, tok := range tail {
			if tok != "" {
				out = append(out, tok)
			}
		}
		return out
	}
	return nil
}

// unresolvedCommandAskReason fails closed when the effective command of a
// segment cannot be resolved: an empty base, a leading parameter expansion
// that could expand to any command, or a binary outside the risk table.
// Callers run hard-deny first, so a known exec-capable payload still Denies
// ("$CMD tar ${1:-cI}" with the dashless cluster); this leg covers the
// residual unknown-command case inside a carrier payload, where the ordinary
// top-level RiskUnknown ask never ran, and keeps an unresolvable lead word
// from hiding the command that follows it.
func unresolvedCommandAskReason(seg []string) string {
	eff := effectiveCommandArgv(seg)
	if len(eff) == 0 {
		return ""
	}
	// A shell carrier's packed payload is one opaque token here; the
	// payload walk resolves its command separately (and recursively).
	// Defer only when the effective tail is exactly that payload, so an
	// unresolvable word before the carrier ("$CMD sudo -s ...") still
	// fails closed.
	joined := strings.Join(eff, " ")
	for i, tok := range seg {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		if i > 0 && !interpreterTailReachable(seg, i) {
			continue
		}
		for _, p := range shellCarrierPayloadsAt(seg, i) {
			if p == joined {
				return ""
			}
		}
	}
	if resolved := resolveEnvTarget(eff); len(resolved) > 0 {
		eff = resolved
	}
	// The effective command may itself be a shell carrier ("sh -c '...'");
	// its payload walk owns the verdict, exactly like the packed sudo/su
	// payloads deferred above.
	if base := strings.ToLower(resolveBinaryBase(eff[0])); base == "sudo" || base == "su" || shellPayloadBases[base] {
		if len(shellCarrierPayloadsAt(eff, 0)) > 0 {
			return ""
		}
	}
	head := eff[0]
	if hasParamExpansion(head) {
		return "command with parameter expansion requires user approval"
	}
	stripped := stripLeadBackslashes(stripSubstPrefix(head))
	if stripped == "" {
		return "unresolved command requires user approval"
	}
	if classifyRisk(eff) == RiskUnknown {
		return "unknown command requires user approval"
	}
	return ""
}

// carrierPayloadAskEscalation runs the Ask half of the escalation circuit
// over one shell-carrier payload command line, following nested shell-mode
// carriers packed in the payload text ("su -c \"sudo -s 'tar $1'\"") with
// the shared depth budget. The hard-deny counterpart walks the same carrier
// shapes (checkSudoShellPayloadDepth / checkSuCPayload /
// checkShellCPayload) but returns Deny only; this leg supplies the
// unresolvable-tail Ask they cannot express ("sudo -s 'tar $1'" with the
// expansion packed inside the quoted operand). Payloads that cannot be
// re-lexed are left to carrierInterpreterEscalation's fail-closed Ask.
func carrierPayloadAskEscalation(payload string, depth int) *GateDecision {
	for _, seg := range shellSegmentArgvs(payload) {
		if len(seg) == 0 {
			continue
		}
		if d := shellAskLegs(seg, RiskUnknown); d != nil {
			return d
		}
		for i, tok := range seg {
			if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
				continue
			}
			if i > 0 && !interpreterTailReachable(seg, i) {
				continue
			}
			payloads := shellCarrierPayloadsAt(seg, i)
			if len(payloads) == 0 {
				continue
			}
			if depth+1 >= maxCarrierPayloadDepth {
				return &GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: carrierPayloadDepthReason}
			}
			for _, p := range payloads {
				if d := carrierPayloadAskEscalation(p, depth+1); d != nil {
					return d
				}
			}
		}
	}
	return nil
}

// carrierInterpreterEscalation runs the shared interpreter escalation
// (interpreterTails + script-content scan + opaque-code check) over one
// shell-carrier payload line (sudo -s/-i operands, su -c command, shell -c
// code). It returns a non-nil GateDecision on escalation (Deny for
// hard-deny content, Ask for opaque interpreter code or an unresolvable
// Ask-only tail), nil when clean. Payloads that fail to parse are
// fail-closed to Ask (unparseable code must not run silently under allow).
func carrierInterpreterEscalation(payload string) *GateDecision {
	segs := shellSegmentArgvs(payload)
	if len(segs) == 0 {
		return &GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: "unparseable shell-carrier payload requires user approval"}
	}
	for _, seg := range segs {
		if len(seg) == 0 {
			continue
		}
		for _, tail := range interpreterTails(seg) {
			if d := interpreterTailEscalation(tail); d != nil {
				return d
			}
		}
	}
	// Ask-only payload legs (parameter-expansion tails, chdir/env/make
	// steering) never reach the Deny-only hard-deny payload walk, so run
	// them on the same re-lexed payload after the interpreter legs. Keep
	// the interpreter verdict first: an opaque-code Ask stays the more
	// specific reason.
	if d := carrierPayloadAskEscalation(payload, 0); d != nil {
		return d
	}
	return nil
}

// maxCarrierPayloadDepth bounds nested shell-carrier payload unwrapping. A
// flat sudo chain re-parses to a shorter suffix per level; without a bound, a
// long chain of sudo -s tokens cost quadratic (and worse) time. Exceeding
// the depth fails closed: a payload that keeps hiding carriers past this
// depth is not something an operator command legitimately does.
const maxCarrierPayloadDepth = 8

// carrierPayloadDepthReason is the shared fail-closed reason for both depth
// guards (the Deny-only sudo payload walk and the Ask payload circuit), so
// the two verdicts cannot drift.
const carrierPayloadDepthReason = "shell-carrier payload nests too deeply to audit"

// checkSudoShellPayload descends into sudo -s/--shell and -i/--login
// commands (M3: "sudo -s 'rm -rf /'" runs its operands as a shell
// command, hiding rm as a single quoted token from the rm token scan).
// When -s or -i is present, the remaining operands are re-checked with the
// full hard-deny circuit breaker as one joined command line, so both
// packed ("sudo -s 'rm -rf /'") and split ("sudo -s rm -rf /") forms
// deny. Bare "sudo -s" with no command has nothing to check. Only
// command-position sudo tokens are carriers (interpreterTailReachable,
// so `echo sudo -s 'rm -rf /'` stays green). Interpreter escalation for
// the payload ("sudo -s 'python3 evil.py'") runs separately via
// carrierInterpreterEscalation in the EvaluateCommand 12.2 section.
// Boundary (pinned by test): a packed echo payload denies exactly like
// the bare command ("sudo -s 'echo rm -rf /'" denies, consistent with
// "echo rm -rf /" denying via the quote-blind rm token scan). The token
// scan treats packed payload argv as real argv; only quoted text inside
// one token ("echo \"rm -rf /\"") stays green.
//
// The payload walk is single-pass per segment and depth-bounded: only the
// first carrier needs checking (later sudo tokens are inside its payload),
// the joined payload is re-lexed and checked with the sudo leg disabled,
// and carriers that survive in the payload text (a quoted nested sudo
// command) are followed with the shared depth budget instead of recursing
// through the whole circuit breaker per suffix.
//
// The same walk owns eval(1): eval joins its operand words into a command
// line and runs it, so the joined tail is re-checked like a shell -c payload
// (checkEvalPayloadDepth) with the same nested-carrier depth budget. Attached
// subshell/group openers ("(sudo", "{eval") classify by their stripped form.
func checkSudoShellPayload(argv []string) string {
	return checkSudoShellPayloadDepth(argv, 0)
}

func checkSudoShellPayloadDepth(argv []string, depth int) string {
	for i, tok := range argv {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		base := strings.ToLower(resolveBinaryBase(shellPrefixClassToken(tok)))
		if base == "eval" {
			if i > 0 && !interpreterTailReachable(argv, i) {
				continue
			}
			// eval(1) joins every remaining operand into one command
			// line and runs it, so its whole tail is the payload.
			return checkEvalPayloadDepth(argv[i+1:], depth)
		}
		if base != "sudo" {
			continue
		}
		if i > 0 && !interpreterTailReachable(argv, i) {
			continue
		}
		payloads := sudoShellPayloads(argv[i+1:])
		for _, payload := range payloads {
			// The joined payload is re-lexed by checkHardDenyScan, so a
			// flat nested carrier ("sudo -s sudo -s rm -rf /") is
			// tokenized and checked here. The sudo leg stays disabled to
			// avoid re-parsing this same payload per carrier.
			if reason := checkHardDenyScan(nil, []string{payload}, false); reason != "" {
				return reason
			}
			// A carrier packed inside one quoted token survives the
			// re-lex: follow it iteratively with the depth budget.
			for _, pseg := range shellSegmentArgvs(payload) {
				if len(pseg) == 0 || !hasShellPayloadCarrier(pseg) {
					continue
				}
				if depth+1 >= maxCarrierPayloadDepth {
					return carrierPayloadDepthReason
				}
				if reason := checkSudoShellPayloadDepth(pseg, depth+1); reason != "" {
					return reason
				}
			}
		}
		// The first reachable sudo's tail covers every later sudo token
		// (sudoShellPayloads parses option streams across operands), so
		// the scan ends here instead of rebuilding a suffix per token
		// (quadratic on a flat sudo chain).
		return ""
	}
	return ""
}

// checkEvalPayloadDepth re-checks an eval(1) command line with the full
// hard-deny circuit. eval joins its operand words with spaces and executes
// the result, so the joined tail is a shell command line exactly like a
// shell -c code string (there is no flag prefix to parse). A carrier packed
// inside one quoted operand ("eval \"sudo -s 'rm -rf /'\"") survives the
// re-lex and is followed with the shared depth budget; a flat carrier
// ("eval sudo -s rm -rf /") tokenizes into real words and is caught by the
// re-lexed scan itself. The eval leg stays disabled in the nested scan
// (checkHardDenyScan, allowSudo=false, and checkShellCPayload does not treat
// eval as a -c shell) so the recursion is bounded by this depth counter, not
// re-entered from every level.
func checkEvalPayloadDepth(rest []string, depth int) string {
	payload := strings.Join(rest, " ")
	if strings.TrimSpace(payload) == "" {
		return ""
	}
	if reason := checkHardDenyScan(nil, []string{payload}, false); reason != "" {
		return reason
	}
	for _, pseg := range shellSegmentArgvs(payload) {
		if len(pseg) == 0 || !hasShellPayloadCarrier(pseg) {
			continue
		}
		if depth+1 >= maxCarrierPayloadDepth {
			return carrierPayloadDepthReason
		}
		if reason := checkSudoShellPayloadDepth(pseg, depth+1); reason != "" {
			return reason
		}
	}
	return ""
}

// hasEvalCarrier reports whether seg contains a reachable eval(1) token.
// Only command-position eval counts (interpreterTailReachable), so
// `echo eval 'rm -rf /'` stays green.
func hasEvalCarrier(seg []string) bool {
	for i, tok := range seg {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		if strings.ToLower(resolveBinaryBase(shellPrefixClassToken(tok))) != "eval" {
			continue
		}
		if i > 0 && !interpreterTailReachable(seg, i) {
			continue
		}
		return true
	}
	return false
}

// hasShellPayloadCarrier reports whether seg contains a reachable carrier
// whose payload the depth-bounded hard-deny walk must follow manually: sudo
// -s/-i and eval. Shell -c and su carriers are always re-checked inline by
// checkShellCPayload, so they do not need this follow.
func hasShellPayloadCarrier(seg []string) bool {
	return hasSudoShellCarrier(seg) || hasEvalCarrier(seg)
}

// hasSudoShellCarrier reports whether seg contains a reachable sudo token
// whose tail parses to a shell-mode payload. Like the payload walk, it
// inspects only the first reachable sudo: its tail already covers every
// later token.
func hasSudoShellCarrier(seg []string) bool {
	for i, tok := range seg {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		if strings.ToLower(resolveBinaryBase(shellPrefixClassToken(tok))) != "sudo" {
			continue
		}
		if i > 0 && !interpreterTailReachable(seg, i) {
			continue
		}
		return len(sudoShellPayloads(seg[i+1:])) > 0
	}
	return false
}

// normalizeHardDenySegment strips subshell/group openers from a parsed
// hard-deny segment so an attached opener on the command ("(sudo",
// "{chmod") and the trailing closer of a grouped command ("(chmod -R 777
// /)") resolve exactly like the bare form for every token matcher. The
// shared sandbox.StripCdGroupOpeners rule is the same one the command-position
// walk uses, so a grouped carrier cannot classify differently from its bare
// spelling. Ungrouped segments are returned unchanged.
func normalizeHardDenySegment(seg []string) []string {
	if stripped, grouped := sandbox.StripCdGroupOpeners(seg); grouped {
		return stripped
	}
	return seg
}

// checkArgvHardDeny runs the parsed-argv hard-deny checks (recursive
// deletion, device destruction, chmod/chown whole-tree, fork bombs).
func checkArgvHardDeny(argv []string) string {
	return checkArgvHardDenyScan(argv, true)
}

// checkArgvHardDenyScan is checkArgvHardDeny with the sudo payload
// recursion switchable; see checkHardDenyScan.
func checkArgvHardDenyScan(argv []string, allowSudo bool) string {
	if len(argv) == 0 {
		return ""
	}
	// Normalize a grouped invocation once so every token matcher below sees
	// the bare utility: "(sudo -s ..." tokenizes as "(sudo", and
	// "(chmod -R 777 /)" attaches the closer to the target ("/)"). A bare
	// form already classifies, so only a grouped segment is rewritten.
	argv = normalizeHardDenySegment(argv)
	bin := strings.ToLower(resolveBinaryBase(argv[0]))

	// 1. Recursive deletion of system/home roots (H1: token scan catches
	// wrapper-prefixed rm, not just argv[0]).
	if reason := checkRmTokenScan(argv); reason != "" {
		return reason
	}
	if reason := checkShellCPayload(argv); reason != "" {
		return reason
	}
	if allowSudo {
		if reason := checkSudoShellPayload(argv); reason != "" {
			return reason
		}
	}

	// 2. Raw device destruction.
	if reason := checkDeviceDestruction(argv); reason != "" {
		return reason
	}

	// 3. Execution-capable flags: tar --checkpoint-action /
	// --use-compress-program (-I) / --to-command / --info-script /
	// --new-volume-script and sort --compress-program run embedded
	// commands. argv[0] is command position, so tar's dashless old
	// style applies here.
	if reason := checkExecFlagHardDeny(argv, true); reason != "" {
		return reason
	}
	if reason := checkExecFlagTokenScan(argv); reason != "" {
		return reason
	}
	// 3a. Option streams injected through TAR_OPTIONS never appear as
	// argv flags, so `env TAR_OPTIONS='--to-command=id' tar ...` would
	// otherwise Allow (F4 parity for hidden option carriers).
	if reason := toolOptionEnvHardDeny(argv); reason != "" {
		return reason
	}
	// 3c. make -f/--file/--eval reads makefiles or evaluated make code the
	// gate cannot inspect; MAKEFILES carries the same payload through the
	// environment. Token scan catches wrapper-prefixed and piped make.
	if reason := checkMakeTokenScan(argv); reason != "" {
		return reason
	}

	// 3b. Git config/env influence: -c/--config-env with exec keys and
	// GIT_* exec-hook overlays deny (token scan catches wrapper-prefixed
	// and env-trampolined git, not just argv[0]).
	if reason := checkGitTokenScan(argv); reason != "" {
		return reason
	}

	// 4. chmod -R 777 /, chown -R <user> / (token scan catches
	// wrapper-prefixed forms like "sudo chmod -R 777 /").
	if bin == "chmod" && hasRecursiveChmod777(argv) {
		return "recursive world-writable permission on system directory"
	}
	if bin == "chown" && hasRecursiveFlag(argv) && hasRootTarget(argv) {
		return "recursive ownership change on root filesystem"
	}
	if reason := checkChmodTokenScan(argv); reason != "" {
		return reason
	}

	// Fork bombs in argv: :(){ :|:& };:
	if reason := detectForkBomb(argv); reason != "" {
		return reason
	}

	return ""
}

// checkStringHardDeny runs the command-string hard-deny checks against one
// view (raw or normalized): fork bombs, rm -rf patterns, device
// destruction, chmod/chown whole-tree.
func checkStringHardDeny(s string) string {
	// Fork bomb detection in strings.
	if reason := detectForkBombInString(s); reason != "" {
		return reason
	}

	// Check for rm -rf patterns in views.
	if reason := checkRmInString(s); reason != "" {
		return reason
	}

	// Check for device destruction in strings.
	if reason := checkDeviceInString(s); reason != "" {
		return reason
	}

	// Check for chmod -R 777 / and chown -R / in strings.
	if reason := checkChmodChownInString(s); reason != "" {
		return reason
	}

	return ""
}

// checkStringHardDenyNoRm runs the non-rm string checks (M3): fork bombs,
// device destruction, chmod/chown whole-tree. rm is handled quote-aware
// via per-segment parsed argv, so raw rm patterns are excluded here to
// avoid matching quoted text like 'echo "rm -rf ~"'.
func checkStringHardDenyNoRm(s string) string {
	if reason := detectForkBombInString(s); reason != "" {
		return reason
	}
	if reason := checkDeviceInString(s); reason != "" {
		return reason
	}
	if reason := checkChmodChownInString(s); reason != "" {
		return reason
	}
	return ""
}

// decodeANSICQuotes decodes bash ANSI-C $'...' and locale $"..."
// forms before gate legs run (G1 ACE-path): argv $'...' misses the --co
// prefix (shlex keeps "$--co=gzip" as one token) and the string leg
// strips '...' as quoted, so both miss. Decoding to the literal runs the
// token + string legs on the executed form while the original command is
// preserved for reason text. Plain '...' / "..." quoting is untouched,
// so quoted-echo greens ("sort --co" stays Allow) are preserved; only
// $-quoted forms decode. Unbalanced openers fail closed: the tail is left
// as-is (flag text stays visible to the quote-blind string leg, and
// shlex parse failure escalates to Ask downstream), never silently
// dropped. Returns the decoded form and whether an unbalanced opener
// was seen.
func decodeANSICQuotes(s string) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	unbalanced := false
	i := 0
	for i < len(s) {
		if s[i] == '$' && i+1 < len(s) && (s[i+1] == '\'' || s[i+1] == '"') {
			opener := s[i+1]
			if opener == '\'' {
				k := i + 2
				for k < len(s) {
					if s[k] == '\\' && k+1 < len(s) {
						k += 2
						continue
					}
					if s[k] == '\'' {
						break
					}
					k++
				}
				if k >= len(s) {
					unbalanced = true
					b.WriteString(s[i:])
					break
				}
				b.WriteString(quoteDecoded(decodeANSIInner(s[i+2 : k])))
				i = k + 1
				continue
			}
			k := i + 2
			for k < len(s) {
				if s[k] == '\\' && k+1 < len(s) {
					k += 2
					continue
				}
				if s[k] == '"' {
					break
				}
				k++
			}
			if k >= len(s) {
				unbalanced = true
				b.WriteString(s[i:])
				break
			}
			b.WriteString(quoteDecoded(decodeLocaleInner(s[i+2 : k])))
			i = k + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), unbalanced
}

// quoteDecoded preserves single-word semantics when an ANSI-C / locale
// decode yields whitespace or shell-special chars (notably shell -c
// payloads like $'rm -rf /'): the decoded content is wrapped in double
// quotes (escaping " and \) so shlex keeps it one token, exactly like the
// shell's quoted $'...' word. Flag-only decodes (--co=gzip) contain no
// whitespace and stay raw so the argv prefix match fires. Empty decodes
// stay empty (harmless, no flag hidden).
func quoteDecoded(s string) string {
	if s == "" {
		return s
	}
	if !strings.ContainsAny(s, " \t\n\r\"'\\$`") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return b.String()
}

// isHexDigit reports whether c is an ASCII hex digit.
func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// hexVal returns the numeric value of an ASCII hex digit.
func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// decodeANSIInner decodes the body of a $'...' ANSI-C quote to its
// literal: \n \t \r \' \\ \a \b \e \f \v \" \? \xHH \uHHHH \UHHHHHHHH
// octal \NNN and \cX controls. Unknown escapes drop the backslash and
// keep the char (revealing hidden text fail-closed).
func decodeANSIInner(inner string) string {
	var b strings.Builder
	b.Grow(len(inner))
	i := 0
	for i < len(inner) {
		if inner[i] != '\\' || i+1 >= len(inner) {
			b.WriteByte(inner[i])
			i++
			continue
		}
		c := inner[i+1]
		switch c {
		case 'n':
			b.WriteByte('\n')
			i += 2
		case 't':
			b.WriteByte('\t')
			i += 2
		case 'r':
			b.WriteByte('\r')
			i += 2
		case 'a':
			b.WriteByte('\a')
			i += 2
		case 'b':
			b.WriteByte('\b')
			i += 2
		case 'e', 'E':
			b.WriteByte(0x1b)
			i += 2
		case 'f':
			b.WriteByte('\f')
			i += 2
		case 'v':
			b.WriteByte('\v')
			i += 2
		case '\\':
			b.WriteByte('\\')
			i += 2
		case '\'':
			b.WriteByte('\'')
			i += 2
		case '"':
			b.WriteByte('"')
			i += 2
		case '?':
			b.WriteByte('?')
			i += 2
		case 'x':
			j := i + 2
			var v byte
			n := 0
			for j < len(inner) && n < 2 && isHexDigit(inner[j]) {
				v = v*16 + hexVal(inner[j])
				j++
				n++
			}
			if n == 0 {
				b.WriteString("\\x")
				i += 2
				continue
			}
			b.WriteByte(v)
			i = j
		case 'u':
			j := i + 2
			var v rune
			n := 0
			for j < len(inner) && n < 4 && isHexDigit(inner[j]) {
				v = v*16 + rune(hexVal(inner[j]))
				j++
				n++
			}
			if n == 0 {
				b.WriteString("\\u")
				i += 2
				continue
			}
			b.WriteRune(v)
			i = j
		case 'U':
			j := i + 2
			var v rune
			n := 0
			for j < len(inner) && n < 8 && isHexDigit(inner[j]) {
				v = v*16 + rune(hexVal(inner[j]))
				j++
				n++
			}
			if n == 0 {
				b.WriteString("\\U")
				i += 2
				continue
			}
			b.WriteRune(v)
			i = j
		case 'c':
			if i+2 >= len(inner) {
				b.WriteString("\\c")
				i += 2
				continue
			}
			ch := inner[i+2]
			if ch >= 'a' && ch <= 'z' {
				ch = ch - 'a' + 'A'
			}
			b.WriteByte(ch & 0x1f)
			i += 3
		case '0', '1', '2', '3', '4', '5', '6', '7':
			j := i + 1
			var v byte
			n := 0
			for j < len(inner) && n < 3 && inner[j] >= '0' && inner[j] <= '7' {
				v = v*8 + (inner[j] - '0')
				j++
				n++
			}
			b.WriteByte(v)
			i = j
		default:
			b.WriteByte(c)
			i += 2
		}
	}
	return b.String()
}

// decodeLocaleInner decodes the body of a $"..." locale string to its
// content: \" -> " and \\ -> \ unescape; other backslash sequences are
// kept as-is so flag text stays visible.
func decodeLocaleInner(inner string) string {
	var b strings.Builder
	b.Grow(len(inner))
	i := 0
	for i < len(inner) {
		if inner[i] == '\\' && i+1 < len(inner) {
			n := inner[i+1]
			if n == '"' || n == '\\' {
				b.WriteByte(n)
				i += 2
				continue
			}
		}
		b.WriteByte(inner[i])
		i++
	}
	return b.String()
}

// normalizeShellEvasion replaces common shell evasion tricks:
// $IFS -> space, ${IFS} -> space, $'\t'/etc -> space,
// $HOME/${HOME} -> "~" (the canonical home marker already in
// systemRoots, so "rm -rf $HOME" still hard-denies). A literal
// "/home/user" path is left alone: it names one account's directory,
// not the current-home root the policy hard-denies.
func normalizeShellEvasion(s string) string {
	s = strings.ReplaceAll(s, "${IFS}", " ")
	s = strings.ReplaceAll(s, "$IFS", " ")
	s = strings.ReplaceAll(s, "$'\\t'", " ")
	s = strings.ReplaceAll(s, "$'\\n'", " ")
	s = strings.ReplaceAll(s, "$'\\r'", " ")
	s = strings.ReplaceAll(s, "$'\\x20'", " ")
	s = strings.ReplaceAll(s, "${HOME}", "~")
	s = strings.ReplaceAll(s, "$HOME", "~")
	return s
}

// matchForbiddenGlob reports whether a deletion target matches the
// forbidden glob set ("/*", "/.*"). The globs are Unix-rooted and match a
// single top-level entry, so matching uses path.Match, which always treats
// "/" as the only "*" boundary: filepath.Match would use "\" on Windows and
// let "*" cross "/" (making "/foo/bar" match "/*"), while "/*" itself
// ("rm -rf /*") must still be denied.
func matchForbiddenGlob(t string) bool {
	for _, glob := range forbiddenGlobs {
		if t == glob {
			return true
		}
		if matched, _ := path.Match(glob, t); matched {
			return true
		}
	}
	return false
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
		// Fail-closed without -f: exact system roots still deny
		// (recursive deletion of / needs no glob or device to be
		// catastrophic). Globs and devices keep the -f requirement
		// so ordinary "rm -r ./dir" shapes stay out of scope.
		if hasRecursive && !hasForce {
			for _, t := range targets {
				tc := strings.Trim(t, "()")
				if isSystemRootTarget(tc) {
					return fmt.Sprintf("recursive deletion of %s (without force flag)", t)
				}
			}
		}
		return ""
	}

	for _, t := range targets {
		// Check exact match against system roots, canonicalized
		// (HIGH-1/F1): "/etc/", "/tmp/../etc", "~/", "$HOME/"
		// all Clean to a protected root. Raw t is kept in the
		// reason; only the comparison is canonical. A trailing
		// substitution closer ("$(rm -rf /)" leaves "/)" as one
		// token) is trimmed for the comparison only.
		tc := strings.Trim(t, "()")
		if isSystemRootTarget(tc) {
			return fmt.Sprintf("recursive force-deletion of %s", t)
		}
		// Check /* and /.* patterns (canonicalized, so a trailing
		// slash still matches).
		if matchForbiddenGlob(tc) || matchForbiddenGlob(canonicalGatePath(tc)) {
			return fmt.Sprintf("recursive force-deletion with glob %s", t)
		}
		// Check if target is a device (raw and env-expanded forms:
		// "rm -rf ${x}" with x=/dev/sda denies like the literal).
		if isDevicePath(t) || isDevicePath(canonicalGatePath(tc)) {
			return fmt.Sprintf("recursive force-deletion of device %s", t)
		}
	}

	return ""
}

// tarExecLongs are the tar(1) long options that execute embedded
// commands. Every entry denies in full and in any unambiguous getopt_long
// abbreviation; the bare --checkpoint=N counter alone is harmless and
// stays out (it has no dash suffix and is not listed here). --rsh-command
// and --rmt-command run a remote shell / rmt helper, and --info-script /
// --new-volume-script (-F) run a script per volume.
var tarExecLongs = []string{
	"checkpoint-action",
	"use-compress-program",
	"to-command",
	"info-script",
	"new-volume-script",
	"rsh-command",
	"rmt-command",
}

// tarExecFlagName reports whether a long-option name (without leading
// "--" and without any "=value") selects one of tarExecLongs, in full or
// by unambiguous getopt_long abbreviation. Names that are also a prefix
// of the harmless --checkpoint counter without carrying a dash (c, ch,
// ..., checkpoint) stay ambiguous with the counter tar itself rejects,
// so they stay out; every other prefix of exactly the exec set denies
// fail-closed (an invalid flag tar rejects is safe to deny too).
//
// Any dashed --checkpoint-* name is an action (the harmless counter has no
// dash suffix), so the legacy "--checkpoint-" prefix leg lives here too:
// every --checkpoint-a* abbreviation and even a spelling tar rejects denies
// rather than slipping past the exec-long resolver.
func tarExecFlagName(name string) bool {
	if name == "" {
		return false
	}
	name = strings.ToLower(name)
	// A dashless prefix of the harmless --checkpoint counter (c, ch,
	// ..., checkpoint) stays ambiguous with the counter tar itself
	// rejects, so it stays out even when it also prefixes
	// checkpoint-action.
	if !strings.Contains(name, "-") && strings.HasPrefix("checkpoint", name) {
		return false
	}
	if strings.HasPrefix(name, "checkpoint-") {
		return true
	}
	for _, full := range tarExecLongs {
		if name == full || strings.HasPrefix(full, name) {
			return true
		}
	}
	return false
}

// tarShortValueTakers are tar's value-taking short options: getopt consumes
// the rest of a cluster as the first value-taker's argument (or the next
// token), so nothing after it is a flag. -I/-F also take values but are
// execution-capable, so tarShortExecFlag checks them before this set.
const tarShortValueTakers = "bCfgHKLNTVX"

// tarShortExecFlag reports whether a single-dash tar token carries an
// execution-capable short flag: -I/--use-compress-program program operand
// (exact "-I", attached "-IPROG", or bundled "-xI"/"-xIv") or
// -F/--info-script / --new-volume-script script operand (exact "-F",
// attached "-FSCRIPT", or bundled "-xF"). The cluster is walked left to
// right like getopt: a plain value-taker ends option parsing for the rest
// of the token, so a directory or archive name that happens to contain F/I
// ("-C/home/Foo", "-fI", "-xvfarchive") is not an exec flag. Lowercase -i
// is unrelated and stays out.
func tarShortExecFlag(a string) bool {
	if len(a) < 2 || a[0] != '-' || a[1] == '-' {
		return false
	}
	for k := 1; k < len(a); k++ {
		switch a[k] {
		case 'I', 'F':
			return true
		}
		if strings.IndexByte(tarShortValueTakers, a[k]) >= 0 {
			return false
		}
	}
	return false
}

// tarTokenExecFlag reports whether one candidate token selects a tar
// execution-capable flag. Long options resolve through tarExecFlagName
// (unambiguous abbreviations and every --checkpoint-* action); short
// clusters resolve through tarShortExecFlag. This is the single walk shared
// by the argv, string-window, old-style, and TAR_OPTIONS legs so a spelling
// (raw, ${}-expanded, or parameter-default) cannot disagree between them.
func tarTokenExecFlag(form string) bool {
	if strings.HasPrefix(form, "--") {
		name := strings.TrimPrefix(form, "--")
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			name = name[:idx]
		}
		return tarExecFlagName(name)
	}
	return tarShortExecFlag(form)
}

// candidateForms returns every spelling a token may take after the shell
// expands its ${...} constructs: the raw token, the ${}-expanded token the
// gate's own environment produces (word-split like the callers below), and
// the substituted word of every parameter operator (${NAME:-word},
// ${1:---to-command=id}) the shell inserts when the parameter is unset or
// empty. The gate cannot know the caller's shell values, so callers walk
// every candidate fail-closed. Word-splitting each substituted word keeps
// the option-stream and old-style callers on the same candidate set as the
// argv legs (a default word may itself contain a value operand, "-I id").
func candidateForms(tok string) []string {
	forms := []string{tok}
	if expanded := os.ExpandEnv(tok); expanded != tok {
		forms = append(forms, strings.Fields(expanded)...)
	}
	for _, word := range paramDefaultWords(tok) {
		forms = append(forms, strings.Fields(word)...)
	}
	return forms
}

// tarOldStyleExecFlag reports whether GNU tar's old option style selects an
// execution-capable flag. tar reads the first argument after the binary as a
// dashless option cluster ("tar cI prog" is "tar -cI prog"), while every
// later dashless word is an operand ("tar -f out.tar cI" names a file
// called "cI"). The word is evaluated with a synthesized leading dash
// through the same left-to-right tarShortExecFlag walk, so cf/cvf/xf/tvf
// stay false (f consumes the archive value) and cI/cF/xI deny.
//
// Shell parameter expansions are resolved fail-closed: a ${NAME:-cI}
// default is what the shell puts on the command line when NAME is unset or
// empty, so the substituted word is walked too (the gate cannot know the
// caller's value of NAME). A substituted word that is already dashed is a
// flag in its own right: a long option ("${1:---to-command=id}") resolves
// through tarExecFlagName plus the "--checkpoint-" prefix leg like any
// other argv long, and a dashed short bundle is walked as-is.
func tarOldStyleExecFlag(word string) bool {
	if word == "" || word[0] == '-' {
		return false
	}
	for _, candidate := range candidateForms(word) {
		if candidate == "" {
			continue
		}
		if strings.HasPrefix(candidate, "--") {
			if tarTokenExecFlag(candidate) {
				return true
			}
			continue
		}
		form := candidate
		if form[0] != '-' {
			form = "-" + form
		}
		if tarShortExecFlag(form) {
			return true
		}
	}
	return false
}

// checkExecFlagHardDeny catches execution-capable flags that run an
// embedded command (F4, fail-closed Deny by default, no allowlist
// needed): tar --checkpoint-action / --use-compress-program (-I) /
// --to-command / --info-script / --new-volume-script (-F) /
// --rsh-command / --rmt-command (every action executes something; the bare
// --checkpoint=N counter alone is harmless and stays out) and sort
// --compress-program PROG. Unambiguous getopt_long abbreviations deny via
// tarExecFlagName, so prefix matching covers them. Flag tokens are matched
// over the shared candidateForms set (raw, ${}-expanded
// "sort --c${_H}ompress" with _H=o, and parameter-operator defaults such as
// ${1:---to-command=id}), so a substituted flag denies like the literal.
// sed -e / awk system() are opaque-code Ask via the
// interpreter-style escalation, not this hard-deny leg.
//
// oldStyle enables GNU tar's dashless first-argument form; callers pass
// false when the tar word is a plain argument of another command ("echo
// tar cI" prints text), where the dashless word is prose, not a flag.
func checkExecFlagHardDeny(argv []string, oldStyle bool) string {
	if len(argv) == 0 {
		return ""
	}
	bin := strings.ToLower(resolveBinaryBase(argv[0]))
	switch bin {
	case "tar", "gtar", "bsdtar":
		// GNU tar old option style: a dashless first argument is an
		// option cluster ("tar cI prog" is "tar -cI prog"), so the
		// short-flag walk must see it with a synthesized dash. Only
		// argv[1] is old-style; later dashless words are operands.
		if oldStyle && len(argv) > 1 && tarOldStyleExecFlag(argv[1]) {
			return "tar -I/-F program executes commands"
		}
		for _, a := range argv[1:] {
			for _, form := range candidateForms(a) {
				if !tarTokenExecFlag(form) {
					continue
				}
				if strings.HasPrefix(strings.ToLower(form), "--checkpoint-") {
					return "tar checkpoint action executes commands"
				}
				if strings.HasPrefix(form, "--") {
					return "tar execution-capable flag executes commands"
				}
				return "tar -I/-F program executes commands"
			}
		}
	case "sort":
		for _, a := range argv[1:] {
			// Same candidate walk as the tar legs: an unrecognized
			// "sort ${1:---compress-program=id}" is exactly the shell's
			// --compress-program=id when 1 is unset, so it denies like
			// the literal. Any "--co"* is either an unambiguous
			// --compress-program abbreviation ("--co" is the shortest:
			// "--c" is ambiguous with --check and rejected by sort) or an
			// invalid flag sort rejects, so fail-closed deny on the "--co"
			// prefix.
			for _, form := range candidateForms(a) {
				if strings.HasPrefix(form, "--co") {
					return "sort compress-program executes commands"
				}
			}
		}
	}
	return ""
}

// checkExecFlagTokenScan checks every token position for a tar/sort
// utility (F4): wrapper prefixes (sudo tar ...) and piped tails place
// tar/sort after argv[0], so an argv[0]-only checkExecFlagHardDeny
// misses "printf x | tar ... --checkpoint-action=..." and
// "sudo tar ...". Quoted payloads stay single tokens so echo green is
// preserved. Tar's dashless old-style cluster is only read when the tar
// word is in command position: "echo tar cI" prints prose, while
// "sudo tar cI", "then tar cI" and "printf x | tar cI" run tar
// (commandPositionFor is the shared command-position rule).
func checkExecFlagTokenScan(argv []string) string {
	for i, tok := range argv {
		if tok == "" {
			continue
		}
		if envAssignRe.MatchString(tok) {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			continue
		}
		base := strings.ToLower(resolveBinaryBase(stripSubstPrefix(tok)))
		if !tarFamilyBases[base] && base != "sort" {
			continue
		}
		// Substitution-stripped gate entry: checkExecFlagHardDeny
		// re-derives the binary from the tail's [0], so pass a corrected
		// tail copy with [0] stripped ("$(tar" -> "tar"); the unstripped
		// opener resolves to "" and the flag tail would be skipped.
		fixed := make([]string, 0, len(argv)-i)
		fixed = append(fixed, stripSubstPrefix(tok))
		fixed = append(fixed, argv[i+1:]...)
		oldStyle := commandPositionFor(argv, i)
		if reason := checkExecFlagHardDeny(fixed, oldStyle); reason != "" {
			return reason
		}
	}
	return ""
}

// stripQuotedRegions blanks single- and double-quoted regions (replaced
// with a space so word boundaries survive) before quote-blind string-leg
// regexes run: quoted text is data, never an executed flag ("echo
// \"tar --checkpoint-action=...\"", "grep -r \"sort --compress-program\"",
// "git commit -m ..."). Only balanced pairs are stripped; an unbalanced
// opener is left alone fail-closed.
func stripQuotedRegions(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '\'' && c != '"' {
			b.WriteByte(c)
			i++
			continue
		}
		j := i + 1
		closed := false
		for j < len(s) {
			if c == '"' && s[j] == '\\' && j+1 < len(s) {
				j += 2
				continue
			}
			if s[j] == c {
				closed = true
				break
			}
			j++
		}
		if !closed {
			b.WriteByte(c)
			i++
			continue
		}
		b.WriteByte(' ')
		i = j + 1
	}
	return b.String()
}

// checkExecFlagInString is the string-leg twin of checkExecFlagHardDeny
// (F4): substitution bodies and packed payloads tokenize with affixed
// closers, so the argv leg alone misses them. Quoted regions are stripped
// first: the string leg is otherwise quote-blind and would deny quoted
// text. Longs resolve through tarExecFlagName (abbreviation-aware) and
// the sort "--co" prefix exactly like the argv leg; the bare
// --checkpoint=N counter has no dash suffix and stays out. Short -I
// bundles match the same tarShortExecFlag rule. make's -f/--file/--eval
// and MAKEFILES/MAKEFLAGS are scanned through the same shared option tail
// (makeWindowHasExecFlag), so a normalized $IFS spelling
// ("make$IFS-f evil.mk") denies like the literal.
func checkExecFlagInString(s string) string {
	s = stripQuotedRegions(s)
	lowered := strings.ToLower(s)
	for _, bin := range []string{"tar", "gtar", "bsdtar", "sort", "make"} {
		for pos := 0; pos < len(lowered); {
			idx := strings.Index(lowered[pos:], bin)
			if idx < 0 {
				break
			}
			start := pos + idx
			// Word boundary: the binary name must not be a suffix of
			// a longer word ("guitar" is not tar) or of a filename
			// ("archive.tar" is not the tar binary). Shell words can
			// carry dots, so a dot before the match keeps it out.
			if start > 0 && (isWordChar(s[start-1]) || s[start-1] == '.') {
				pos = start + len(bin)
				continue
			}
			// Window: from the binary word to the next shell
			// segment boundary (unquoted by construction here:
			// quotes were stripped above).
			end := len(s)
			for _, sep := range []string{";", "\n", "\r", "&&", "||", "|", "&"} {
				if k := strings.Index(s[start:], sep); k >= 0 && start+k < end {
					end = start + k
				}
			}
			window := s[start:end]
			if tarFamilyBases[bin] && tarWindowHasExecFlag(window, stringCommandPosition(s, start)) {
				return "tar execution-capable flag executes commands"
			}
			if bin == "sort" && sortWindowHasExecFlag(window) {
				return "sort compress-program executes commands"
			}
			if bin == "make" && makeWindowHasExecFlag(window) {
				return "make -f/--file/--eval runs uninspected makefiles (hard deny)"
			}
			pos = start + len(bin)
		}
	}
	return ""
}

// isWordChar reports whether c continues a command word.
func isWordChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// stringCommandPosition reports whether the binary word starting at s[start]
// sits in command position: only transparent prefix material (wrapper
// binaries, flags, env assignments, shell command-opening words) may precede
// it in the current segment. "echo tar cI" prints prose, while "$(tar cI)",
// "(tar cI)", "x | tar cI", "then tar cI" and "sudo tar cI" all run tar.
// Quotes were stripped by the caller, so the raw bytes are shell words.
func stringCommandPosition(s string, start int) bool {
	prefix := s[:start]
	cut := 0
	for i := 0; i < len(prefix); i++ {
		switch prefix[i] {
		case ';', '\n', '\r', '|', '&', '`', '(', ')', '{', '}':
			cut = i + 1
		}
	}
	prefix = strings.TrimSpace(prefix[cut:])
	if prefix == "" {
		return true
	}
	argv := parseCommandArgv(prefix)
	if len(argv) == 0 {
		return true
	}
	argv = append(argv, "tar")
	return commandPositionFor(argv, len(argv)-1)
}

// shellCommandPrefixWords are words that keep a following word in command
// position without being the command themselves: shell reserved words that
// open a command or a condition ("then", "do", "elif", "!", "if", "while",
// "until") and builtins that execute or modify their operand ("command",
// "exec", "builtin", "eval", "nocorrect", "noglob", braces, parentheses).
// eval also re-lexes its operands as a command line (see checkEvalPayloadDepth
// and shellCarrierPayloadsAt), so the quoted payload form is a carrier.
// Delegating binaries are not listed: interpreterTailReachable owns that set
// and their value-taking options. The shared shellPrefixWalk treats these
// words as transparent, keeping "then tar cI", "! tar cI", "( tar cI",
// "eval tar cI" and "command tar cI" in command position, so a plain argument
// ("echo tar cI") is the only thing that reads as prose. coproc/case are the
// same prefix-opacity class and remain an explicit follow-up (they are not
// silently asserted closed here); a case-pattern body needs its own opener
// rule rather than a plain transparent word.
var shellCommandPrefixWords = map[string]bool{
	"!": true, "if": true, "then": true, "else": true, "elif": true,
	"while": true, "until": true, "do": true, "{": true, "}": true,
	"(": true, ")": true,
	"command": true, "exec": true, "builtin": true, "eval": true,
	"nocorrect": true, "noglob": true,
}

// commandPositionFor reports whether argv[i] sits in command position:
// either it leads the argv (i == 0), or every preceding token is transparent
// prefix material. It is a thin alias for interpreterTailReachable, the
// single shared walk: shell command-opening words, group openers, and
// delegating wrappers are all transparent there, so a plain argument
// ("echo tar cI") is the only thing that reads as prose.
func commandPositionFor(argv []string, i int) bool {
	return interpreterTailReachable(argv, i)
}

// tarWindowHasExecFlag scans one tar command window for execution-capable
// flags: --longs via tarExecFlagName and short -I/-F bundles via
// tarShortExecFlag, each over the shared candidateForms set (raw,
// ${}-expanded, and parameter-operator defaults), plus (only in command
// position) GNU tar's old option style (a dashless first argument after the
// binary, "tar cI prog"). Quoting was stripped by the caller, so quoted text
// never reaches here.
func tarWindowHasExecFlag(window string, oldStyle bool) bool {
	fields := strings.Fields(window)
	for i, tok := range fields {
		for _, form := range candidateForms(tok) {
			if tarTokenExecFlag(form) {
				return true
			}
		}
		// Old option style: only the dashless word right after the
		// binary is a flag cluster; later dashless words are operands.
		if oldStyle && i == 0 && len(fields) > 1 && tarFamilyBases[strings.ToLower(resolveBinaryBase(stripSubstPrefix(tok)))] && tarOldStyleExecFlag(fields[1]) {
			return true
		}
	}
	return false
}

// sortWindowHasExecFlag scans one sort command window for the
// --compress-program flag (any "--co"* abbreviation, the shortest
// unambiguous prefix; "--c" is ambiguous with --check and rejected by
// sort, so it stays out of the argv leg but the string leg keeps the
// historical "--co" prefix rule).
func sortWindowHasExecFlag(window string) bool {
	for _, tok := range strings.Fields(window) {
		for _, form := range candidateForms(tok) {
			if strings.HasPrefix(form, "--co") {
				return true
			}
		}
	}
	return false
}

// checkDeviceDestruction checks argv for device destruction patterns
// (dd, mkfs, shred, fdisk, parted).
func checkDeviceDestruction(argv []string) string {
	bin := strings.ToLower(resolveBinaryBase(argv[0]))

	switch bin {
	case "dd":
		for _, a := range argv[1:] {
			if strings.HasPrefix(a, "of=") {
				target := strings.TrimPrefix(a, "of=")
				// ${}-expanded too: "dd of=${x}" with x=/dev/vda
				// denies like the literal.
				if isDevicePath(target) || isDevicePath(os.ExpandEnv(target)) {
					return fmt.Sprintf("raw device write to %s via dd", target)
				}
			}
		}
	case "mkfs", "mkfs.ext2", "mkfs.ext3", "mkfs.ext4",
		"mkfs.xfs", "mkfs.btrfs", "mkfs.vfat", "mkfs.fat",
		"mkfs.ntfs", "mkfs.exfat":
		for _, a := range argv[1:] {
			if isDevicePath(a) || isDevicePath(os.ExpandEnv(a)) {
				return fmt.Sprintf("filesystem creation on device %s", a)
			}
		}
	case "shred":
		for _, a := range argv[1:] {
			if isDevicePath(a) || isDevicePath(os.ExpandEnv(a)) {
				return fmt.Sprintf("secure deletion of device %s", a)
			}
		}
	case "fdisk", "parted":
		for _, a := range argv[1:] {
			if isDevicePath(a) || isDevicePath(os.ExpandEnv(a)) {
				return fmt.Sprintf("partition table modification on device %s", a)
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
	if matches := rmRe.FindStringSubmatch(s); len(matches) >= 3 {
		targets := strings.Fields(matches[2])
		for _, t := range targets {
			tc := strings.Trim(t, "()")
			if isSystemRootTarget(tc) {
				return fmt.Sprintf("recursive force-deletion of %s", t)
			}
			if matchForbiddenGlob(tc) || matchForbiddenGlob(canonicalGatePath(tc)) {
				return fmt.Sprintf("recursive force-deletion with glob %s", t)
			}
			if isDevicePath(t) || isDevicePath(canonicalGatePath(tc)) {
				return fmt.Sprintf("recursive force-deletion of device %s", t)
			}
		}
		return ""
	}
	// Fail-closed without -f: recursive-only deletion of an exact system
	// root still denies ("rm -r /"). Globs and devices keep the -f
	// requirement so ordinary recursive removes stay out of scope.
	rmNoForceRe := regexp.MustCompile(`rm\s+((?:-[^\s]*[rR][^\s]*\s+)|(?:--recursive\s+))(.+)`)
	if matches := rmNoForceRe.FindStringSubmatch(s); len(matches) >= 3 {
		// The flag cluster must not smuggle -f in a combined form
		// ("rm -rf /" is handled above, but "--recursive -f" style
		// tails must not double-count here either).
		if strings.Contains(matches[1], "f") || strings.Contains(matches[1], "F") {
			return ""
		}
		for _, t := range strings.Fields(matches[2]) {
			tc := strings.Trim(t, "()")
			if isSystemRootTarget(tc) {
				return fmt.Sprintf("recursive deletion of %s (without force flag)", t)
			}
		}
	}
	return ""
}

// checkDeviceInString checks a normalized string for device destruction patterns.
func checkDeviceInString(s string) string {
	// dd of=/dev/* pattern (shared device alternation).
	ddRe := regexp.MustCompile(`dd\s+.*\bof=(/dev/` + deviceNameAlternation + `)`)
	if m := ddRe.FindStringSubmatch(s); len(m) > 1 {
		return fmt.Sprintf("raw device write to %s via dd", m[1])
	}

	// mkfs targeting /dev/* pattern.
	mkfsRe := regexp.MustCompile(`mkfs\S*\s+.*(/dev/` + deviceNameAlternation + `)`)
	if m := mkfsRe.FindStringSubmatch(s); len(m) > 1 {
		return fmt.Sprintf("filesystem creation on device %s", m[1])
	}

	// shred targeting /dev/* pattern.
	shredRe := regexp.MustCompile(`shred\s+.*(/dev/` + deviceNameAlternation + `)`)
	if m := shredRe.FindStringSubmatch(s); len(m) > 1 {
		return fmt.Sprintf("secure deletion of device %s", m[1])
	}

	// fdisk/parted targeting /dev/* pattern.
	fdiskRe := regexp.MustCompile(`(?:fdisk|parted)\s+.*(/dev/` + deviceNameAlternation + `)`)
	if m := fdiskRe.FindStringSubmatch(s); len(m) > 1 {
		return fmt.Sprintf("partition table modification on device %s", m[1])
	}

	// Redirect to device: > /dev/sd*, /dev/vd*, ..., /dev/mapper/*.
	redirectRe := regexp.MustCompile(`[>]+\s*(/dev/` + deviceNameAlternation + `)`)
	if m := redirectRe.FindStringSubmatch(s); len(m) > 1 {
		return fmt.Sprintf("redirect to device %s", m[1])
	}

	return ""
}

// checkChmodChownInString checks a normalized string for chmod/chown -R on root.
func checkChmodChownInString(s string) string {
	// chmod -R 777 / (or /etc, /usr); leading zeros ("0777") are the
	// same mode and must match exactly like the bare form.
	chmodRe := regexp.MustCompile(`chmod\s+[^ ]*[rR][^ ]*\s+0*777\s+(/\S*)`)
	if m := chmodRe.FindStringSubmatch(s); len(m) > 1 {
		// Canonicalized (HIGH-1/F1): "777 /etc/" Cleans to "/etc".
		target := canonicalGatePath(m[1])
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
		// Canonicalized: "chown -R user //" Cleans to "/".
		if canonicalGatePath(m[1]) == "/" {
			return "recursive ownership change on root filesystem"
		}
	}

	return ""
}

// ---- Git config/env influence, code-tool programs, chmod subpaths,
// parameter expansion (fail-closed Ask/Deny, never Allow low) ---- //

// gitExecConfigKeys are git -c/--config-env key names (lowercased) whose
// values execute commands or select executable programs: a -c pair with
// one of these keys denies, exactly like an execution-capable tar flag.
var gitExecConfigKeys = map[string]bool{
	"core.pager": true, "core.editor": true, "core.fsmonitor": true,
	"core.sshcommand": true, "core.askpass": true,
	"diff.external":          true,
	"interactive.difffilter": true, "sequence.editor": true,
}

// gitExecConfigPrefixes are key prefixes (lowercased) whose values name
// executable programs (pager drivers, mergetool/difftool commands).
var gitExecConfigPrefixes = []string{"pager.", "mergetool.", "difftool."}

// gitExecConfigKeyDeny reports whether a git -c/--config-env key=value pair
// selects command execution. Alias values starting with "!" run through
// the shell; other aliases are Ask-level config influence, not deny.
func gitExecConfigKeyDeny(key, val string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if gitExecConfigKeys[key] {
		return true
	}
	for _, p := range gitExecConfigPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	if strings.HasPrefix(key, "alias.") {
		// Quote-tolerant like the on-disk legs: "-c alias.x='!cmd'"
		// still runs a shell command after git strips one quote layer.
		unquoted := strings.Trim(strings.TrimSpace(val), "\"'")
		if strings.HasPrefix(strings.TrimSpace(unquoted), "!") {
			return true
		}
	}
	return false
}

// gitDenyEnvKeys are exact GIT_* environment variable names that inject
// command execution or repository config into a git invocation. The
// GIT_EDITOR overlay the git engine sets internally (non-interactive
// true) and GIT_TERMINAL_PROMPT (fail-fast prompts) are allowlisted out:
// they are required for the engine's own non-interactive runs.
var gitDenyEnvKeys = map[string]bool{
	"GIT_EXTERNAL_DIFF": true, "GIT_PAGER": true, "GIT_SSH": true,
	"GIT_SSH_COMMAND": true, "GIT_ASKPASS": true,
	"GIT_CONFIG_COUNT": true, "GIT_CONFIG_PARAMETERS": true,
	"GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_SYSTEM": true,
	"GIT_CONFIG": true,
}

// gitEnvAssignDeny reports whether a VAR=val environment assignment
// injects command execution or config into git: any GIT_CONFIG_* name
// (COUNT/KEY_*/VALUE_*/PARAMETERS/GLOBAL/SYSTEM and future keys) or one
// of the exec-hook names above. GIT_EDITOR denies unless it carries the
// engine's own non-interactive sentinel (true/false): any other value
// names an executable editor hook.
func gitEnvAssignDeny(key, val string) bool {
	if key == "GIT_EDITOR" {
		return val != "true" && val != "false"
	}
	if gitDenyEnvKeys[key] {
		return true
	}
	if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
		return true
	}
	if strings.HasPrefix(key, "GIT_CONFIG_") {
		return true
	}
	return false
}

// gitEnvAssignAskSkipped are GIT_* assignments the git engine sets
// itself for non-interactive runs: GIT_TERMINAL_PROMPT (fail-fast
// prompts, any value) and GIT_EDITOR carrying the non-interactive
// sentinel (true/false) never Ask. Any other GIT_EDITOR value is an
// executable editor hook and falls through to the deny leg.
func gitEnvAssignAskSkipped(key, val string) bool {
	if key == "GIT_TERMINAL_PROMPT" {
		return true
	}
	return key == "GIT_EDITOR" && (val == "true" || val == "false")
}

// gitOperandUsesExtTransport reports whether a bare git operand selects
// the ext:: transport (quote-tolerant, scheme case-insensitive): git
// executes the remainder as a command on clone/fetch/ls-remote. Only
// non-flag operands past the subcommand reach this check, so flags and
// their values never fire; a file literally named "ext::*" over-asks
// (fail-closed, approval clears it).
func gitOperandUsesExtTransport(val string) bool {
	unquoted := strings.Trim(val, "\"'")
	trimmed := strings.TrimSpace(unquoted)
	return strings.HasPrefix(strings.ToLower(trimmed), "ext::")
}

// splitGitConfigKV cuts a "key=value" -c operand at the first "=".
// Missing "=" yields an empty value (real git rejects it; fail-closed
// asks downstream).
func splitGitConfigKV(kv string) (string, string) {
	if idx := strings.IndexByte(kv, '='); idx >= 0 {
		return kv[:idx], kv[idx+1:]
	}
	return kv, ""
}

// checkGitTailDeny scans one git-led tail (tail[0] is the git token) for
// execution-capable configuration and denies: -c/--config-env with an
// exec key ("-c core.pager=id", "--config-env core.pager=EVIL"), in
// separate, attached ("-cfoo=bar"), or "=opt" form. Only the global
// option region before the subcommand is scanned: a subcommand's own -c
// ("git branch -c old new" copies a branch) is not a config override.
// Values are ${}-expanded first, so "-c core.pager=${x}" denies exactly
// like the literal.
func checkGitTailDeny(tail []string) string {
	for j := 1; j < len(tail); j++ {
		a := tail[j]
		if a == "-c" || a == "--config-env" {
			if j+1 >= len(tail) {
				continue
			}
			key, val := splitGitConfigKV(os.ExpandEnv(tail[j+1]))
			if gitExecConfigKeyDeny(key, val) {
				return fmt.Sprintf("git %s selects command execution (%s)", a, strings.TrimSpace(key))
			}
			j++
			continue
		}
		if strings.HasPrefix(a, "--config-env=") {
			key, _ := splitGitConfigKV(strings.TrimPrefix(a, "--config-env="))
			if gitExecConfigKeyDeny(os.ExpandEnv(key), "") {
				return fmt.Sprintf("git --config-env selects command execution (%s)", strings.TrimSpace(key))
			}
			continue
		}
		if len(a) > 2 && a[0] == '-' && a[1] != '-' {
			// Short cluster: attached "-cfoo=bar" carries the -c
			// operand in the remainder; a -C directory override
			// consumes the remainder or the next token.
			if idx := strings.IndexByte(a[1:], 'c'); idx >= 0 {
				if rest := a[idx+2:]; strings.Contains(rest, "=") {
					key, val := splitGitConfigKV(os.ExpandEnv(rest))
					if gitExecConfigKeyDeny(key, val) {
						return fmt.Sprintf("git -c selects command execution (%s)", strings.TrimSpace(key))
					}
				}
			}
			if idx := strings.IndexByte(a[1:], 'C'); idx >= 0 && idx+2 < len(a) {
				continue
			}
			if idx := strings.IndexByte(a[1:], 'C'); idx >= 0 && idx+2 == len(a) {
				j++
			}
			continue
		}
		if strings.HasPrefix(a, "-") || envAssignRe.MatchString(a) {
			// -C/--git-dir/--work-tree take a separate directory
			// operand: skip it so a directory named "-c" (or an
			// exec-looking key) is never evaluated as config.
			if a == "-C" || a == "--git-dir" || a == "--work-tree" {
				j++
			}
			continue
		}
		// First non-flag token: the subcommand. Global options end here.
		break
	}
	return ""
}

// checkGitTokenScan checks every token position for a git utility (H1):
// wrapper prefixes (sudo git ...), piped tails, and env trampolines
// ("env GIT_EXTERNAL_DIFF=x git status") place git after argv[0], so an
// argv[0]-only check misses them. Any token whose resolved base is git
// runs checkGitTailDeny on its tail; every VAR=val assignment ahead of
// the git token is evaluated as an env overlay (exec-hook keys deny).
// Quoted payloads stay single tokens ("git -c ..." != git) so echo green
// is preserved.
func checkGitTokenScan(argv []string) string {
	for i, tok := range argv {
		if tok == "" {
			continue
		}
		if strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		if gitScanBase(tok) != "git" {
			continue
		}
		fixed := make([]string, 0, len(argv)-i)
		fixed = append(fixed, stripSubstPrefix(tok))
		fixed = append(fixed, argv[i+1:]...)
		if reason := checkGitTailDeny(fixed); reason != "" {
			return reason
		}
		for _, a := range argv[:i] {
			if !envAssignRe.MatchString(a) {
				continue
			}
			key := a[:strings.IndexByte(a, '=')]
			if gitEnvAssignDeny(key, a[len(key)+1:]) {
				return fmt.Sprintf("git environment %s injects command execution", key)
			}
		}
	}
	return ""
}

// gitTailAskReason returns a non-empty Ask reason when a git-led tail
// carries config influence below the deny line: any other -c/--config-env
// pair, an attached/bare -c value, -C/--git-dir/--work-tree directory
// overrides, a ${} in a -c value, any other GIT_* assignment ahead of
// git, or a bare ext:: transport operand past the subcommand (clone,
// fetch, ls-remote ... execute the remainder as a command). Only the
// global option region before the subcommand is scanned for options;
// operands are scanned only for the ext:: scheme (never a legitimate
// ref or flag value: ":" cannot appear in a ref name, and flags keep
// their "-" prefix). Never Allow low: the caller escalates to Ask
// (approval can clear a benign override after inspection).
func gitTailAskReason(tail []string, assigns []string) string {
	subIdx := -1
	for j := 1; j < len(tail); j++ {
		a := tail[j]
		if a == "-c" || a == "--config-env" {
			if j+1 >= len(tail) {
				return fmt.Sprintf("git %s without a value requires user approval", a)
			}
			if strings.Contains(tail[j+1], "${") {
				return "git -c value with parameter expansion requires user approval"
			}
			return fmt.Sprintf("git %s config override requires user approval", a)
		}
		if strings.HasPrefix(a, "--config-env=") {
			return "git --config-env override requires user approval"
		}
		if a == "-C" || a == "--git-dir" || a == "--work-tree" ||
			strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") {
			return "git directory override requires user approval"
		}
		if len(a) > 2 && a[0] == '-' && a[1] != '-' {
			if idx := strings.IndexByte(a[1:], 'c'); idx >= 0 {
				if rest := a[idx+2:]; rest == "" {
					return "git -c without a value requires user approval"
				} else if strings.Contains(rest, "=") {
					if strings.Contains(rest, "${") {
						return "git -c value with parameter expansion requires user approval"
					}
					return "git -c config override requires user approval"
				}
			}
			// A -C directory override bundled in a short cluster
			// ("-Cdir"): the remainder is the directory operand.
			if strings.IndexByte(a[1:], 'C') >= 0 {
				return "git directory override requires user approval"
			}
			continue
		}
		if strings.HasPrefix(a, "-") || envAssignRe.MatchString(a) {
			// -C takes a separate directory operand: skip it so a
			// directory name is never evaluated as config.
			if a == "-C" {
				j++
			}
			continue
		}
		// First non-flag token: the subcommand. Global options end
		// here (a subcommand's own -c like "git branch -c" is not a
		// config override). Operands past it are scanned for ext::
		// below.
		subIdx = j
		break
	}
	if subIdx >= 0 {
		for _, o := range tail[subIdx+1:] {
			if o == "" || strings.HasPrefix(o, "-") || envAssignRe.MatchString(o) {
				continue
			}
			if gitOperandUsesExtTransport(o) {
				return "git ext:: transport executes a command and requires user approval"
			}
		}
	}
	for _, a := range assigns {
		if !envAssignRe.MatchString(a) {
			continue
		}
		key := a[:strings.IndexByte(a, '=')]
		val := a[len(key)+1:]
		if !strings.HasPrefix(key, "GIT_") || gitEnvAssignAskSkipped(key, val) {
			continue
		}
		if gitEnvAssignDeny(key, val) {
			continue // deny leg reports these, not the Ask leg
		}
		return fmt.Sprintf("git environment %s override requires user approval", key)
	}
	return ""
}

// gitTokenAskReason scans every token position for git and returns the
// Ask-level config-influence reason (deny stays in checkGitTokenScan).
func gitTokenAskReason(argv []string) string {
	for i, tok := range argv {
		if tok == "" {
			continue
		}
		if strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		if gitScanBase(tok) != "git" {
			// Env-trampolined -S payloads hide assignments from the
			// raw token scan (`env -S 'GIT_TRACE=1 git status'`
			// parses the payload as one token): scan the expanded
			// stream too, assignments preserved. Recursion is
			// guarded by containsEnvSplitString so each level
			// consumes one -S layer: without a split-string the
			// direct fixed-tail scan below already covers git,
			// and unguarded re-expansion is exponential in the
			// env-layer count (n=24 took 22.9s, n=25 timed out).
			if gitScanBase(tok) == "env" && containsEnvSplitString(argv[i+1:]) {
				if expanded, bad := expandEnvSplitString(argv[i+1:]); !bad {
					if reason := gitTokenAskReason(expanded); reason != "" {
						return reason
					}
				}
			}
			continue
		}
		fixed := make([]string, 0, len(argv)-i)
		fixed = append(fixed, stripSubstPrefix(tok))
		fixed = append(fixed, argv[i+1:]...)
		if reason := gitTailAskReason(fixed, argv[:i]); reason != "" {
			return reason
		}
	}
	return ""
}

// envLongIsChdir reports whether an env(1) long-option name (without
// leading dashes or =value) uniquely resolves to --chdir by the same
// unique-prefix rule as resolveEnvLong. Only the chdir operand steers
// the utility's working directory; --unset/--argv0 values do not, so
// only chdir triggers the directory-override Ask.
func envLongIsChdir(name string) bool {
	var match string
	count := 0
	for _, full := range envLongOptions {
		if full == name {
			match = full
			count = 1
			break
		}
		if strings.HasPrefix(full, name) {
			match = full
			count++
		}
	}
	return count == 1 && match == "chdir"
}

// envChdirReason is the Ask reason for a proven env(1) -C/--chdir layer.
// envSplitNestingReason is the Ask reason when a -S/--split-string value
// cannot be expanded (unparseable or over the splice budget): -C is not
// proven, so the -C reason must never be used for this cause.
const (
	envChdirReason        = "env -C directory override requires user approval"
	envSplitNestingReason = "env --split-string nesting requires user approval"
)

// hasEnvChdirOpt reports whether an env(1) option stream (the tokens
// after env, before any -- or utility) carries -C/--chdir, including
// attached values (-Cdir), unique abbreviations (--ch, --chd), and =/-
// separated values (--chdir=/x, --chdir /x): the utility runs in the
// named directory, so the audit must treat it like a git -C override.
// -S/--split-string values are expanded first, exactly like the
// trampoline unwrap, so `env -S '-C /hook git status'` is not missed.
// Stops at the utility position or "--": later tokens are operands, not
// options. A VAR=val assignment before a nested env does not stop the
// scan (SE-1: `env -S 'X=1 env -C /evil git status'` splices to ["X=1"
// "env" "-C" ...], and the inner env chdirs exactly like a literal -C),
// so a nested env utility re-enters the scan on its own option stream.
// Recursion strictly decreases: each re-entry consumes one env token via
// rest[i+1:] (or rest[i+2:] past "--"), so adversarial nesting terminates
// without a cap.
//
// Tri-state: returns (chdir, unproven). chdir is set only when -C/--chdir
// is proven in the (expanded) option stream. unproven is set when a -S
// value cannot be expanded (unparseable or over-budget) or a residual -S
// survives expansion: -C can no longer be proven, but it cannot be ruled
// out either, so callers Ask with the nesting reason, never the -C one.
// Both signals propagate through the nested-env recursion, so a --
// boundary (`env -- env -S` with a dangling split-string) reports
// unproven nesting instead of a false -C.
func hasEnvChdirOpt(rest []string) (chdir bool, unproven bool) {
	return scanEnvChdirOpt(rest)
}

// envChainCoveredTokens reports how many leading tokens of rest belong to
// the env(1) option chain that scanEnvChdirOpt consumes, in rest's own
// coordinates. It is deliberately conservative: a -S/--split-string flag
// covers the flag and its value and then stops, because the spliced words
// are not input positions and a spliced wrapper can put the following input
// tokens behind the utility. Under-reporting only re-scans a position;
// over-reporting would skip one the scan did not consume (the regression
// this pins: `env -S 'sudo' env -C /hook git status` must scan the trailing
// env, and `env -S 'env' env -C /hook git status` must not skip it because
// the expanded stream is longer than the input). The span never crosses a
// token the scan did not reach.
func envChainCoveredTokens(rest []string) int {
	i := 0
	for i < len(rest) {
		a := rest[i]
		if a == "--" {
			if i+1 < len(rest) && gitScanBase(rest[i+1]) == "env" {
				return i + 2 + envChainCoveredTokens(rest[i+2:])
			}
			return i + 1
		}
		if a == "-" {
			return i
		}
		if envAssignRe.MatchString(a) {
			i++
			continue
		}
		if strings.HasPrefix(a, "--") {
			name := strings.TrimPrefix(a, "--")
			hasVal := false
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				name = name[:idx]
				hasVal = true
			}
			switch resolveEnvLong(name) {
			case envLongSplitString:
				if hasVal || i+1 >= len(rest) {
					return i + 1
				}
				return i + 2
			case envLongTakesValue:
				if hasVal {
					i++
				} else if i+1 < len(rest) {
					i += 2
				} else {
					return i
				}
			case envLongBoolean:
				i++
			default:
				return i
			}
			continue
		}
		if len(a) > 1 && a[0] == '-' {
			if k := envShortSplitPos(a); k >= 0 {
				if k+1 < len(a) || i+1 >= len(rest) {
					return i + 1
				}
				return i + 2
			}
			consumedNext := false
			for k := 1; k < len(a); k++ {
				if a[k] == 'C' || a[k] == 'u' || a[k] == 'a' {
					consumedNext = k+1 >= len(a)
					break
				}
			}
			if consumedNext {
				if i+1 >= len(rest) {
					return i
				}
				i += 2
			} else {
				i++
			}
			continue
		}
		if gitScanBase(a) == "env" {
			return i + 1 + envChainCoveredTokens(rest[i+1:])
		}
		return i
	}
	return i
}

// scanEnvChdirOpt is hasEnvChdirOpt's parse: the option stream after a
// literal or spliced nested env, expanded to the split-string fixpoint so a
// smuggled -C is seen. It returns only the tri-state: callers that scan
// several env positions must use envChainCoveredTokens for the skip span,
// never a count derived from the expanded stream (whose coordinates no
// longer match the input).
func scanEnvChdirOpt(rest []string) (chdir bool, unproven bool) {
	expanded, bad := expandEnvSplitString(rest)
	if bad {
		return false, true
	}
	rest = expanded
	i := 0
	for i < len(rest) {
		a := rest[i]
		if a == "--" {
			// `env -- env -C /hook ls` executes the inner env
			// with its chdir: scan the nested option stream.
			// (After --, assignments are operands, so only a
			// bare nested env re-enters.) Anything else after
			// -- is an operand, never an option, so -C there
			// proves nothing.
			if i+1 < len(rest) && gitScanBase(rest[i+1]) == "env" {
				return scanEnvChdirOpt(rest[i+2:])
			}
			return false, false
		}
		if a == "-" {
			return false, false
		}
		if strings.HasPrefix(a, "--") {
			name := strings.TrimPrefix(a, "--")
			hasVal := false
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				name = name[:idx]
				hasVal = true
			}
			switch resolveEnvLong(name) {
			case envLongTakesValue:
				if envLongIsChdir(name) {
					return true, false
				}
				if !hasVal {
					i++
				}
				i++
				continue
			case envLongBoolean:
				i++
				continue
			default:
				return false, false
			}
		}
		if len(a) > 1 && a[0] == '-' {
			for k := 1; k < len(a); k++ {
				c := a[k]
				if c == 'C' {
					return true, false
				}
				if c == 'u' || c == 'a' {
					if k+1 >= len(a) {
						i++
					}
					break
				}
			}
			i++
			continue
		}
		if envAssignRe.MatchString(a) {
			i++
			continue
		}
		// SE-1: the option stream ended at a nested env utility
		// (`X=1 env -C /evil ...`): the inner layer's options steer
		// the same process, so scan them too instead of treating
		// the nested env as an opaque operand.
		if gitScanBase(a) == "env" {
			return scanEnvChdirOpt(rest[i+1:])
		}
		return false, false
	}
	return false, false
}

// envChdirAskReason returns a non-empty Ask reason when any env(1) layer in
// command position in the segment carries -C/--chdir: the utility runs in
// the named directory, which a shell trampoline ("env -C /hook sh -c '...'")
// can hide from the git-led scan, so it mirrors the git -C/--git-dir
// directory-override Ask (never Allow low, even for non-git-led utilities).
// Env positions in command position are scanned like the other H1 env legs
// (sudo env -C ...); positions inside a chain already covered by an earlier
// scan are skipped. A settled plain utility does not stop the scan: the
// settled word is attacker-chosen and may delegate (time) or permute its
// operands (make/tar getopt), leaving a later env -C live, so trailing env
// positions are rescanned. A work budget fails closed with the split-string
// nesting reason on pathological chains. There is no second expansion and no
// contains gate: the tri-state return already separates proven chdir from
// unproven nesting, so `env -- env -S` (dangling split-string past --)
// reports nesting, never a false -C. Proven chdir wins over unproven nesting
// globally.
func envChdirAskReason(seg []string) string {
	sawChdir := false
	sawUnproven := false
	over := forEachEnvPosition(seg, false, true, func(i int, _ []string) bool {
		chdir, unproven := scanEnvChdirOpt(seg[i+1:])
		if chdir {
			sawChdir = true
			return false
		}
		if unproven {
			sawUnproven = true
		}
		return true
	})
	if sawChdir {
		return envChdirReason
	}
	if over || sawUnproven {
		return envSplitNestingReason
	}
	return ""
}

// checkChmodTokenScan checks every token position for chmod/chown (H1):
// wrapper prefixes (sudo chmod ...) place the utility after argv[0], so
// an argv[0]-only check misses "sudo chmod -R 777 /". Exact system roots
// deny, exactly like the argv[0] leg; subpaths Ask via
// chmodSubpathAskReason.
func checkChmodTokenScan(argv []string) string {
	for i, tok := range argv {
		if tok == "" {
			continue
		}
		if strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		switch strings.ToLower(resolveBinaryBase(shellPrefixClassToken(stripSubstPrefix(tok)))) {
		case "chmod":
			if hasRecursiveChmod777(argv[i:]) {
				return "recursive world-writable permission on system directory"
			}
		case "chown":
			if hasRecursiveFlag(argv[i:]) && hasRootTarget(argv[i:]) {
				return "recursive ownership change on root filesystem"
			}
		}
	}
	return ""
}

// chdirFlagBinaries are utilities whose own -C/--directory option steers the
// working directory, changing what the command reads and writes without any
// env(1) or git layer. Both permute options past operands (GNU getopt), so
// the scan covers the whole tail to the next "--", not just a global-option
// prefix. gtar/bsdtar and gmake are the same utilities under their GNU/BSD
// names; the option tables are identical for -C/--directory.
var chdirFlagBinaries = map[string]bool{
	"make":   true,
	"gmake":  true,
	"tar":    true,
	"gtar":   true,
	"bsdtar": true,
}

// tarFamilyBases and makeFamilyBases name the utilities whose hidden option
// streams are injected by TAR_OPTIONS / MAKEFLAGS / GNUMAKEFLAGS. The env
// keys carry pre-parsed options that never appear in argv, so the argv-only
// -C and exec-flag scans cannot see them.
var (
	tarFamilyBases  = map[string]bool{"tar": true, "gtar": true, "bsdtar": true}
	makeFamilyBases = map[string]bool{"make": true, "gmake": true}
)

// toolOptionEnvKeys are the environment variables that inject option
// streams or makefiles into make/tar without those options appearing in
// argv. MAKEFILES names makefiles read before the default, so a value under
// it is arbitrary make code.
var toolOptionEnvKeys = []string{
	"TAR_OPTIONS",
	"MAKEFLAGS",
	"GNUMAKEFLAGS",
	"MAKEFILES",
}

// toolOptionEnvKey reports whether key names the canonical tool-option
// variable want. Windows environment names are case-insensitive, so the
// match folds there; on other platforms it stays byte-exact (tar/make only
// read the canonical spelling, so `tar_options=...` is inert and must not
// over-deny).
func toolOptionEnvKey(key, want string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(key, want)
	}
	return key == want
}

// toolOptionEnvKeyName resolves key to its canonical tool-option name, or
// "" when it names none.
func toolOptionEnvKeyName(key string) string {
	for _, want := range toolOptionEnvKeys {
		if toolOptionEnvKey(key, want) {
			return want
		}
	}
	return ""
}

// splitEnvAssignToken splits one VAR=val argv token at the first "=".
// Returns ok=false for anything envAssignRe rejects.
func splitEnvAssignToken(tok string) (key, val string, ok bool) {
	if !envAssignRe.MatchString(tok) {
		return "", "", false
	}
	idx := strings.IndexByte(tok, '=')
	return tok[:idx], tok[idx+1:], true
}

// firstBinIndex returns the index of the first token in seg whose resolved
// basename is in bases, or -1. Used to scope env-assignment scans to the
// prefix that can actually reach the utility: a VAR=val operand after the
// utility is data, not an overlay.
func firstBinIndex(seg []string, bases map[string]bool) int {
	for i, tok := range seg {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		if bases[strings.ToLower(resolveBinaryBase(stripSubstPrefix(tok)))] {
			return i
		}
	}
	return -1
}

// firstToolIndex returns the first make/tar family token index. The two
// families share the TAR_OPTIONS/MAKEFLAGS audit, so one scan point covers
// both.
func firstToolIndex(seg []string) int {
	for i, tok := range seg {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		base := strings.ToLower(resolveBinaryBase(stripSubstPrefix(tok)))
		if tarFamilyBases[base] || makeFamilyBases[base] {
			return i
		}
	}
	return -1
}

// envOptionForms splits one TAR_OPTIONS/MAKEFLAGS value into candidate
// option tokens. The tools parse these values with their own whitespace
// rules, while users quote them for the shell, so scan both the
// whitespace split and the shared shlex split (sandbox.SplitEnvSplitString,
// the same lexer the -S splice uses): a form hidden by either tokenization
// still surfaces, and an unparseable quoted value still contributes its
// whitespace tokens fail-closed.
func envOptionForms(val string) []string {
	forms := strings.Fields(val)
	if toks, ok := sandbox.SplitEnvSplitString(val); ok {
		forms = append(forms, toks...)
	}
	// Keep the unsplit value as a candidate too: an operator default may
	// contain whitespace ("${1:--I id}"), so the word splits above lose
	// the expansion while the shell keeps it one word. candidateForms then
	// walks the substituted word fail-closed.
	forms = append(forms, val)
	return forms
}

// joinSplitParamExpansions re-joins a shell parameter expansion the gate's
// shared shlex split on an inner space: shlex is not brace-aware, so
// `TAR_OPTIONS=${1:--I id} tar -xf a.tar` becomes `TAR_OPTIONS=${1:--I`
// plus `id}`. The shell keeps the expansion one word, so without the join a
// whitespace-bearing default would hide an execution-capable option from
// the option-stream scan. Only an opener with no closer in the same token
// swallows following tokens, and it stops at the first closer.
//
// An opener with no closer anywhere swallows the rest of the stream, which
// is also what a quoted literal `'${'` does ("make '${' -f evil.mk"): the
// join is only the terminated-expansion view. Callers scan the raw argv
// beside it, so a literal opener cannot hide the flags that follow it.
func joinSplitParamExpansions(toks []string) []string {
	out := make([]string, 0, len(toks))
	for i := 0; i < len(toks); i++ {
		cur := toks[i]
		if strings.Contains(cur, "${") && !strings.Contains(cur, "}") {
			for j := i + 1; j < len(toks) && !strings.Contains(cur, "}"); j++ {
				cur += " " + toks[j]
				i = j
			}
		}
		out = append(out, cur)
	}
	return out
}

// expandOptionForms adds the ${}-expanded spelling of each token and the
// substituted word of every parameter-operator default, exactly like the
// argv legs (candidateForms): "TAR_OPTIONS=--to-command=${X}" denies like
// the literal when X is set in the gate's environment, and
// "TAR_OPTIONS=${1:--I id}" denies because the shell substitutes the
// execution-capable -I when 1 is unset. TAR_OPTIONS/MAKEFLAGS therefore
// share the argv candidate set instead of only the ambient env expansion.
func expandOptionForms(forms []string) []string {
	out := make([]string, 0, len(forms))
	for _, form := range forms {
		out = append(out, candidateForms(form)...)
	}
	return out
}

// makeExecLongs are the make(1) long options that run makefiles or
// evaluated make code the gate cannot inspect: -f/--file/--makefile reads
// an arbitrary file (with $(shell ...) recipes), and --eval evaluates an
// arbitrary make expression.
//
// Policy: these are hard Deny, not Ask. A makefile or --eval expression is
// arbitrary code (`$(shell rm -rf /)`) that runs inside whatever sandbox the
// run already has, and under an allow policy an Ask would be auto-approved.
// The gate cannot inspect the file at argv time, so it fails closed exactly
// like TAR_OPTIONS exec flags. Only the directory-steering -C stays Ask.
var makeExecLongs = []string{"file", "makefile", "eval"}

// makeExecFlagName reports whether a make long-option name (without leading
// "--" and any "=value") selects one of makeExecLongs, in full or by
// getopt_long prefix. A prefix of the exec set that make itself rejects as
// ambiguous (e.g. --e, shared with --environment-overrides) denies
// fail-closed like tarExecFlagName: an invalid flag is safe to deny, while
// unrelated valid longs (--environment-overrides) match no prefix and stay
// green.
func makeExecFlagName(name string) bool {
	if name == "" {
		return false
	}
	name = strings.ToLower(name)
	for _, full := range makeExecLongs {
		if name == full || strings.HasPrefix(full, name) {
			return true
		}
	}
	return false
}

// makeShortValueTakers are make's value-taking short options. getopt
// consumes the rest of a cluster as the argument of the first value-taker
// it sees, so an f after one of these is that option's argument
// ("-C/f" is the directory "/f"), not the -f makefile flag.
const makeShortValueTakers = "CIjloOW"

// makeShortExecFlag reports whether a single-dash make token carries the
// value-taking -f makefile flag: exact "-f", attached "-fFILE", or bundled
// ("-nf", "-if"). The cluster is read left to right like getopt so a
// value-taker's argument never masquerades as -f ("-C/f", "-jf").
func makeShortExecFlag(a string) bool {
	if len(a) < 2 || a[0] != '-' || (len(a) > 1 && a[1] == '-') {
		return false
	}
	for k := 1; k < len(a); k++ {
		c := a[k]
		if c == 'f' {
			return true
		}
		if strings.IndexByte(makeShortValueTakers, c) >= 0 {
			return false
		}
	}
	return false
}

// makeOptionExecForm reports whether one make option token selects a
// makefile or evaluated code, for both argv and MAKEFLAGS/GNUMAKEFLAGS
// streams. MAKEFLAGS callers must normalize the first word through
// makeOptionEnvForms first (make accepts it without a leading dash).
func makeOptionExecForm(form string) bool {
	if form == "" || form == "--" {
		return false
	}
	if strings.HasPrefix(form, "--") {
		name := strings.TrimPrefix(form, "--")
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			name = name[:idx]
		}
		return makeExecFlagName(name)
	}
	return makeShortExecFlag(form)
}

// makeOptionEnvForms splits a MAKEFLAGS/GNUMAKEFLAGS value and applies
// make's implicit leading dash to its first word: make reads `MAKEFLAGS=f
// evil.mk` as `-f evil.mk` and `MAKEFLAGS=C /tmp` as `-C /tmp`. Only the
// first word loses its dash; later words are operands (or assignments), so
// prepending to them would invent flags. Assignments ("FOO=bar") and the
// "--" separator stay untouched.
func makeOptionEnvForms(val string) []string {
	forms := envOptionForms(val)
	dashed := make([]string, len(forms))
	for i, form := range forms {
		dashed[i] = form
		if i != 0 || form == "" || form == "--" || form[0] == '-' || envAssignRe.MatchString(form) {
			continue
		}
		dashed[i] = "-" + form
	}
	return expandOptionForms(dashed)
}

// tarOptionAskForm reports whether one TAR_OPTIONS token steers the
// extraction directory (-C/--directory) or names an archive file the argv
// does not show (-f/--file). Both hide state from the path audit; the
// exec-capable flags deny earlier in the hard-deny leg.
func tarOptionAskForm(form string) string {
	if form == "--" {
		return ""
	}
	if strings.HasPrefix(form, "--") {
		name := strings.TrimPrefix(form, "--")
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			name = name[:idx]
		}
		if len(name) >= 3 && strings.HasPrefix("directory", name) {
			return "TAR_OPTIONS -C directory override requires user approval"
		}
		if len(name) >= 3 && strings.HasPrefix("file", name) {
			return "TAR_OPTIONS -f archive override requires user approval"
		}
		return ""
	}
	if len(form) > 1 && form[0] == '-' {
		if strings.ContainsRune(form[1:], 'C') {
			return "TAR_OPTIONS -C directory override requires user approval"
		}
		// tar bundles getopt shorts freely ("-xvf", "-tf", "-cvf"): f is
		// the value-taking archive flag wherever it sits in the cluster,
		// so any f in the bundle names a hidden archive. This is a
		// deliberate over-approximation: a value-taker before f may make
		// the f part of its argument ("-Cf"), and an f inside an attached
		// value ("-Tfile") is not the archive flag; asking is safe.
		if strings.ContainsRune(form[1:], 'f') {
			return "TAR_OPTIONS -f archive override requires user approval"
		}
	}
	return ""
}

// makeOptionAskForm reports whether one MAKEFLAGS/GNUMAKEFLAGS token steers
// the make directory (-C/--directory), exactly like a make -C override.
// Flags and variable assignments are otherwise left alone; only directory
// steering needs approval. key names the variable in the reason.
func makeOptionAskForm(key, form string) string {
	if form == "--" {
		return ""
	}
	if strings.HasPrefix(form, "--") {
		name := strings.TrimPrefix(form, "--")
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			name = name[:idx]
		}
		if len(name) >= 3 && strings.HasPrefix("directory", name) {
			return fmt.Sprintf("%s -C directory override requires user approval", key)
		}
		return ""
	}
	if len(form) > 1 && form[0] == '-' && (form == "-C" || strings.ContainsRune(form[1:], 'C')) {
		return fmt.Sprintf("%s -C directory override requires user approval", key)
	}
	return ""
}

// toolOptionEnvHardDeny denies execution-capable option streams injected
// through make/tar environment variables. The assignments are argv-visible
// but the option text is not a normal token, so the argv scans never see
// it: `env TAR_OPTIONS='--to-command=id' tar -xf a.tar` would otherwise
// Allow and run id per archived file, and `env MAKEFLAGS='-f /tmp/evil.mk'
// make` would run an uninspected makefile. MAKEFILES names makefiles
// outright. Only a tar-/make-family token in the same segment makes the
// assignment live; without one the variable is inert (or an operand) and
// stays green.
//
// The raw argv is scanned beside the joined view so a literal unterminated
// `${` cannot swallow the rest of the line and hide the assignment or the
// utility behind it ("env X='${' TAR_OPTIONS='-I evil.sh' tar -xf a.tar").
// The joined view still catches an expansion the shared shlex split on an
// inner space.
func toolOptionEnvHardDeny(argv []string) string {
	for _, view := range [][]string{argv, joinSplitParamExpansions(argv)} {
		if reason := toolOptionEnvHardDenyView(view); reason != "" {
			return reason
		}
	}
	return ""
}

// toolOptionEnvHardDenyView is toolOptionEnvHardDeny for one already-joined
// token view.
func toolOptionEnvHardDenyView(argv []string) string {
	if tarIdx := firstBinIndex(argv, tarFamilyBases); tarIdx >= 0 {
		for _, tok := range argv[:tarIdx] {
			key, val, ok := splitEnvAssignToken(tok)
			if !ok || !toolOptionEnvKey(key, "TAR_OPTIONS") {
				continue
			}
			for _, form := range expandOptionForms(envOptionForms(val)) {
				if tarTokenExecFlag(form) {
					return "TAR_OPTIONS injects execution-capable tar flags"
				}
			}
		}
	}
	if makeIdx := firstBinIndex(argv, makeFamilyBases); makeIdx >= 0 {
		for _, tok := range argv[:makeIdx] {
			key, val, ok := splitEnvAssignToken(tok)
			if !ok {
				continue
			}
			switch toolOptionEnvKeyName(key) {
			case "MAKEFILES":
				// An empty value names no extra makefile; only a
				// non-empty list is uninspectable code.
				if strings.TrimSpace(val) == "" {
					continue
				}
				return "MAKEFILES injects makefiles the gate cannot inspect"
			case "MAKEFLAGS", "GNUMAKEFLAGS":
				for _, form := range makeOptionEnvForms(val) {
					if makeOptionExecForm(form) {
						return "MAKEFLAGS injects execution-capable make flags"
					}
				}
			}
		}
	}
	return ""
}

// checkMakeTokenScan checks every token position for a make-family utility
// (H1-style token scan): wrapper prefixes (sudo make ...), pipes, and
// assignments place make after argv[0], so an argv[0]-only check misses
// "sudo make -f /tmp/evil.mk". A -f/--file/--makefile/--eval tail reads
// makefiles or evaluated code the gate cannot inspect, so it denies in full
// (bare `make -f` allowed before this leg). Quoted payloads stay one token
// so echo prose stays green.
//
// Like toolOptionEnvHardDeny, it scans the raw argv and the joined view: a
// literal unterminated `${` ("make '${' -f evil.mk") must not swallow the
// -f tail, while a terminated expansion the shared shlex split on an inner
// space ("MAKEFLAGS=${1:--f evil.mk} make") still re-joins.
func checkMakeTokenScan(argv []string) string {
	for _, view := range [][]string{argv, joinSplitParamExpansions(argv)} {
		if reason := checkMakeTokenScanView(view); reason != "" {
			return reason
		}
	}
	return ""
}

// checkMakeTokenScanView is checkMakeTokenScan for one already-joined token
// view.
func checkMakeTokenScanView(argv []string) string {
	for i, tok := range argv {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		if !makeFamilyBases[strings.ToLower(resolveBinaryBase(stripSubstPrefix(tok)))] {
			continue
		}
		if reason := makeTokenTailDeny(argv[i+1:]); reason != "" {
			return reason
		}
	}
	return ""
}

// makeTokenTailDeny scans the option tail after a make utility for a
// makefile/eval flag or an option-injection assignment. It stops at the
// shell's "--" end-of-options separator and resolves each option token over
// the shared candidateForms set, so an unquoted parameter-operator default
// ("make ${1:---eval=id}") denies exactly like the literal spelling when
// the referenced parameter is unset. Assignments stay on the raw token:
// splitEnvAssignToken is about the literal KEY=value shape make itself
// parses, and an unresolvable ${...} there is covered by the Makefiles /
// MAKEFLAGS value checks below (fail-closed).
func makeTokenTailDeny(tail []string) string {
	for _, a := range tail {
		if a == "--" {
			break
		}
		// Command-line variable assignments reach the same parsers:
		// `make MAKEFILES=evil.mk` names an uninspected makefile and
		// `make MAKEFLAGS=-f...` passes exec flags to sub-makes.
		if key, val, ok := splitEnvAssignToken(a); ok {
			switch toolOptionEnvKeyName(key) {
			case "MAKEFILES":
				if strings.TrimSpace(val) != "" {
					return "MAKEFILES names makefiles the gate cannot inspect"
				}
			case "MAKEFLAGS", "GNUMAKEFLAGS":
				for _, form := range makeOptionEnvForms(val) {
					if makeOptionExecForm(form) {
						return "MAKEFLAGS injects execution-capable make flags"
					}
				}
			}
			continue
		}
		for _, form := range candidateForms(a) {
			if makeOptionExecForm(form) {
				return "make -f/--file/--eval runs uninspected makefiles (hard deny)"
			}
		}
	}
	return ""
}

// makeWindowHasExecFlag scans one make command window for the makefile /
// eval flags and MAKEFILES/MAKEFLAGS assignments, over the shared
// candidateForms set (raw, ${}-expanded, and parameter-operator defaults).
// The window starts at the make word (fields[0]), so the walk skips it and
// applies the same tail rule as the argv leg. Quoting was stripped by the
// caller, so quoted text never reaches here.
func makeWindowHasExecFlag(window string) bool {
	fields := strings.Fields(window)
	if len(fields) < 2 {
		return false
	}
	return makeTokenTailDeny(fields[1:]) != ""
}

// toolOptionEnvAskReason returns a non-empty Ask reason when an argv
// assignment injects a directory (or tar archive) override through
// TAR_OPTIONS/MAKEFLAGS/GNUMAKEFLAGS ahead of a make/tar utility. Like a
// make/tar -C or a git -C, the gate cannot see the files the steered run
// reads. `${}`-expanded forms are scanned too.
func toolOptionEnvAskReason(seg []string) string {
	// A -S/--split-string payload is a single argv token, so the
	// assignment it carries never reaches splitEnvAssignToken:
	// `env -S 'TAR_OPTIONS=-C /hook tar ...'`. Expand once (the
	// fixpoint leaves no residual -S on success) and rescan.
	for i, tok := range seg {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		if gitScanBase(tok) != "env" || !containsEnvSplitString(seg[i+1:]) {
			continue
		}
		if expanded, bad := expandEnvSplitString(seg[i+1:]); !bad && !containsEnvSplitString(expanded) {
			if reason := toolOptionEnvAskReason(expanded); reason != "" {
				return reason
			}
		}
		break
	}
	toolIdx := firstToolIndex(seg)
	if toolIdx < 0 {
		return ""
	}
	for _, tok := range seg[:toolIdx] {
		key, val, ok := splitEnvAssignToken(tok)
		if !ok {
			continue
		}
		switch toolOptionEnvKeyName(key) {
		case "TAR_OPTIONS":
			for _, form := range expandOptionForms(envOptionForms(val)) {
				if reason := tarOptionAskForm(form); reason != "" {
					return reason
				}
			}
			// A value the gate could not expand is still parsed by
			// tar as pre-options, so an unresolvable ${...} can hide
			// a -C/directory or archive steer ("TAR_OPTIONS=${X}").
			if hasParamExpansion(val) {
				return "TAR_OPTIONS with parameter expansion requires user approval"
			}
		case "MAKEFLAGS", "GNUMAKEFLAGS":
			for _, form := range makeOptionEnvForms(val) {
				if reason := makeOptionAskForm(key, form); reason != "" {
					return reason
				}
			}
			if hasParamExpansion(val) {
				return fmt.Sprintf("%s with parameter expansion requires user approval", key)
			}
		}
	}
	return ""
}

// chdirShortFlagCluster reports whether a dash-prefixed tar/make short token
// carries the -C directory flag, including attached values and bundles
// ("-Cdir", "-xCf"). A leading "--" long option is never a short cluster.
func chdirShortFlagCluster(a string) bool {
	return a == "-C" || (len(a) > 2 && a[0] == '-' && a[1] != '-' && strings.ContainsRune(a[1:], 'C'))
}

// utilityChdirAskReason returns a non-empty Ask reason when a make/tar tail
// carries -C/--directory: the utility runs in the named directory, exactly
// like a git -C override, and the gate cannot see the repository, makefile,
// or archive paths the steered run reads. Attached and clustered short
// forms ("-Cdir", "-xCf") and unique long prefixes ("--dir") are covered.
// GNU tar's old option style ("tar cC /tmp") is the same cluster without a
// leading dash, but only in the first word after the binary. A literal "--"
// ends option parsing, so operands named "-C" stay green.
func utilityChdirAskReason(seg []string) string {
	for i, tok := range seg {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		base := strings.ToLower(resolveBinaryBase(stripSubstPrefix(tok)))
		if !chdirFlagBinaries[base] {
			continue
		}
		if tarFamilyBases[base] && i+1 < len(seg) {
			// Old option style: a dashless first word is a flag
			// cluster ("tar cC /tmp" is "tar -cC /tmp").
			if old := seg[i+1]; old != "" && old[0] != '-' && chdirShortFlagCluster("-"+old) {
				return fmt.Sprintf("%s -C directory override requires user approval", base)
			}
		}
		for _, a := range seg[i+1:] {
			if a == "--" {
				break
			}
			if chdirShortFlagCluster(a) {
				return fmt.Sprintf("%s -C directory override requires user approval", base)
			}
			if strings.HasPrefix(a, "--") {
				name := strings.TrimPrefix(a, "--")
				if idx := strings.IndexByte(name, '='); idx >= 0 {
					name = name[:idx]
				}
				if len(name) >= 3 && strings.HasPrefix("directory", name) {
					return fmt.Sprintf("%s --directory override requires user approval", base)
				}
			}
		}
	}
	return ""
}

// chmodAskRoots are the system roots whose recursive-777 subpaths Ask
// (exact roots already Deny): "chmod -R 777 /etc/ssh" must never Allow.
var chmodAskRoots = []string{"/etc", "/usr", "/boot", "/var", "/home", "/root"}

// chmodSubpathAskReason returns a non-empty Ask reason when a chmod tail
// recursively sets 777/0777 on a subpath of a system root. Exact roots
// deny in the hard-deny leg; this leg only fires below them.
func chmodSubpathAskReason(argv []string) string {
	for i, tok := range argv {
		if tok == "" {
			continue
		}
		if strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		if strings.ToLower(resolveBinaryBase(stripSubstPrefix(tok))) != "chmod" {
			continue
		}
		tail := argv[i:]
		if !hasRecursiveFlag(tail) {
			continue
		}
		foundMode := false
		for _, a := range tail[1:] {
			if chmodModeIsWorldWritable(a) {
				foundMode = true
				break
			}
		}
		if !foundMode {
			continue
		}
		for _, a := range tail[1:] {
			if strings.HasPrefix(a, "-") || envAssignRe.MatchString(a) {
				continue
			}
			c := canonicalGatePath(strings.Trim(a, "()"))
			for _, root := range chmodAskRoots {
				if strings.HasPrefix(c, root+"/") {
					return fmt.Sprintf("recursive world-writable permission under %s requires user approval", root)
				}
			}
		}
	}
	return ""
}

// codeToolBases are binaries whose inline program text can execute shell
// commands: awk system() and sed e/s///e. Membership drives the
// permission-mode-independent opaque-code Ask escalation, exactly like
// codeInterpreters: under {"system_exec": "allow"} these programs must
// still ask, never run silently.
var codeToolBases = map[string]bool{
	"awk": true, "gawk": true, "mawk": true,
	"sed": true, "gsed": true,
}

// codeToolTails returns every awk/sed-led tail of seg that sits in command
// position (H1/F1): wrapper prefixes (sudo awk ...), find -exec, and env
// trampolines place the tool after seg[0]. Position 0 is always command
// position; deeper positions need wrapper-prefix reachability
// (interpreterTailReachable, shared with interpreters), so a plain
// argument ("echo awk hello") stays green. Quoted payloads stay one token
// so echo green is preserved.
//
// The env leg carries the same bounded every-position scan as
// interpreterTails: coveredUntil skips spans an earlier unwrap already
// covered, a settled plain utility allows only maxSettledEnvScans trailing
// env rescan (make/tar permute operands, so a trailing env can stay live),
// and the work budget stops adversarial never-settling chains. Without the
// bounds each env position re-expanded the whole suffix, which is cubic on
// a bare `env env ...` chain.
func codeToolTails(seg []string) [][]string {
	var out [][]string
	collect := func(argv []string) {
		for i, tok := range argv {
			if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
				continue
			}
			if !codeToolBases[strings.ToLower(resolveBinaryBase(shellPrefixClassToken(tok)))] {
				continue
			}
			if i > 0 && !interpreterTailReachable(argv, i) {
				continue
			}
			out = append(out, argv[i:])
		}
	}
	collect(seg)
	forEachEnvPosition(seg, true, true, func(_ int, eff []string) bool {
		if len(eff) > 0 {
			collect(eff)
		}
		return true
	})
	return out
}

// awkSystemRe matches an awk system() call (the command-execution
// primitive) in program text.
var awkSystemRe = regexp.MustCompile(`system\s*\(`)

// sedExecERe matches the sed "e" command (an optional address or
// command separator, then a bare e): "e", "1e cmd", "s/a/b/;e".
var sedExecERe = regexp.MustCompile(`(^|[;{}\s\d$/,])e(\s|;|$)`)

// sedSubstERe matches the sed s///e flag (up to 3 flag letters, then a
// segment boundary so "s/a/escape/" stays green): "s/a/b/e",
// "s/a/b/ge".
var sedSubstERe = regexp.MustCompile(`s/[^/\n]*/[^/\n]*/[a-zA-Z]{0,3}e[a-zA-Z]{0,3}(\s|;|$|/)`)

// sedProgramExec reports whether one sed program text (an -e operand or a
// free program argument) can execute shell commands.
func sedProgramExec(prog string) bool {
	if sedExecERe.MatchString(prog) {
		return true
	}
	if sedSubstERe.MatchString(prog) {
		return true
	}
	return false
}

// codeToolAskReason runs the per-tail opaque-code check for awk/sed
// (Ask, treating their programs as interpreter code): awk program text
// containing system() and sed program text with e/s///e ask, even under
// allow. Flags and their values (-e EXPR, -f FILE, -n, -E) are skipped so
// "sed -e 's/a/b/'" and "sed -n 's/a/b/p'" stay out; only program text
// is evaluated. Returns "" when clean.
func codeToolAskReason(tail []string) string {
	if len(tail) == 0 {
		return ""
	}
	base := strings.ToLower(resolveBinaryBase(shellPrefixClassToken(tail[0])))
	switch {
	case base == "awk" || base == "gawk" || base == "mawk":
		for _, a := range tail[1:] {
			// ${}-expanded too: system(${CMD}) asks like the literal.
			if awkSystemRe.MatchString(a) || awkSystemRe.MatchString(os.ExpandEnv(a)) {
				return "awk program calls system() and requires user approval"
			}
		}
	case base == "sed" || base == "gsed":
		for j := 1; j < len(tail); j++ {
			a := tail[j]
			if a == "-e" || a == "--expression" {
				if j+1 < len(tail) && sedProgramExec(os.ExpandEnv(tail[j+1])) {
					return "sed program executes shell commands and requires user approval"
				}
				j++
				continue
			}
			if strings.HasPrefix(a, "--expression=") {
				if sedProgramExec(os.ExpandEnv(strings.TrimPrefix(a, "--expression="))) {
					return "sed program executes shell commands and requires user approval"
				}
				continue
			}
			if a == "-f" || a == "--file" {
				// A -f script file operand: skip the filename here
				// (file content is out of scope for this leg).
				j++
				continue
			}
			if strings.HasPrefix(a, "--file=") {
				continue
			}
			if strings.HasPrefix(a, "-") {
				// Bundled single-dash cluster: a value-taking e
				// takes the remainder ("-ne1e" -> program "1e") or
				// the next token ("-ne" -> next token).
				if len(a) > 1 && a[1] != '-' {
					if idx := strings.IndexByte(a[1:], 'e'); idx >= 0 {
						if rest := a[idx+2:]; rest != "" {
							if sedProgramExec(os.ExpandEnv(rest)) {
								return "sed program executes shell commands and requires user approval"
							}
						} else if j+1 < len(tail) && sedProgramExec(os.ExpandEnv(tail[j+1])) {
							return "sed program executes shell commands and requires user approval"
						}
					}
					// A trailing f takes the next token as the
					// script file; skip it below via the f rule.
					if last := a[len(a)-1]; last == 'f' {
						j++
					}
				}
				continue
			}
			if sedProgramExec(os.ExpandEnv(a)) {
				return "sed program executes shell commands and requires user approval"
			}
		}
	}
	return ""
}

// paramAnyBinaries are utilities whose bare (non-flag) operands with
// parameter expansion fail closed to Ask: the expansion can smuggle a
// root or device target ("rm -rf ${x}" with x=/ must never Allow).
// chmod/chown ride here too: "chmod -R 777 ${x}" must never Allow low.
var paramAnyBinaries = map[string]bool{
	"rm": true, "dd": true, "shred": true, "fdisk": true, "parted": true,
	"mkfs": true, "mkfs.ext2": true, "mkfs.ext3": true, "mkfs.ext4": true,
	"mkfs.xfs": true, "mkfs.btrfs": true, "mkfs.vfat": true,
	"mkfs.fat": true, "mkfs.ntfs": true, "mkfs.exfat": true,
	"chmod": true, "chown": true,
}

// paramExpansionRe matches shell parameter expansions in all spellings:
// bare $NAME, positional/special parameters ($1, $@, $*, $#, $?, $$, $!,
// $0-$9), and braced ${...} (including ${NAME:-default} operators). The
// bare special forms matter because they expand just like a named
// parameter ("rm -rf $1" with $1=/ deletes root) and must fail closed.
// A lone "$" (not followed by a name, special parameter, or brace) stays
// out; "$-"/"$0" expand to shell flags/name but are still treated as
// unresolvable carriers for uniformity.
var paramExpansionRe = regexp.MustCompile(`\$(?:[A-Za-z_][A-Za-z0-9_]*|[0-9]+|[#?*$@!-]|\{[^}]*\})`)

// hasParamExpansion reports whether a token carries a $NAME/${...} or
// positional/special parameter expansion in any spelling.
func hasParamExpansion(t string) bool {
	return paramExpansionRe.MatchString(t)
}

// paramOperatorDefaultRe captures the substituted word of a shell parameter
// expansion operator: ${NAME:-word}, ${NAME-word}, ${NAME:=word},
// ${NAME=word}, ${NAME:+word}, ${NAME+word}, and the same operators on a
// positional/special parameter (${1:-word}, ${@-word}). Prefix/suffix
// trimming (${NAME#pat}, ${NAME%pat}) yields no whole command word, so it
// stays out; substitution (${NAME/pat/rep}) is left to the unresolved-
// expansion Ask.
var paramOperatorDefaultRe = regexp.MustCompile(`\$\{(?:[A-Za-z_][A-Za-z0-9_]*|[0-9]+|[@*#?$!-]+):?[-=+]([^}]*)\}`)

// paramDefaultWords returns the words a shell may substitute for a
// ${NAME<op>word} expansion. The gate cannot know NAME's value (the caller's
// shell and the server process have different environments), so every
// candidate is reported and callers fail closed on any dangerous one. A
// nested default keeps its inner text, so the raw spelling is walked too.
func paramDefaultWords(s string) []string {
	matches := paramOperatorDefaultRe.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if w := strings.TrimSpace(m[1]); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// paramExpansionAskReason returns a non-empty Ask reason when a $NAME /
// ${...} parameter expansion sits in an execution-capable flag or a
// destructive target of a sensitive utility: tar/sort/git flags ("sort --c${_H}ompress"),
// dd of=${DEV}, or any rm operand ("rm -rf ${x}"). Known values already
// deny via ${}-expansion in the hard-deny legs above; this leg catches
// the unresolvable remainder so it degrades to Ask, never Allow low.
// For a tar/make-family utility in command position the whole tail fails
// closed ("tar -xf a.tar ${X}", "make $1"), because the expansion can name
// an option, target, or steering assignment.
// Plain quoted text and unrelated binaries ("echo ${x}") stay green: the
// expansion must sit after a sensitive binary in the same segment.
func paramExpansionAskReason(seg []string) string {
	binIdx := -1
	var bin string
	for i, tok := range seg {
		if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
			continue
		}
		base := strings.ToLower(resolveBinaryBase(stripSubstPrefix(tok)))
		if tarFamilyBases[base] || makeFamilyBases[base] ||
			base == "sort" || base == "git" ||
			base == "chmod" || base == "chown" || paramAnyBinaries[base] {
			binIdx = i
			bin = base
			break
		}
	}
	if binIdx < 0 {
		return ""
	}
	// tar/make-family tails are opaque carriers: an unresolvable expansion
	// anywhere after the utility can be an option (GNU tar's dashless
	// first word "-cI", make's -f/--eval), a target, or a variable
	// assignment that steers the run ("tar -xf a.tar ${X}", "make $1").
	// When the utility word is in command position the whole tail fails
	// closed to Ask; a prose mention ("echo tar ${X}") stays green.
	commandPosition := commandPositionFor(seg, binIdx)
	for _, t := range seg[binIdx+1:] {
		if !hasParamExpansion(t) {
			continue
		}
		if commandPosition && (tarFamilyBases[bin] || makeFamilyBases[bin]) {
			return fmt.Sprintf("%s argument with parameter expansion requires user approval", bin)
		}
		if strings.HasPrefix(t, "-") || strings.Contains(t, "=") {
			return fmt.Sprintf("%s flag with parameter expansion requires user approval", bin)
		}
		if paramAnyBinaries[bin] {
			return fmt.Sprintf("%s target with parameter expansion requires user approval", bin)
		}
	}
	return ""
}

// ---- Indirection Hardening (Phase 5) ---- //

// resolveBinaryBase resolves the real basename of a binary, following
// symlinks to defeat rename-type bypasses. Only resolves path-form names
// (containing "/"); bare names use filepath.Base directly since PATH
// symlink resolution (awk->gawk, reboot->systemctl) would incorrectly
// change risk-table lookups. Best-effort: falls back to filepath.Base(name)
// on any error so a legit command is never blocked because resolution
// failed.
//
// Provider-prefix unwrapping: since Ubuntu 25.10 the default coreutils
// provider is uutils (rust-coreutils) with GNU coreutils installed under a
// "gnu" prefix (gnurm, gnucp, gnuls, ...), and the coreutils-from-*
// packages point the canonical paths at the active provider binary. On
// such systems /usr/bin/rm is a symlink whose EvalSymlinks target is
// "gnurm". When (and only when) the leaf itself traversed a symlink, a
// leading "gnu" provider prefix is unwrapped so the gate classifies by the
// invoked utility identity (gnurm -> rm). Directory symlinks do NOT
// qualify: a real gnu* file reached through a symlinked parent
// (/tmp/link/gnuls, a symlinked TMPDIR, /bin -> /usr/bin) keeps its leaf
// name, so it stays fail-closed unknown. A real file literally named gnu*
// is likewise left alone (fail-closed: unknown, never downgraded to a
// lower risk).
func resolveBinaryBase(name string) string {
	if !strings.Contains(name, "/") {
		return filepath.Base(name)
	}
	// Path form: resolve symlinks directly.
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return filepath.Base(name)
	}
	base := filepath.Base(resolved)
	invoked := filepath.Base(filepath.Clean(name))
	// Same leaf name means no leaf symlink was traversed (any resolved
	// vs input difference is only a symlinked parent directory): never
	// unwrap, so a real gnu* file stays fail-closed unknown.
	if base == invoked {
		return base
	}
	// Leaf symlink traversed: unwrap only when it recovers the invoked
	// utility identity. Anything else (mytool -> gnugo, mytool ->
	// gnuplot) keeps the provider binary name and stays fail-closed
	// unknown instead of aliasing an unrelated tool or inventing an
	// identity.
	if unwrapped := stripProviderPrefix(base); strings.EqualFold(unwrapped, invoked) {
		return unwrapped
	}
	return base
}

// stripProviderPrefix unwraps a distribution provider prefix ("gnu" on
// Ubuntu 25.10+, where GNU coreutils ship as gnurm/gnucp/gnuls/...).
// The remainder test is only risk-table membership (case-insensitive,
// via classifyRisk): "gnugo" DOES strip to "go" here because go is a
// known risk-table name, while unlisted remainders ("gnuplot" -> "plot")
// are returned unchanged. Aliasing is prevented by the caller:
// resolveBinaryBase only adopts the unwrapped name when it EqualFolds
// the invoked leaf, so gnugo stays gnugo and fail-closed unknown. The
// bare remainder never touches the filesystem: resolveBinaryBase
// short-circuits slash-free names, so this check cannot recurse into
// EvalSymlinks.
func stripProviderPrefix(base string) string {
	if len(base) <= len("gnu") || !strings.EqualFold(base[:len("gnu")], "gnu") {
		return base
	}
	remainder := base[len("gnu"):]
	if classifyRisk([]string{remainder}) == RiskUnknown {
		return base
	}
	return remainder
}

// envAssignRe matches VAR=val assignments. Utility names never contain
// "=", so anything matching before the utility position is an environment
// assignment, not the command.
var envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// envShortSplitPos returns the index of -S in a short cluster when S is
// the first value-taking member (scanned left to right). A -C/-u/-a
// before S takes the remainder as its own value (e.g. -CS means -C with
// value "S"), so it is not a split-string. Returns -1 when no
// split-string S is in option position.
func envShortSplitPos(a string) int {
	for k := 1; k < len(a); k++ {
		switch a[k] {
		case 'C', 'u', 'a':
			return -1
		case 'S':
			return k
		}
	}
	return -1
}

// envLongOptions is the known env(1) long-option set used for unique-prefix
// resolution (getopt_long abbreviations: --s -> --split-string,
// --ch -> --chdir, --u -> --unset). The sandbox leaf owns the canonical
// list, so the gate and the pre-gate splice cannot drift; the accessor
// returns an independent copy.
var envLongOptions = sandbox.EnvLongOptionNames()

// envLongKind classifies a resolved env long option.
type envLongKind int

const (
	envLongUnknown envLongKind = iota
	envLongSplitString
	envLongTakesValue
	envLongBoolean
)

// resolveEnvLong resolves an abbreviated env long name (without leading
// "--" and without any "=value") by unique prefix (getopt_long
// semantics). An exact match wins; a proper prefix matching exactly one
// known option resolves to it; zero or multiple matches are ambiguous ->
// unknown (fail-closed: consume nothing, splice nothing, sentinel).
func resolveEnvLong(name string) envLongKind {
	var match string
	count := 0
	for _, full := range envLongOptions {
		if full == name {
			match = full
			count = 1
			break
		}
		if strings.HasPrefix(full, name) {
			match = full
			count++
		}
	}
	if count != 1 {
		return envLongUnknown
	}
	switch match {
	case "split-string":
		return envLongSplitString
	case "unset", "chdir", "argv0":
		return envLongTakesValue
	default:
		return envLongBoolean
	}
}

// envSplitUnknown is the fail-closed sentinel for an unparseable
// -S/--split-string value (H2): an unknown binary that classifies
// RiskUnknown (ask) and never hard-denies.
const envSplitUnknown = "__env_split_unknown__"

// expandEnvSplitString splices -S/--split-string values into the env option
// stream (H2): the value IS a command line, so dropping it would allow
// "env -S 'rm -rf /'". Values are shlex-split and injected where the flag
// stood; unparseable or empty values fail closed to the unknown sentinel.
// Long flags resolve by unique getopt_long prefix (--s/--sp/--spl... all
// splice like --split-string, with =val or a separate value). Short -S
// resolves at any cluster position: a trailing S takes the next token
// ("-iS 'rm -rf /'", "-viS ..."), a mid-cluster S takes the attached
// remainder as its value ("-iSrm -rf /").
//
// M-3: nested split-strings (`env -S 'env -S "rm -rf /"'`) re-enter the
// splice, so expansion runs to a fixpoint (capped at 32 passes so
// adversarial nesting cannot blow up the scan; the payload shrinks each
// pass, so depth-9 reviewer PoCs still expand fully and hard-deny).
// A residual -S after the budget, or any unparseable value, fails closed
// to the unknown sentinel (bad=true): callers Ask, never Allow.
//
// Cross-layer contract: the gate splice is an over-approximation kept
// inline in the option stream (the env layers are preserved for the
// per-position token scans). It splices -S at any position before "--",
// while the sandbox spliceEnvLayer stops at the utility position and
// unwraps+collects (utility plus chdir operands). The divergence after
// the utility is fail-closed over-approximation, pinned by
// TestExpandSpliceOverApprox; keep the option-position table in sync
// with the sandbox mirror (TestSpliceEnvLayerTable) by construction.
//
// When the stream has no -S/--split-string before "--", expansion is the
// identity and the input slice is returned as-is (no copy); every caller
// treats the stream as read-only.
func expandEnvSplitString(rest []string) ([]string, bool) {
	if !containsEnvSplitString(rest) {
		// No -S/--split-string before "--": expansion is the identity.
		// Return the input stream instead of copying it, so the
		// every-position unwrap (resolveEnvTarget) does not allocate a
		// fresh suffix per env layer. Callers treat the result as
		// read-only.
		return rest, false
	}
	expanded, bad := expandEnvSplitOnce(rest)
	if bad {
		return expanded, true
	}
	for d := 0; d < 31; d++ {
		if !containsEnvSplitString(expanded) {
			break
		}
		next, bad := expandEnvSplitOnce(expanded)
		if bad {
			return next, true
		}
		if tokensEqual(expanded, next) {
			break
		}
		expanded = next
	}
	if containsEnvSplitString(expanded) {
		return []string{envSplitUnknown}, true
	}
	return expanded, false
}

// containsEnvSplitString reports whether the stream still carries an
// unexpanded -S/--split-string flag anywhere before a "--" stop.
// The gate splice is deliberately any-position (fail-closed
// over-approximation: a -S behind a nested env utility still re-enters),
// unlike the sandbox splice which stops at the utility. See the
// expandEnvSplitString contract and TestExpandSpliceOverApprox.
func containsEnvSplitString(rest []string) bool {
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "--" {
			return false
		}
		if strings.HasPrefix(a, "--") {
			name := a[2:]
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				name = name[:idx]
			}
			if resolveEnvLong(name) == envLongSplitString {
				return true
			}
			continue
		}
		if len(a) > 1 && a[0] == '-' && envShortSplitPos(a) >= 0 {
			return true
		}
	}
	return false
}

// tokensEqual reports whether two token streams are identical.
func tokensEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func expandEnvSplitOnce(rest []string) ([]string, bool) {
	expanded := make([]string, 0, len(rest))
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "--" {
			expanded = append(expanded, a)
			expanded = append(expanded, rest[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "--") {
			name := strings.TrimPrefix(a, "--")
			val := ""
			hasVal := false
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				val = name[idx+1:]
				name = name[:idx]
				hasVal = true
			}
			if resolveEnvLong(name) == envLongSplitString {
				if !hasVal {
					if i+1 >= len(rest) {
						return []string{envSplitUnknown}, true
					}
					val = rest[i+1]
					i++
				}
				toks, ok := sandbox.SplitEnvSplitString(val)
				if !ok {
					return []string{envSplitUnknown}, true
				}
				expanded = append(expanded, toks...)
				continue
			}
			expanded = append(expanded, a)
			continue
		}
		if len(a) > 1 && a[0] == '-' {
			if k := envShortSplitPos(a); k >= 0 {
				var val string
				if k+1 < len(a) {
					// Mid-cluster or attached: the remainder is
					// the value ("-SVALUE", "-iSVALUE").
					val = a[k+1:]
				} else {
					// Trailing S: the value is the next token
					// ("-S VALUE", "-iS VALUE").
					if i+1 >= len(rest) {
						return []string{envSplitUnknown}, true
					}
					val = rest[i+1]
					i++
				}
				toks, ok := sandbox.SplitEnvSplitString(val)
				if !ok {
					return []string{envSplitUnknown}, true
				}
				if prefix := a[1:k]; prefix != "" {
					expanded = append(expanded, "-"+prefix)
				}
				expanded = append(expanded, toks...)
				continue
			}
			expanded = append(expanded, a)
			continue
		}
		expanded = append(expanded, a)
	}
	return expanded, false
}

// unwrapEnvTrampoline resolves one leading env(1) layer (F1):
// env [OPTION]... [VAR=val]... [--] [UTILITY [ARGS]...].
// Returns (effective, true) when argv[0] resolves to env (effective is nil
// for bare env with no utility); (nil, false) when argv[0] is not env.
// Short-flag clusters evaluate left to right like getopt (mirroring
// spliceEnvLayer): the first value-taking member (-u/-C/-a) takes the
// attached remainder as its value ("-uFOO", "-Cdir") or consumes the
// next argument when trailing ("-iu", exact "-u"). -S/--split-string is
// spliced, never dropped (H2); long options resolve by unique getopt_long
// prefix, so --s/--sp/... splice exactly like --split-string. Unknown or
// ambiguous long options fail closed to the unknown sentinel (never
// silently skipped as booleans). Ambiguous shapes resolve toward treating
// the next token as the utility, which fails closed to unknown/ask rather
// than allowing.
func unwrapEnvTrampoline(argv []string) ([]string, bool) {
	if len(argv) == 0 {
		return nil, false
	}
	if strings.ToLower(resolveBinaryBase(stripLeadBackslashes(argv[0]))) != "env" {
		return nil, false
	}
	rest := argv[1:]
	expanded, bad := expandEnvSplitString(rest)
	if bad {
		return expanded, true
	}
	rest = expanded
	i := 0
	for i < len(rest) {
		a := rest[i]
		if a == "--" {
			i++
			break
		}
		if a == "-" {
			break
		}
		if strings.HasPrefix(a, "--") {
			name := strings.TrimPrefix(a, "--")
			hasVal := false
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				name = name[:idx]
				hasVal = true
			}
			switch resolveEnvLong(name) {
			case envLongSplitString:
				// Should have been expanded above; fail closed.
				return []string{envSplitUnknown}, true
			case envLongTakesValue:
				if hasVal {
					i++
				} else {
					// Trailing value-taking long without its operand
					// (e.g. `env --chdir` with no directory):
					// unparseable, fail closed to the unknown
					// sentinel instead of treating env as bare
					// (bare env classifies low and would Allow).
					if i+1 >= len(rest) {
						return []string{envSplitUnknown}, true
					}
					i += 2
				}
				continue
			case envLongBoolean:
				i++
				continue
			default:
				// Unknown or ambiguous long: fail closed, never
				// silently skip (an unparsed option could hide the
				// real utility position).
				return []string{envSplitUnknown}, true
			}
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			if envShortSplitPos(a) >= 0 {
				// Should have been expanded above; fail closed.
				return []string{envSplitUnknown}, true
			}
			// Short cluster, evaluated left to right like getopt
			// (mirrors spliceEnvLayer): the first value-taking
			// member (-u/-C/-a) takes the attached remainder as
			// its value, or the next token when trailing. A
			// trailing-only check would mistake `-uC` (unset C,
			// utility next) for `-C` (next token skipped as a
			// directory) and drop the real utility from the
			// risk/allowlist view.
			takesNext := false
			for k := 1; k < len(a); k++ {
				if a[k] == 'C' || a[k] == 'u' || a[k] == 'a' {
					takesNext = k+1 >= len(a)
					break
				}
			}
			if takesNext {
				// Trailing value-taking member (exact `-u` or a
				// cluster tail like `-iu`) without its operand:
				// fail closed to unknown, never bare env (bare
				// env classifies low and would Allow).
				if i+1 >= len(rest) {
					return []string{envSplitUnknown}, true
				}
				i += 2
				continue
			}
			i++
			continue
		}
		if envAssignRe.MatchString(a) {
			i++
			continue
		}
		break
	}
	if i > len(rest) {
		i = len(rest)
	}
	if i >= len(rest) {
		return nil, true
	}
	return rest[i:], true
}

// resolveEnvTarget fully unwraps nested env(1) layers (F1) to the real
// utility argv. Returns nil when argv is not env-led or when bare env
// carries no utility. The result aliases the input tail (no copying).
func resolveEnvTarget(argv []string) []string {
	eff := argv
	stripped := false
	for {
		next, isEnv := unwrapEnvTrampoline(eff)
		if !isEnv {
			break
		}
		stripped = true
		if len(next) == 0 {
			return nil
		}
		eff = next
	}
	if !stripped {
		return nil
	}
	return eff
}

// codeInterpreters is the set of binaries that can execute opaque scripts
// or inline code (12.2). Windows interpreters (cmd, powershell, pwsh,
// wscript, cscript, mshta) MUST stay in this set: membership drives the
// permission-mode-independent opaque-code escalation. Never add them to the
// risk tables alone - under permissions {"system_exec": "allow"} that
// combination would let opaque shell/code execute silently on Windows.
var codeInterpreters = map[string]bool{
	"python": true, "python3": true, "node": true, "ruby": true,
	"perl": true, "php": true, "lua": true, "r": true,
	"bash": true, "sh": true, "zsh": true, "dash": true, "fish": true,
	"cmd": true, "powershell": true, "pwsh": true,
	"wscript": true, "cscript": true, "mshta": true,
}

// isInterpreterWrapperPrefix reports whether base is a binary that delegates execution to the
// following utility (H1/F1): an interpreter after one of these prefixes
// is in command position ("sudo python3 -c ..."), while an interpreter
// after any other command ("echo python3 hello", "grep python3 x") is a
// plain argument and must not escalate. Execution wrappers (chroot,
// runuser, setsid, stdbuf, unshare, nsenter, gosu, tini, flock, ionice)
// stay transparent so "sudo stdbuf python3 evil.py" and
// "chroot / python3 evil.py" still escalate under allow (HIGH-3/F3).
// The set is owned by the sandbox leaf and read through its accessor here so
// the two layers cannot drift and no importer holds a mutable alias;
// internal/agent already imports internal/sandbox, while the reverse would
// cycle.
func isInterpreterWrapperPrefix(base string) bool {
	return sandbox.IsEnvTrampolinePrefix(base)
}

// interpreterCodeFlags are the standalone flags that mark inline-code
// execution in an interpreter tail (12.2). -E is perl(1) inline code
// ("-E" like "-e" with features enabled); -m is python(1) module
// execution ("python -m http.server" runs code, never a bare version
// probe); -B/-R are php(1) inline-code flags ("php -B 'init'" runs
// begin code, "php -R 'line'" runs per-line code, octal-encoded or
// plain - $'...' decoding feeds this path, not just hard-deny).
var interpreterCodeFlags = map[string]bool{
	"-c": true, "-e": true, "-r": true, "--eval": true,
	"-E": true, "-m": true, "-B": true, "-R": true,
}

// interpreterClusterValueTakers are short option letters that consume the
// attached remainder of a single-dash cluster token as their value on at
// least one code interpreter while NEVER acting as a boolean flag on any
// of them, so a c/e/r/m/E later in the token is an option value, not a
// code flag: -F (perl split pattern), -D (perl debugger flags), -C (perl
// unicode flags / ruby directory), -K (ruby encoding). Deliberately
// EXCLUDED: -X (python -X takes a value like -Xdev, but perl -X is a
// boolean flag, so "perl -Xe ..." executes code; fail-closed keeps -X out
// and "python3 -Xdev ..." over-asks instead), -d (php -d takes an ini
// value but perl -d is the boolean debugger, so "perl -de ..." executes
// code), -W (python/ruby take a value but perl -W is boolean warnings),
// -I (perl/ruby include dir but python -I is boolean isolated mode). When
// in doubt a letter stays OUT of this set: an unknown letter never hides
// a later code letter (fail-closed).
var interpreterClusterValueTakers = map[byte]bool{
	'F': true, 'D': true, 'C': true, 'K': true,
}

// isInterpreterInlineCodeFlag reports whether a single argv token is an
// interpreter inline-code/module flag in any form (F2): standalone
// -c/-e/-r/--eval/-E/-m/-B/-R, attached --eval=CODE, packed single-dash
// -c/-e/-r/-m/-E/-B/-R CODE forms ("-rCODE", "-mMODULE", "-Esay 1",
// "-Binit", "-Rline"), or bundled single-dash clusters with the code
// letter past first position ("-wE", "-nr", "-weputs 1", "-ucprint(1)",
// "-Bq", "-Rl"): the whole cluster is walked, so a c/e/r/m/E/B/R in any
// position counts ("perl -wE'say 1'", "php -nr'echo 1;'",
// "ruby -we'puts 1'", "python3 -uc'print(1)'", "php -B'init'").
// A known value-taking member (F/D/C/K) consumes the attached remainder
// as its value instead, ending the walk. -f is intentionally excluded: it
// names a script file (handled by the opaque non-flag/script scan), not
// inline code, so it stays out of the fail-closed code-flag path.
func isInterpreterInlineCodeFlag(a string) bool {
	if interpreterCodeFlags[a] {
		return true
	}
	if strings.HasPrefix(a, "--eval=") {
		return true
	}
	if len(a) > 2 && a[0] == '-' && a[1] != '-' {
		// No a[2] != '-' guard: an attached code payload may itself start
		// with '-' ("php -r'-echo 1;'" concatenates to "-r-echo 1;"), and
		// the walk below already returns on the code letter at k==1
		// before reaching it. a[1] != '-' still excludes "--..." longs.
		for k := 1; k < len(a); k++ {
			switch a[k] {
			case 'c', 'e', 'r', 'm', 'E', 'B', 'R':
				return true
			}
			if interpreterClusterValueTakers[a[k]] {
				// Attached value: the remainder is the
				// option's argument, never more flags.
				return false
			}
		}
	}
	return false
}

// wrapperValueFlagsByWrapper are per-wrapper options that consume the next
// token as their value (H1/F1): "sudo -u root python3 ..." runs python3,
// so "root" is an option value, not a command, and must not block
// interpreter reachability. The sets are per-wrapper because the same
// short flag differs across tools: "-n" takes an adjustment value on
// nice(1)/xargs(1) ("nice -n 5 python3 ...") but is a boolean
// non-interactive flag on sudo(8)/doas(1) ("sudo -n -u root python3 ..."
// runs python3 with -n set, so -u root must still be skipped); likewise
// "-p" takes a prompt value only on sudo and is boolean elsewhere
// (xargs -p interactive, su -p preserve-environment). Long --opt forms
// take a separate value token the same way (--opt=val stays one flag
// token and is skipped already). The table is the single source of truth,
// reused by interpreterTailReachable and checkSudoShellPayload (plus the
// short-cluster handling below): keep them in sync by construction.
// sudo(8): -C/--close-from, -D/--chdir, -g/--group, -p/--prompt,
// -R/--chroot, -r/--role, -T/--command-timeout, -t/--type,
// -U/--other-user, -u/--user. doas(1): -a style, -C config, -u user.
// timeout(1): -s/--signal, -k/--kill-after (duration is a separate
// positional operand). xargs(1): -a/--arg-file, -d/--delimiter,
// -E/--eof, -I/--replace, -L/--max-lines, -n/--max-args, -P/--max-procs,
// -s/--max-chars (plus deprecated -i/-l aliases). su(1): -s/--shell,
// -g/--group, -G/--supp-group. runuser(1): -u/--user, -g/--group,
// -s/--shell, -w/--whitelist-environment (-c is NOT listed: it is not a
// documented value flag, so a following payload stays visible
// fail-closed). stdbuf(1): -i/-o/-e buffer sizes (attached -o0 handled
// by the cluster logic). unshare(1)/nsenter(1)/ionice(1): the long/short
// options that take a separate value token. flock(1): -c/--command and
// -E/--conflict-exit-code. chroot(1): --userspec/--groups (NEWROOT is a
// positional operand skipped in interpreterTailReachable, like the
// timeout duration). setsid(1)/tini(1)/gosu(1) take no values, so they
// need no table entry: their flags skip as booleans.
var wrapperValueFlagsByWrapper = map[string]map[string]bool{
	"sudo": {
		"-u": true, "-g": true, "-p": true, "-C": true, "-D": true,
		"-R": true, "-r": true, "-T": true, "-U": true, "-t": true,
		"--user": true, "--group": true, "--prompt": true, "--close-from": true,
		"--chdir": true, "--chroot": true, "--role": true, "--command-timeout": true,
		"--other-user": true, "--type": true,
	},
	"doas": {
		"-u": true, "-C": true, "-a": true,
	},
	"nice": {
		"-n": true, "--adjustment": true,
	},
	"timeout": {
		"-s": true, "--signal": true, "-k": true, "--kill-after": true,
	},
	"xargs": {
		"-a": true, "--arg-file": true,
		"-d": true, "--delimiter": true,
		"-E": true, "--eof": true,
		"-I": true, "--replace": true, "-i": true,
		"-L": true, "--max-lines": true, "-l": true,
		"-n": true, "--max-args": true,
		"-P": true, "--max-procs": true,
		"-s": true, "--max-chars": true,
		"--process-slot-var": true,
	},
	"su": {
		"-s": true, "--shell": true,
		"-g": true, "--group": true,
		"-G": true, "--supp-group": true,
		"-w": true, "--whitelist-environment": true,
	},
	"runuser": {
		"-u": true, "--user": true,
		"-g": true, "--group": true,
		"-s": true, "--shell": true,
		"-w": true, "--whitelist-environment": true,
	},
	"stdbuf": {
		"-i": true, "--input": true,
		"-o": true, "--output": true,
		"-e": true, "--error": true,
	},
	"unshare": {
		"--map-user": true, "--map-group": true,
		"--map-users": true, "--map-groups": true,
		"--mount": true, "--mount-proc": true,
		"--wd": true, "--workdir": true, "--root": true,
		"--propagation": true,
	},
	"nsenter": {
		"-t": true, "--target": true,
		"-S": true, "--setuid": true,
		"-G": true, "--setgid": true,
		"-w": true, "--wd": true,
		"--root": true,
	},
	"ionice": {
		"-c": true, "--class": true,
		"-n": true, "--classdata": true,
		"-p": true, "--pid": true,
	},
	"flock": {
		"-c": true, "--command": true,
		"-E": true, "--conflict-exit-code": true,
	},
	"chroot": {
		"--userspec": true, "--groups": true,
	},
}

// wrapperTakesValue reports whether flag consumes the next token as its
// value under the given delegating wrapper (lowercased base, "" when no
// wrapper precedes the flag). Unknown wrappers and unknown flags are
// boolean: the next token stays visible. For interpreter-reachability
// that is fail-OPEN (a phantom option value looks like a command and
// blocks the real interpreter, e.g. "sudo -D /tmp python3 ..." with an
// unknown -D), so the per-wrapper tables above must stay complete;
// completeness is the fail-closed direction here.
func wrapperTakesValue(wrapper, flag string) bool {
	if set, ok := wrapperValueFlagsByWrapper[wrapper]; ok {
		return set[flag]
	}
	return false
}

// wrapperShortTakesValue reports whether a single short option letter
// takes a value under the given wrapper (derived from the shared table:
// "-u" in the table means 'u' takes a value).
func wrapperShortTakesValue(wrapper string, ch byte) bool {
	return wrapperTakesValue(wrapper, "-"+string([]byte{ch}))
}

// wrapperClusterConsumesNext reports whether a short-flag cluster token
// (e.g. "-sD", body "sD") consumes the NEXT argv token as an option value
// under the given wrapper: a value-taking member with no attached
// remainder (i.e. in last position) takes the next token; a value-taking
// member with attached remainder (e.g. "-uroot", "-s/bin/sh") takes the
// remainder instead and consumes nothing further.
func wrapperClusterConsumesNext(wrapper, body string) bool {
	for k := 0; k < len(body); k++ {
		if wrapperShortTakesValue(wrapper, body[k]) {
			if k+1 < len(body) {
				return false
			}
			return true
		}
	}
	return false
}

// suLongOptions is the known su(1) long-option set used for unique-prefix
// resolution (util-linux getopt_long abbreviations: --comm -> --command,
// --sess -> --session-command, --white -> --whitelist-environment).
var suLongOptions = []string{
	"command", "session-command", "shell", "group", "supp-group",
	"whitelist-environment", "preserve-environment", "login", "fast",
	"help", "version",
}

// suLongKind classifies a resolved su long option.
type suLongKind int

const (
	suLongUnknown suLongKind = iota
	suLongCarrier
	suLongTakesValue
	suLongBoolean
)

// resolveSuLong resolves an abbreviated su long name (without leading "--"
// and without any "=value") by unique prefix (getopt_long semantics).
// An exact match wins; a proper prefix matching exactly one known option
// resolves to it; zero or multiple matches are ambiguous -> unknown
// (scan through fail-closed, consume nothing).
func resolveSuLong(name string) suLongKind {
	var match string
	count := 0
	for _, full := range suLongOptions {
		if full == name {
			match = full
			count = 1
			break
		}
		if strings.HasPrefix(full, name) {
			match = full
			count++
		}
	}
	if count != 1 {
		return suLongUnknown
	}
	switch match {
	case "command", "session-command":
		return suLongCarrier
	case "shell", "group", "supp-group", "whitelist-environment":
		return suLongTakesValue
	default:
		return suLongBoolean
	}
}

// interpreterTailEscalation runs the shared per-tail escalation (12.2/12.3):
// script-content scan (hard-deny first) then opaque-code check. Returns
// non-nil on escalation, nil when clean. Single source of truth for both
// the direct interpreter loop and shell-carrier payloads.
func interpreterTailEscalation(tail []string) *GateDecision {
	// F2: a bare interpreter ("python3") or a lone "-" script operand
	// ("echo code | python3 -") reads code from stdin: opaque code
	// execution even under allow/bwrap. Informational flags
	// ("--version", "-V") stay on the hasOpaqueCodeArg path below.
	// Every tail here is interpreter-led by construction
	// (interpreterTails filters on codeInterpreters), so a bare or
	// stdin-fed interpreter always escalates.
	if len(tail) <= 1 || (len(tail) == 2 && tail[1] == "-") {
		return &GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: "interpreter executing stdin script/code requires user approval"}
	}
	// 12.3: Script-content heuristic scan (hard-deny first).
	if found, scrRisk := scanScriptContent(tail); scrRisk == RiskHigh && found != "" {
		// Distinguish hard-deny (from checkHardDeny) from risky
		// pattern match. Hard-deny reasons contain distinctive
		// phrases about destruction/permission changes.
		if strings.Contains(found, "recursive") ||
			strings.Contains(found, "raw device") ||
			strings.Contains(found, "filesystem") ||
			strings.Contains(found, "secure deletion") ||
			strings.Contains(found, "partition table") ||
			strings.Contains(found, "redirect to device") ||
			strings.Contains(found, "fork bomb") ||
			strings.Contains(found, "world-writable") ||
			strings.Contains(found, "ownership change") {
			return &GateDecision{Action: ActionDeny, Risk: RiskHigh, Reason: "script contains: " + found}
		}
		return &GateDecision{Action: ActionAsk, Risk: RiskHigh, Reason: "script contains risky pattern: " + found}
	}
	// 12.2: Interpreter opaque-code escalation.
	if hasOpaqueCodeArg(tail) {
		return &GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: "interpreter executing opaque script/code requires user approval"}
	}
	return nil
}

// interpreterTailReachable reports whether the interpreter at argv[i]
// (i > 0) sits in command position: every preceding token is transparent
// prefix material (a delegating wrapper, a VAR= assignment, a flag, a
// wrapper option value, a timeout duration, a chroot NEWROOT, a gosu
// user, a flock lock file, a separator, a shell command-opening word, or a
// subshell/group opener), e.g. "sudo python3 ...", "FOO=bar python3 ...",
// "command sudo python3 ...", "( sudo python3 ...", "! bash -c ...". An
// interpreter after a real command ("echo python3 hello", "echo command
// python3 ...") is an argument, not a command.
//
// The walk is the shared shellPrefixWalk that effectiveCommandArgv uses, so
// the hard-deny reachability rule and the Ask-side effective-command rule
// cannot drift. expansionsTransparent is true here: an unresolvable
// $VAR/${...} in command position may expand to the command or to wrapper
// prefix material, so the word after it stays in command position
// (fail-closed; "sudo -s '$CMD tar ${1:-cI}'" keeps the dashless tar cluster
// live). Group openers are normalized with sandbox.StripCdGroupOpeners so a
// grouped segment ("( sudo ...", "(sudo ...") resolves like its bare form.
func interpreterTailReachable(argv []string, i int) bool {
	if i <= 0 {
		return true
	}
	prefix := argv[:i]
	if stripped, grouped := sandbox.StripCdGroupOpeners(prefix); grouped {
		prefix = stripped
	}
	return shellPrefixOpaqueIndex(prefix, true) < 0
}

// interpreterTailHasCodeFlag reports whether the interpreter tail carries
// an inline-code flag ("-c", "-e", "-r", "--eval", "-E", "-m", "-B", "-R",
// "--eval=", packed -c/-e/-r/-m/-E/-B/-R forms, bundled clusters like
// "-wE"/"-nr"): find . -exec
// python3 -c ..." still escalates even though "." is not transparent
// prefix material. A bare "echo python3 -c ..." also asks (fail-closed,
// pinned by test) rather than risk missing a real execution.
func interpreterTailHasCodeFlag(tail []string) bool {
	for _, a := range tail[1:] {
		if isInterpreterInlineCodeFlag(a) {
			return true
		}
	}
	return false
}

// interpreterTails returns every interpreter-led tail of seg that sits in
// command position (H1/F1): wrapper prefixes (sudo, nice, FOO=bar, ...)
// place the interpreter after seg[0], so a seg[0]-only check misses "sudo
// python3 -c ...". Position 0 is always command position (segments split
// on pipes, so "echo hi | python3 x.py" leads its own segment); deeper
// positions need wrapper-prefix reachability or a following inline-code
// flag, so plain arguments ("echo python3 hello", "grep python3 x") stay
// green. The same walk runs over env-unwrapped targets, but only for
// env-led positions in command position (resolveEnvTarget(seg[i:]) for
// env in command position, covering "sudo env python3 ..." and
// -S/--split-string forms); an env that is itself a plain argument
// ("echo env python3 hello") is not followed. Flags and VAR=val
// assignments are skipped; quoted "python3 ..." stays one token and never
// equals an interpreter, so echo green is preserved.
//
// The env leg is bounded like the other every-position scans: coveredUntil
// skips positions an earlier unwrap already covered (a bare `env env ...`
// chain is one span, not one suffix re-expansion per token), and after the
// stream settles on a plain utility only maxSettledEnvScans trailing env
// positions are rescanned (a settled word may delegate or permute its
// operands, but a long interleaved `env x` tail is that utility's
// operands). Past maxEnvPositionScans the leg stops: the first unwrap's
// token scan already covers the stream, and stopping keeps the scan linear
// on adversarial alternating wrapper/env chains instead of expanding every
// suffix (500 bare env layers were 1.5s, 2000 were 84s).
func interpreterTails(seg []string) [][]string {
	var out [][]string
	collect := func(argv []string) {
		for i, tok := range argv {
			if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
				continue
			}
			if !codeInterpreters[strings.ToLower(resolveBinaryBase(shellPrefixClassToken(tok)))] {
				continue
			}
			if i > 0 && !interpreterTailReachable(argv, i) && !interpreterTailHasCodeFlag(argv[i:]) {
				continue
			}
			out = append(out, argv[i:])
		}
	}
	collect(seg)
	forEachEnvPosition(seg, true, true, func(_ int, eff []string) bool {
		// Only follow env in command position: the helper's
		// reachableOnly filter passes "sudo env python3 ..." and
		// skips "echo env python3 hello". Hand the once-unwrapped
		// stream to collect: every further env token inside it is an
		// operand of the resolved utility, never a fresh suffix.
		if len(eff) > 0 {
			collect(eff)
		}
		return true
	})
	return out
}

// hasOpaqueCodeArg reports whether argv[1:] contains a code-execution flag
// (-c, -e, -r, --eval, --eval=CODE, -E, -m, -B, -R, -f, packed
// -c/-e/-r/-m/-E/-B/-R CODE forms, bundled clusters like "-wE"/"-nr"/"-uc")
// or a non-flag argument (script file). Returns false when argv has no args or only
// informational flags like --version, -V, --help.
func hasOpaqueCodeArg(argv []string) bool {
	if len(argv) <= 1 {
		return false
	}
	for _, a := range argv[1:] {
		if a == "-f" || isInterpreterInlineCodeFlag(a) {
			return true
		}
		// Non-flag argument (script file).
		if !strings.HasPrefix(a, "-") {
			return true
		}
	}
	return false
}

// maxScriptScanBytes caps how much of a script file is read for heuristic
// scanning (12.3).
const maxScriptScanBytes = 256 * 1024

// riskyScriptPatterns is the regex list matched against script contents
// during the heuristic scan (12.3). Order matters: first match wins.
var riskyScriptPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)rm\s+-[^\s]*[rf][^\s]*\s*/`),
	regexp.MustCompile(`(?i)rm\s+-rf`),
	regexp.MustCompile(`(?i)dd\s+.*\bof=/dev/`),
	regexp.MustCompile(`(?i)\bmkfs\b`),
	regexp.MustCompile(`(?i)shred\s+/dev/`),
	regexp.MustCompile(`(?i)fdisk\s+/dev/`),
	regexp.MustCompile(`(?i)parted\s+/dev/`),
	regexp.MustCompile(`:\(\)\s*\{`),
	regexp.MustCompile(`(?i)chmod\s+-R\s+777\s*/`),
	regexp.MustCompile(`(?i)chown\s+-R\s+.*\s+/`),
	regexp.MustCompile(`(?i)git\s+push\s+.*(--force|-f\b)`),
	regexp.MustCompile(`(?i)curl\s+.*\|\s*(sh|bash)`),
	regexp.MustCompile(`(?i)wget\s+.*\|\s*(sh|bash)`),
	regexp.MustCompile(`(?i)base64\s+(-d|--decode).*\|\s*(sh|bash)`),
	regexp.MustCompile(`(?i)(sudo|su\s+-)`),
	regexp.MustCompile(`>\s*/dev/(?:sd|vd|hd|xvd|ubd|nvme|mmcblk|loop|dm-|mapper|disk)`),
	regexp.MustCompile(`(?i)os\.system\s*\(`),
	regexp.MustCompile(`(?i)subprocess\.(run|call|Popen|check_output|os\.system)`),
	regexp.MustCompile(`(?i)os\.remove\s*\(`),
	regexp.MustCompile(`(?i)shutil\.rmtree`),
	regexp.MustCompile(`(?i)\beval\s*\(`),
	regexp.MustCompile(`/dev/(?:sda|vda|hda|nvme|mmcblk|loop|dm-|mapper/)`),
}

// scanScriptContent reads and heuristically scans a script file referenced
// in argv for dangerous patterns (12.3). Returns the found pattern and
// RiskHigh on match, or ("", RiskLow) when clean / unreadable.
func scanScriptContent(argv []string) (string, CommandRisk) {
	// Find the first non-flag arg (the script file), skipping code flags.
	var scriptPath string
	for _, a := range argv[1:] {
		// Skip code-flag tokens in every form (standalone, --eval=,
		// packed, bundled clusters): the following token is code or a
		// module name, never a script file. Single source of truth is
		// isInterpreterInlineCodeFlag, kept in sync with
		// hasOpaqueCodeArg by construction; -f names a script file,
		// so only the flag token itself is skipped while its
		// filename operand stays a scan target below.
		if a == "-f" || isInterpreterInlineCodeFlag(a) {
			continue
		}
		// Skip other flags and flag-value args (e.g. "-m", "module").
		if strings.HasPrefix(a, "-") {
			continue
		}
		scriptPath = a
		break
	}
	if scriptPath == "" {
		return "", RiskLow
	}

	abs, err := filepath.Abs(scriptPath)
	if err != nil {
		return "", RiskLow
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", RiskLow
	}
	if len(data) > maxScriptScanBytes {
		data = data[:maxScriptScanBytes]
	}
	content := string(data)

	// Step 1: hard-deny circuit breaker against script content.
	if reason := checkHardDeny(nil, []string{content}); reason != "" {
		return reason, RiskHigh
	}

	// Step 2: risky pattern scan.
	for _, re := range riskyScriptPatterns {
		if match := re.FindString(content); match != "" {
			return match, RiskHigh
		}
	}

	return "", RiskLow
}

// effectiveRiskThreshold returns the ask-threshold for the given sandbox:
// - explicit sb.RiskThreshold ("low"|"medium"|"high"|"unknown") when set
// - otherwise bubblewrap mode -> RiskHigh, paths/off/nil -> RiskMedium
func effectiveRiskThreshold(sb *sandbox.SandboxConfig) CommandRisk {
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
	if sb == nil || sb.Mode == sandbox.SandboxModeOff || sb.Mode == sandbox.SandboxModePaths {
		return RiskMedium
	}
	// Bubblewrap -> RiskHigh.
	return RiskHigh
}

// EvaluateCommand applies the full policy to one system command.
// argv is parseCommandArgv(command) when len(args)==0, else
// append([]string{command}, args...). The flow label defaults to the
// start label (v1 parity); use EvaluateCommandWithLabel for flow-aware
// gating.
func EvaluateCommand(sb *sandbox.SandboxConfig, command string, args []string) GateDecision {
	l := permissions.StartLabel()
	return EvaluateCommandWithLabel(sb, command, args, &l)
}

// EvaluateCommandWithLabel applies the full policy plus the Flow Guard
// pre-dispatch check (FG-003) under label. A nil label is fail-closed when a
// v2 policy is installed (evaluated as narrowest); with no installed policy
// the flow lookup reports ok=false and the verdict keeps v1 behavior, so nil
// is inert there. Flow can only escalate, never widen: base deny-class
// verdicts stand, RiskUnknown still always asks.
func EvaluateCommandWithLabel(sb *sandbox.SandboxConfig, command string, args []string, label *permissions.SessionLabel) GateDecision {
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

	// 1. Permission: sb.Permitted("system_exec") == "deny" -> ActionDeny.
	if sb != nil {
		if action, ok := sb.Permitted("system_exec"); ok && action == "deny" {
			return GateDecision{Action: ActionDeny, Risk: RiskUnknown, Reason: "system_exec is denied by sandbox permissions"}
		}
	}

	// 2. Hard-deny circuit breaker.
	if reason := hardDenyReason(command, argv); reason != "" {
		// Determine risk for logging; use HIGH for hard-denied commands.
		risk := classifyRisk(argv)
		util.DebugWarn("gate_hard_deny", "command", command, "reason", reason, "risk", risk.String())
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

	// 4. Allowlist: if AllowedCommands is non-empty every executed binary
	// must be listed - shell-form (sh -c) authorizes all && ; || | &
	// segments, not just argv[0] (F2: "ls && rm ..." must not pass on ls
	// alone). Env trampolines authorize the real utility (M1: "env ls"
	// checks ls, not env).
	if sb != nil && len(sb.AllowedCommands) > 0 {
		segArgvs := [][]string{argv}
		if len(args) == 0 {
			segArgvs = shellSegmentArgvs(command)
		}
		for _, seg := range segArgvs {
			if len(seg) == 0 {
				return GateDecision{Action: ActionDeny, Risk: classifyRisk(argv), Reason: "command is not in the allowlist"}
			}
			effSeg := seg
			if eff := resolveEnvTarget(seg); len(eff) > 0 {
				effSeg = eff
			}
			bin := filepath.Base(effSeg[0])
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
	}

	risk := classifyRisk(argv)
	// F2: shell-form lines execute every segment (sh -c whole line), so
	// rate the maximum risk across && ; || | & segments instead of argv[0]
	// alone ("ls && rm ..." is not low-risk because it starts with ls).
	if len(args) == 0 {
		if segRisk := classifyShellSegments(command); segRisk > risk {
			risk = segRisk
		}
	}

	// 4b (resolved first, H3): Permissions policy (permissions.json,
	// PM-002) + Flow Guard pre-dispatch (FG-003). Resolved before any
	// ask-escalation so EffectDeny short-circuits immediately and can
	// never be downgraded to ask by shell-trick or interpreter
	// escalation. Ask/allow outcomes are deferred until after escalation
	// (max(deny,ask,allow), deny authoritative). No installed policy =
	// skip with zero behavior change. A policy allow still falls through
	// on RiskUnknown (fail-closed, step 5 below). Flow only escalates: a
	// requires failure moves allow->ask (or ->deny when the requiring
	// rule denies), and a narrowed label with no mapped rule fails closed
	// (C2). The pre-check records (labelVersion, argsHash) for the
	// post-admission re-check (H2). S14 single identity: the shell
	// resources are dual (permissions.ShellResources: ShellLine primary +
	// bare command fallback, M4) - the same strings the sandbox audit
	// and the ask binding hash - so a rule that matches the full line
	// (with args) cannot be bypassed by matching the bare command in one
	// gate and the line in another. Migration note: rules authored
	// against the bare command keep matching via the fallback leg;
	// tighten toward the ShellLine form (see docs/permissions/guide.md).
	var policyAsk *GateDecision
	policyAllow := false
	shellRes := permissions.ShellResources(command, args)
	if eff, rule, detail, ok := permissions.LookupWithLabel(label, "shell", shellRes...); ok {
		// M4 fired-leg audit (debug-gated): log which shell leg fired.
		flowAuditPathLeg(label, "shell", shellRes, rule, detail)
		if detail != "" && rule == nil {
			// Flow unknown-sink fail-closed on a narrowed label (C2).
			if eff == permissions.EffectDeny {
				return GateDecision{Action: ActionDeny, Risk: risk, Reason: detail + " by permissions policy"}
			}
			policyAsk = &GateDecision{Action: ActionAsk, Risk: risk, Reason: detail + " by permissions policy"}
		} else if rule != nil {
			flowReason := ""
			if detail != "" {
				flowReason = detail + " by permissions policy"
			}
			switch eff {
			case permissions.EffectDeny:
				reason := "denied by permissions policy" + ruleSuffix(rule)
				if flowReason != "" {
					reason = flowReason
				}
				return GateDecision{Action: ActionDeny, Risk: risk, Reason: reason, PolicyRule: rule}
			case permissions.EffectAsk:
				reason := "requires approval by permissions policy" + ruleSuffix(rule)
				if flowReason != "" {
					reason = flowReason
				}
				policyAsk = &GateDecision{Action: ActionAsk, Risk: risk, Reason: reason, PolicyRule: rule}
			case permissions.EffectAllow:
				// Best-effort @-file operand check (M4) before allowing
				// or falling through to RiskUnknown: a shell command
				// referencing a flow-denied file escalates here.
				if d := checkAtFileFlow(label, command, args); d != nil {
					if d.Action == ActionDeny {
						return *d
					}
					policyAsk = d
				} else {
					policyAllow = true
				}
			}
		}
	}

	// 12.2 / 12.3: Indirection hardening for code interpreters. Shell-form
	// checks every segment (F2: "ls && python3 evil.py" escalates on the
	// trailing interpreter, not just argv[0]). Deny stays authoritative;
	// ask wins over a stored policy allow (H3).
	interpArgvs := [][]string{argv}
	if len(args) == 0 {
		interpArgvs = shellSegmentArgvs(command)
		// ANSI-C $'...' octal/hex decoding feeds interpreter escalation
		// too, not just hard-deny: "php $'-B...'" hides a code flag
		// from the raw argv leg, so the decoded segments escalate
		// exactly like the raw ones.
		if decoded, _ := decodeANSICQuotes(command); decoded != command {
			interpArgvs = append(interpArgvs, shellSegmentArgvs(decoded)...)
		}
	}
	for _, seg := range interpArgvs {
		if len(seg) == 0 {
			continue
		}
		// H1/F1: interpreters hide behind wrapper prefixes (sudo python3
		// -c ...) and env trampolines: walk every token position, plus
		// every env-unwrapped tail. Quoted text stays one token, so echo
		// green is preserved.
		for _, tail := range interpreterTails(seg) {
			if d := interpreterTailEscalation(tail); d != nil {
				return *d
			}
		}
		// The Ask-only fail-closed leaves (code tools, git config/env,
		// env/make/tar chdir, option-stream env, chmod subpaths,
		// parameter-expansion tails, and an unresolvable effective
		// command) are shared with the shell-carrier payload circuit so
		// the two verdicts cannot drift. Hard-deny already ran above, so
		// a known exec-capable form still Denies; these legs only Ask.
		if d := shellAskLegs(seg, risk); d != nil {
			return *d
		}
		// Shell-mode carriers hide interpreter invocations and
		// unresolvable tails as data ("sudo -s 'python3 evil.py'",
		// "sudo -i python3 evil.py", "su -c 'python3 evil.py'",
		// "sh -c 'tar $1'"): the outer seg shows only the carrier,
		// so route the joined carrier payload back through the
		// shared interpreter + Ask escalation. Runs here (not in
		// hard-deny) so allow-still-asks holds without widening
		// deny. Only command-position carriers count, so echo
		// green is preserved.
		for i, tok := range seg {
			if tok == "" || strings.HasPrefix(tok, "-") || envAssignRe.MatchString(tok) {
				continue
			}
			if i > 0 && !interpreterTailReachable(seg, i) {
				continue
			}
			for _, p := range shellCarrierPayloadsAt(seg, i) {
				if d := carrierInterpreterEscalation(p); d != nil {
					return *d
				}
			}
		}
	}
	// Quote-stripped string legs for wrapper-prefixed forms whose argv
	// hides the payload ("sudo chmod -R 777 /etc/ssh"): quoted text is
	// data and was stripped, so quoted-echo greens stay green.
	if len(args) == 0 {
		stripped := stripQuotedRegions(command)
		if stripped != command {
			for _, seg := range shellSegmentArgvs(stripped) {
				if len(seg) == 0 {
					continue
				}
				if reason := chmodSubpathAskReason(seg); reason != "" {
					return GateDecision{Action: ActionAsk, Risk: RiskHigh, Reason: reason}
				}
				if reason := gitTokenAskReason(seg); reason != "" {
					return GateDecision{Action: ActionAsk, Risk: risk, Reason: reason}
				}
				// Quote stripping blanks '...' payloads, so a benign
				// `env -S 'ls'` degrades to a dangling `env -S` whose
				// unexpandable split-string is a quote artifact, not
				// proof of hidden nesting. The argv leg above already
				// Asks on real -C payloads (proven or unproven); the
				// stripped view only re-checks proven -C, so a
				// degraded view never invents a nesting Ask while a
				// genuine chdir still cannot hide behind quotes.
				if chdir, _ := hasEnvChdirOpt(seg); chdir {
					return GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: envChdirReason}
				}
				if reason := paramExpansionAskReason(seg); reason != "" {
					return GateDecision{Action: ActionAsk, Risk: RiskUnknown, Reason: reason}
				}
			}
		}
	}

	// F2: shell-evasion escalation. Substitution and encoding tricks hide
	// the real payload from argv[0]-only classification, so fail closed to
	// ask. Plain chaining alone is covered by the segment-max risk above
	// (benign multi-low chains like "echo a; echo b" stay allowed) and
	// env-prefix / plain redirects by the trampoline and device checks.
	// Runs after the policy deny short-circuit (H3) so ask can never
	// downgrade a deny, but still wins over a stored allow.
	if len(args) == 0 {
		if reason := shellTrickEscalation(command); reason != "" {
			return GateDecision{Action: ActionAsk, Risk: risk, Reason: reason}
		}
	}

	// Deferred policy outcomes (H3 combine): ask now, allow only for
	// known risk (fail-closed on unknown is step 5).
	if policyAsk != nil {
		return *policyAsk
	}
	if policyAllow && risk != RiskUnknown {
		return GateDecision{Action: ActionAllow, Risk: risk, Reason: ""}
	}

	// 5. Risk == RiskUnknown -> ActionAsk (fail-closed on unknown binaries).
	// Per semantics note: RiskUnknown ALWAYS asks, regardless of permission mode.
	if risk == RiskUnknown {
		return GateDecision{Action: ActionAsk, Risk: risk, Reason: "unknown command requires user approval"}
	}

	// 6. Permission "allow" -> ActionAllow (hard-deny still applied at step 2).
	if sb != nil {
		if action, ok := sb.Permitted("system_exec"); ok && action == "allow" {
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
