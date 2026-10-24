// pip_pin.go - pip exact-pin + require-hashes + .venv integrity (S27).
//
// S18 made pip gated (allowlist + approval). S27 hardens the install
// itself, opt-in via SandboxConfig.PipStrictPins (additive: strict off
// keeps exact S18 behavior, no v1 break):
//
//   - pin ==version: the allowlist entry that authorizes an auto-install
//     must carry an exact pin (name==version). A bare entry never
//     authorizes under strict mode; extras (pkg[x]), ranges (>=, ~=),
//     direct references (@, ://) and markers (;) refuse. The pin is what
//     installs, so the model cannot drift the version.
//   - require-hashes: when SandboxConfig.PipHashes is non-empty, the
//     package must carry at least one recorded sha256 digest, and the
//     choke installs with --require-hashes + --hash=sha256:<digest>
//     (transitive sdist/wheel swaps fail the install instead of landing).
//   - transitive: extras and non-pin specifiers refuse (they pull
//     unpinned transitive closures); the installer package itself
//     ("pip") is never auto-installable.
//   - sitecustomize + .venv integrity: before the choke runs, the venv
//     must hold pyvenv.cfg and no unexpected sitecustomize.py /
//     usercustomize.py import hooks (unexpected content denies unless its
//     sha256 is listed in PipTrustedSitecustomize).
//
// Hash-only audit: denials hash the package name (pipAuditCommand); the
// approval prompt keeps the human-readable pin (prompt, not audit).
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
)

// pipPinNameRe / pipPinVersionRe bound the strict pin shape (S27): the
// same 64-char package alphabet as S18 plus an exact == version leg.
var (
	pipPinNameRe    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	pipPinVersionRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
	pipHashHexRe    = regexp.MustCompile(`^[A-Fa-f0-9]{64}$`)
)

// splitPipPin parses an allowlist-grade pin: "name" (no pin) or
// "name==version" (exact pin). Anything else (extras, ranges, URLs,
// markers, whitespace) refuses fail-closed.
func splitPipPin(entry string) (name, version string, hasPin bool, err error) {
	e := strings.TrimSpace(entry)
	if e == "" || strings.ContainsAny(e, " \t\n\r[]();@$/\\*|&$`'\"<>:!~^") {
		return "", "", false, fmt.Errorf("pip pin refused: malformed entry")
	}
	// "==" is the only sanctioned separator; a single "=" refuses.
	if strings.Contains(e, "=") && !strings.Contains(e, "==") {
		return "", "", false, fmt.Errorf("pip pin refused: only exact == pins allowed")
	}
	if i := strings.Index(e, "=="); i >= 0 {
		name, version = e[:i], e[i+2:]
		if strings.Contains(version, "=") {
			return "", "", false, fmt.Errorf("pip pin refused: only exact == pins allowed")
		}
		if !pipPinNameRe.MatchString(name) || !pipPinVersionRe.MatchString(version) || version == "" {
			return "", "", false, fmt.Errorf("pip pin refused: malformed name==version")
		}
		if strings.EqualFold(name, "pip") {
			return "", "", false, fmt.Errorf("pip pin refused: the installer itself is never auto-installable")
		}
		return name, version, true, nil
	}
	if !pipPinNameRe.MatchString(e) {
		return "", "", false, fmt.Errorf("pip pin refused: malformed package name")
	}
	if strings.EqualFold(e, "pip") {
		return "", "", false, fmt.Errorf("pip pin refused: the installer itself is never auto-installable")
	}
	return e, "", false, nil
}

// matchStrictAllowlist resolves a bare auto-pip request against the
// strict allowlist: the entry's base name must match (lowercase, S18
// discipline) AND carry an exact pin. Returns the pin to install
// (name==version) and the recorded hashes for the base name.
func matchStrictAllowlist(sb *sandbox.SandboxConfig, reqName string) (pin string, hashes []string, err error) {
	want := strings.ToLower(strings.TrimSpace(reqName))
	for _, entry := range sb.PipAllowlist {
		name, version, hasPin, perr := splitPipPin(entry)
		if perr != nil {
			// Malformed entries fail closed on match attempt: they
			// authorize nothing (deny, never skip-and-continue into a
			// looser entry for the same package... entries are
			// independent, so continue scanning is safe: a malformed
			// entry simply never matches).
			continue
		}
		if strings.ToLower(name) != want {
			continue
		}
		if !hasPin {
			// M5 hash-only denial: the allowlist entry names a package,
			// which can carry model-controlled text - the audit and the
			// error carry the hash, never the raw name.
			return "", nil, fmt.Errorf("pip strict pins: allowlist entry hash:%s must carry an exact == pin", permissions.ShortHash(reqName))
		}
		return name + "==" + version, sb.PipHashes[strings.ToLower(name)], nil
	}
	return "", nil, fmt.Errorf("package not in pip allowlist (pip_allowlist)")
}

// pipInstallArgs renders the choke argv: ["install", pin], plus
// --require-hashes and one --hash per recorded digest when hashes are
// configured (pip then refuses anything not matching the digests,
// including transitive swaps).
func pipInstallArgs(pin string, hashes []string) []string {
	args := []string{"install"}
	if len(hashes) > 0 {
		args = append(args, "--require-hashes")
		for _, h := range hashes {
			args = append(args, "--hash=sha256:"+strings.ToLower(h))
		}
	}
	return append(args, pin)
}

// validPipHash reports whether h is a sha256 hex digest.
func validPipHash(h string) bool { return pipHashHexRe.MatchString(strings.TrimSpace(h)) }

// venvRootForPip resolves the venv root owning pipBin (./.venv/bin/pip ->
// ./.venv). Stub-injectable for hermetic tests.
var venvRootForPip = func(pipBin string) string {
	return filepath.Dir(filepath.Dir(pipBin))
}

// pipBinForPyBin derives the absolute pip binary for the interpreter's own
// venv (H1 single-root): the installer that serves a ModuleNotFoundError
// must live in the same venv as the failing interpreter, not in a
// cwd-anchored ./.venv that can diverge when the run dir differs from the
// process cwd. Relative pyBin anchors at workDir when provided, else at
// the process cwd; the result is absolute so verify + choke share one root.
func pipBinForPyBin(pyBin, workDir string) string {
	exe := "pip"
	if runtime.GOOS == "windows" {
		exe = "pip.exe"
	}
	if strings.TrimSpace(pyBin) != "" {
		dir := filepath.Dir(pyBin)
		if !filepath.IsAbs(pyBin) && strings.TrimSpace(workDir) != "" {
			dir = filepath.Dir(filepath.Join(workDir, pyBin))
		}
		if filepath.IsAbs(filepath.Join(dir, exe)) {
			return filepath.Clean(filepath.Join(dir, exe))
		}
		if abs, err := filepath.Abs(filepath.Join(dir, exe)); err == nil {
			return abs
		}
		return filepath.Join(dir, exe)
	}
	base := strings.TrimSpace(workDir)
	if base == "" {
		base = "."
	}
	if runtime.GOOS == "windows" {
		return absJoin(base, ".venv", "Scripts", exe)
	}
	return absJoin(base, ".venv", "bin", exe)
}

// absPyBin anchors a (possibly relative) venv python binary at workDir,
// mirroring pipBinForPyBin: the interpreter spawn, the integrity verify,
// and the pip choke must share one absolute root even when the process
// cwd differs from the run workspace (H1 single-root).
func absPyBin(pyBin, workDir string) string {
	if strings.TrimSpace(pyBin) == "" {
		return pyBin
	}
	if filepath.IsAbs(pyBin) {
		return filepath.Clean(pyBin)
	}
	if strings.TrimSpace(workDir) != "" {
		return filepath.Clean(filepath.Join(workDir, pyBin))
	}
	if abs, err := filepath.Abs(pyBin); err == nil {
		return abs
	}
	return pyBin
}

// absJoin joins elems onto base and returns an absolute clean path (fail
// open to the joined path only when Abs errors, which never happens on
// supported platforms).
func absJoin(base string, elems ...string) string {
	p := filepath.Join(append([]string{base}, elems...)...)
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// absVenvRoot resolves the single absolute venv root for a pip binary
// (H1): verifyVenvIntegrity and the choke pip spawn must agree, even when
// the process cwd differs from the run workspace.
func absVenvRoot(pipBin string) string {
	root := venvRootForPip(pipBin)
	if filepath.IsAbs(root) {
		return filepath.Clean(root)
	}
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

// isStartupHookPath reports whether p is a startup-importable hook leg:
// the module form (sitecustomize.<ext> / usercustomize.<ext>) or the
// package form (sitecustomize/__init__.<ext> / usercustomize/__init__.<ext>)
// where <ext> is a startup-importable suffix (py, pyc, pyd, so).
// Matching is case-insensitive; the walk already lowercases the base.
func isStartupHookPath(p, baseLower string) bool {
	switch baseLower {
	case "sitecustomize.py", "sitecustomize.pyc", "sitecustomize.pyd", "sitecustomize.so",
		"usercustomize.py", "usercustomize.pyc", "usercustomize.pyd", "usercustomize.so":
		return true
	case "__init__.py", "__init__.pyc", "__init__.pyd", "__init__.so":
		parent := strings.ToLower(filepath.Base(filepath.Dir(p)))
		return parent == "sitecustomize" || parent == "usercustomize"
	}
	return false
}

// maxVenvAuditEntries bounds the .venv integrity walk (S27): large
// site-packages trees must not false-deny. Heavy generated dirs carry no
// import hooks, so they are skipped; the cap only fires on absurd trees.
const maxVenvAuditEntries = 32768

// rogueImportHook reports the first unexpected startup-importable hook
// leg under venvRoot (M2: module form sitecustomize.<ext> /
// usercustomize.<ext> and package form sitecustomize/__init__.<ext> /
// usercustomize/__init__.<ext>, where <ext> is py, pyc, pyd, or so),
// plus the first unexpected code-executing *.pth file (R31): files
// whose content sha256 is not in the trusted set. Absent hooks return
// "". Pure path-list .pth files (sys.path additions only, no import
// lines) are allowed: they cannot execute code at startup, and the S20
// loader preamble drops unknown search-path legs from the child sys.path
// anyway. The walk is bounded (fail closed on walk errors or absurd
// entry counts).
func rogueImportHook(venvRoot string, trusted map[string]bool) (string, error) {
	var rogue string
	entries := 0
	werr := filepath.WalkDir(venvRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			lower := strings.ToLower(d.Name())
			// M7: generated trees never hold import hooks - skip them so
			// real venvs (thousands of dist-info/__pycache__ entries)
			// audit instead of tripping the entry cap.
			if lower == "__pycache__" || strings.HasSuffix(lower, ".dist-info") || strings.HasSuffix(lower, ".egg-info") {
				return filepath.SkipDir
			}
			return nil
		}
		entries++
		if entries > maxVenvAuditEntries {
			return fmt.Errorf("venv too large to audit")
		}
		base := strings.ToLower(d.Name())
		isHook := isStartupHookPath(p, base)
		if !isHook {
			// R31: a .pth file with an import line executes at
			// interpreter startup (site.py addpackage), the same class
			// as an import hook - untrusted content denies. Pure
			// path-list .pth files cannot execute and stay allowed.
			if !strings.HasSuffix(base, ".pth") {
				return nil
			}
			raw, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			if !pthExecutesCode(string(raw)) {
				return nil
			}
			sum := sha256.Sum256(raw)
			if !trusted[hex.EncodeToString(sum[:])] {
				rogue = p
			}
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(raw)
		if !trusted[hex.EncodeToString(sum[:])] {
			rogue = p
		}
		return nil
	})
	if werr != nil {
		return "", fmt.Errorf("venv integrity check failed: %v", werr)
	}
	return rogue, nil
}

// pthExecutesCode reports whether .pth file content executes code at
// interpreter startup (R31): per site.py addpackage semantics, lines
// starting with "import" execute, "#" lines are comments, and every
// other line is a (benign) sys.path addition.
func pthExecutesCode(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if t == "import" || strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "import\t") {
			return true
		}
	}
	return false
}

// verifyVenvIntegrity gates the strict install on .venv health (S27, R31):
// pyvenv.cfg must exist (a missing/deleted venv never installs into),
// and no unexpected startup-execution files may be present: import hooks
// (M2: sitecustomize/usercustomize module or package legs across py/pyc/
// pyd/so) and code-executing *.pth files (site.py addpackage import
// lines). trusted lists allowed content hashes; empty means any such
// file denies.
func verifyVenvIntegrity(venvRoot string, trusted []string) error {
	if strings.TrimSpace(venvRoot) == "" {
		return fmt.Errorf("venv integrity refused: empty venv root")
	}
	if fi, err := os.Stat(filepath.Join(venvRoot, "pyvenv.cfg")); err != nil || fi.IsDir() {
		return fmt.Errorf("venv integrity refused: missing pyvenv.cfg (refusing to install into an unverified venv)")
	}
	allow := make(map[string]bool, len(trusted))
	for _, h := range trusted {
		allow[strings.ToLower(strings.TrimSpace(h))] = true
	}
	if rogue, rerr := rogueImportHook(venvRoot, allow); rerr != nil {
		return fmt.Errorf("venv integrity refused: %v", rerr)
	} else if rogue != "" {
		return fmt.Errorf("venv integrity refused: unexpected import hook present (sitecustomize/usercustomize)")
	}
	return nil
}

// checkPipStrict runs the S27 gate after the S18 gates pass: pin match,
// hash requirement, venv integrity. Returns the install pin and argv.
// Non-strict configs never reach here (caller checks PipStrictPins).
func checkPipStrict(sb *sandbox.SandboxConfig, pkg, pipBin string) (pin string, args []string, err error) {
	strictPin, hashes, merr := matchStrictAllowlist(sb, pkg)
	if merr != nil {
		return "", nil, merr
	}
	for _, h := range hashes {
		if !validPipHash(h) {
			return "", nil, fmt.Errorf("pip strict pins: recorded hash for package is malformed (refusing to install)")
		}
	}
	if len(sb.PipHashes) > 0 && len(hashes) == 0 {
		return "", nil, fmt.Errorf("pip strict pins: package has no recorded hashes (pip_hashes)")
	}
	if verr := verifyVenvIntegrity(absVenvRoot(pipBin), sb.PipTrustedSitecustomize); verr != nil {
		return "", nil, verr
	}
	return strictPin, pipInstallArgs(strictPin, hashes), nil
}
