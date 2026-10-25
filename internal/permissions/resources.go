// resources.go - Flow Guard canonical resource helpers (FG-002/M4).
//
// Shell uses the raw command line, file actions use scoped absolute paths
// (canonicalized by the caller via sandbox ResolveScopedPath), URLs use
// normalized host (lowercase, IDNA, no fragment, query kept for decision).
package permissions

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/net/idna"
)

// ArgsHash hashes call arguments for audit metadata (never raw args in
// logs). Full sha256 hex; callers truncate for display.
func ArgsHash(args ...string) string {
	h := sha256.New()
	for i, a := range args {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ShellLine builds the canonical shell resource line from a command plus
// explicit args (S14 shared pure helper): the same TrimSpace(command +
// " " + Join(args)) identity the agent flow mapper and the sandbox gate
// match against, so both sides gate the same string. Pure, no policy
// state; adding it changes no existing API.
func ShellLine(command string, args []string) string {
	line := strings.TrimSpace(command + " " + strings.Join(args, " "))
	if line == "" {
		line = command
	}
	return line
}

// ShellResources builds the dual-match shell resource list (S9/M4):
// canonical ShellLine primary, bare trimmed command fallback. The policy
// engine ORs across resources, so a rule matching either leg fires; the
// gate audits which leg fired via the debug-gated leg audit. Identical
// legs collapse to one entry. Migration note: rules authored against
// the bare command keep matching via the fallback leg; tighten toward
// the ShellLine form (see docs/permissions/guide.md).
func ShellResources(command string, args []string) []string {
	line := ShellLine(command, args)
	bare := strings.TrimSpace(command)
	if bare == "" || bare == line {
		return []string{line}
	}
	return []string{line, bare}
}

// FlowArgsHash hashes the canonical (tool, action, resources) triple
// for audit metadata and (labelVersion, argsHash) bindings (S14/H2
// shared identity): every gate (agent pre-dispatch, python gate,
// shell binding) must hash the same triple so one approval grant
// covers exactly one call. Hashes only, never raw args (M2).
func FlowArgsHash(tool, action string, resources []string) string {
	parts := append([]string{tool, action}, resources...)
	return ArgsHash(parts...)
}

// ShortHash returns the first 12 hex chars of ArgsHash for audit lines
// (matches the python code_hash convention).
func ShortHash(args ...string) string {
	full := ArgsHash(args...)
	if len(full) > 12 {
		return full[:12]
	}
	return full
}

// CanonicalPath canonicalizes a path-shaped policy resource to a single
// identity (S5 shared): Clean + absolute + best-effort EvalSymlinks with
// nearest-ancestor fallback. Never errors; unresolvable inputs fall back
// to the cleaned absolute path with resolved=false. Shared by the agent
// flow mapper and the sandbox gates so both sides hash the same identity.
//
// Alias folding (H4): the Win32 alias classes that change which file the
// OS opens (device-namespace prefixes, ADS suffixes, trailing dot/space
// folds) plus ~/ expansion fold here, once, so the gate and the sandbox
// share one identity instead of two divergent canonicalizers.
func CanonicalPath(raw string) (canon string, resolved bool) {
	if raw == "" {
		return "", false
	}
	c := raw
	if c == "~" {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			c = h
		} else {
			return raw, false
		}
	} else if strings.HasPrefix(c, "~/") || strings.HasPrefix(c, `~\`) {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			c = filepath.Join(h, c[2:])
		}
	}
	c = stripWin32Alias(c)
	c = filepath.Clean(c)
	if !filepath.IsAbs(c) {
		if abs, err := filepath.Abs(c); err == nil {
			c = abs
		}
	}
	c = filepath.Clean(c)
	if r, err := filepath.EvalSymlinks(c); err == nil {
		return filepath.Clean(r), true
	}
	dir := c
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		if r, err := filepath.EvalSymlinks(parent); err == nil {
			rel, rerr := filepath.Rel(parent, c)
			if rerr == nil {
				return filepath.Clean(filepath.Join(r, rel)), false
			}
			break
		}
		dir = parent
	}
	return c, false
}

// isWindowsShaped reports whether p looks like a Windows path (drive
// absolute like C:\ or C:/, UNC/device prefix \\) or the process runs on
// Windows (where every path may carry Win32 aliases). POSIX names such as
// "a:b" or "a/b:c" are NOT windows-shaped: on POSIX a colon is a valid
// filename character and must survive unchanged.
//
// Security (M1 sign-off): folding ADS (":stream"), trailing dots/spaces
// and device-namespace prefixes only on windows-shaped input preserves
// the POSIX identity ("a:b" stays "a:b") while keeping the canonical leg
// for genuine Win32 aliases. The raw leg still matches in the gate, so a
// missed fold never widens access - it only skips an extra canonical leg.
func isWindowsShaped(p string) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	if len(p) >= 3 {
		c0 := p[0]
		if (c0 >= 'A' && c0 <= 'Z' || c0 >= 'a' && c0 <= 'z') && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
			return true
		}
	}
	if len(p) == 2 {
		c0 := p[0]
		if (c0 >= 'A' && c0 <= 'Z' || c0 >= 'a' && c0 <= 'z') && p[1] == ':' {
			return true
		}
	}
	return false
}

// stripWin32Alias folds the Win32 alias classes that change which file
// the OS opens into one identity: device-namespace prefixes (\\?\ , \\.\)
// are stripped, ADS suffixes (name:stream) are cut on the final element,
// and trailing dots/spaces are trimmed per element. The raw leg still
// matches, so over-folding here only ADDS a canonical leg, never removes
// coverage. POSIX inputs that are not windows-shaped pass through
// unchanged (see isWindowsShaped).
func stripWin32Alias(p string) string {
	if !isWindowsShaped(p) {
		return p
	}
	cut := p
	if strings.HasPrefix(cut, `\\?\`) {
		cut = cut[len(`\\?\`):]
		if strings.HasPrefix(cut, `UNC\`) {
			cut = `\\` + cut[len(`UNC\`):]
		}
	} else if strings.HasPrefix(cut, `\\.\`) {
		cut = cut[len(`\\.\`):]
	}
	// ADS suffix on the final element (C:\ws\file:ads -> C:\ws\file).
	if i := strings.LastIndexAny(cut, `/\`); i >= 0 {
		head, tail := cut[:i+1], cut[i+1:]
		if j := strings.IndexByte(tail, ':'); j >= 0 {
			tail = tail[:j]
		}
		tail = strings.TrimRight(tail, ". ")
		cut = head + tail
	} else if j := strings.IndexByte(cut, ':'); j > 1 {
		drive, rest := cut[:j+1], cut[j+1:]
		if k := strings.IndexByte(rest, ':'); k >= 0 {
			rest = rest[:k]
		}
		cut = drive + strings.TrimRight(rest, ". ")
	}
	return cut
}

// PathResources builds the dual-match resource list for a path-shaped
// operand (S5 shared): canonical primary, raw fallback. Identical legs
// collapse to one entry. POSIX keeps both legs even when they name the
// same file (a symlink and its target are textually distinct spellings,
// so rules authored against either spelling still match). Windows-only
// collapses below are gated on runtime.GOOS: case-insensitive filesystems
// fold EqualFold spellings, and SameFile folds short-name (8.3) vs
// long-name spellings of the same file instead of emitting a spurious
// second leg.
func PathResources(raw string) []string {
	canon, _ := CanonicalPath(raw)
	if canon == "" || canon == raw {
		return []string{raw}
	}
	if runtime.GOOS != "windows" {
		return []string{canon, raw}
	}
	if strings.EqualFold(canon, raw) {
		return []string{raw}
	}
	if sameFilePath(canon, raw) {
		return []string{raw}
	}
	return []string{canon, raw}
}

// sameFilePath reports whether a and b name the same file (both must
// exist; missing paths return false so callers keep both legs).
func sameFilePath(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// NormalizeURL canonicalizes a URL for policy matching: lowercase scheme
// and host, IDNA punycode for homograph hosts, strip fragment, keep query
// (query participates in the decision). Best-effort: unparseable input
// returns the trimmed input unchanged rather than erroring the gate.
func NormalizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if ascii, err := idna.Lookup.ToASCII(host); err == nil && ascii != "" {
		host = ascii
	}
	// Hostname() strips IPv6 brackets: restore them so rule patterns
	// written for [::1] still match (JoinHostPort adds brackets).
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Fragment = ""
	return u.String()
}
