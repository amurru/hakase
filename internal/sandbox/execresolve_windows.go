//go:build windows

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Windows exec-resolution hardening (WIN-003):
//
//  1. cmd.exe (and the CreateProcess family) resolves bare executable names
//     from the current directory before PATH, so a workspace-planted
//     git.exe/python.exe/curl.bat would execute instead of the system binary.
//     NoDefaultCurrentDirectoryInExePath=1 removes the current directory from
//     that search; it is set process-wide at init so hakase's own
//     exec.LookPath (Go >= 1.19 honors the variable on Windows) and every
//     child we spawn inherit the PATH-only resolution rule.
//  2. The explicit (command, args...) form re-resolves bare command names
//     against PATH (excluding the working directory) and rewrites them to the
//     absolute path before exec. A resolution that lands inside a workspace
//     root - or a bare name that exists only as a planted file in the working
//     directory - is rejected: a workspace file is untrusted content, not a
//     program.

func init() {
	// Process-wide PATH-only executable resolution. Children inherit the
	// variable via os.Environ(); Go's own LookPath honors it too.
	_ = os.Setenv("NoDefaultCurrentDirectoryInExePath", "1")
}

// fixupWindowsChildEnv ensures env carries NoDefaultCurrentDirectoryInExePath=1
// (case-insensitive check - Windows environment names are case-insensitive).
// Applied to every Windows spawn in BuildExecCommand as defense-in-depth for
// environments assembled from non-os.Environ sources.
func fixupWindowsChildEnv(env []string) []string {
	const key = "NoDefaultCurrentDirectoryInExePath"
	for i, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, key) {
			env[i] = key + "=1"
			return env
		}
	}
	return append(env, key+"=1")
}

// cmdBuiltins are cmd.exe internal commands. The shell resolves these before
// touching the filesystem, so a planted same-named executable cannot hijack
// them as the first token of a string command and no PATH rewrite applies.
var cmdBuiltins = map[string]bool{
	"assoc": true, "break": true, "call": true, "cd": true, "chdir": true,
	"cls": true, "color": true, "copy": true, "date": true, "del": true,
	"dir": true, "dpath": true, "echo": true, "endlocal": true, "erase": true,
	"exit": true, "for": true, "ftype": true, "goto": true, "if": true,
	"md": true, "mkdir": true, "mklink": true, "move": true, "path": true,
	"pause": true, "popd": true, "prompt": true, "pushd": true, "rd": true,
	"rem": true, "ren": true, "rename": true, "rmdir": true, "set": true,
	"setlocal": true, "shift": true, "start": true, "time": true,
	"title": true, "type": true, "ver": true, "verify": true, "vol": true,
}

// firstBareToken returns the index and text of the first bare (non-flag,
// non-assignment, non-path) whitespace-delimited token of a cmd command
// line, or -1. Tokens like FOO=bar (env-style prefix), -x / /x (flags),
// quoted paths, and glob/variable metacharacters disqualify the scan.
func firstBareToken(command string) (idx int, tok string) {
	for idx, tok = range SplitCommandTokens(command) {
		if tok == "" {
			continue
		}
		// Metacharacters: not a plain executable position.
		if strings.ContainsAny(tok, `"%*?|&<>()^`) {
			return -1, ""
		}
		// Flags (-x, /x): not a command token.
		if strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "/") {
			return -1, ""
		}
		// FOO=bar env-style assignment prefix: keep scanning.
		if strings.Contains(tok, "=") {
			continue
		}
		// Explicit path form: no cwd lookup happens, no rewrite needed.
		if strings.ContainsAny(tok, `/\`) {
			return -1, ""
		}
		// Bare name (optionally with an extension like foo.bat); both are
		// cwd-resolvable by cmd and need the plant check.
		return idx, tok
	}
	return -1, ""
}

// pathContains reports whether p is contained under root, case-insensitively
// (Windows paths). Both inputs must be absolute.
func pathContains(root, p string) bool {
	a, err1 := filepath.Abs(root)
	b, err2 := filepath.Abs(p)
	if err1 != nil || err2 != nil {
		return false
	}
	a = filepath.Clean(strings.TrimPrefix(a, `\\?\`))
	b = filepath.Clean(strings.TrimPrefix(b, `\\?\`))
	if a == b {
		return true
	}
	rel, err := filepath.Rel(a, b)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// rejectIfWorkspaceExecutable rejects a resolved executable path that lives
// inside any workspace root: workspace files are untrusted content, not
// programs.
func rejectIfWorkspaceExecutable(resolved string, workspaceRoots []string) error {
	for _, root := range workspaceRoots {
		if root == "" {
			continue
		}
		if pathContains(root, resolved) {
			return fmt.Errorf(
				"refusing to execute %q: it resolves inside a workspace root (%s); workspace files are data, not programs - install the tool on PATH or use its absolute system path",
				resolved, root)
		}
	}
	return nil
}

// pathextExtensions returns the executable extensions cmd/CreateProcess use
// when resolving bare names (PATHEXT, defaulting to .COM/.EXE/.BAT/.CMD).
func pathextExtensions() []string {
	px := os.Getenv("PATHEXT")
	if strings.TrimSpace(px) == "" {
		px = ".COM;.EXE;.BAT;.CMD"
	}
	var out []string
	for _, e := range strings.Split(px, ";") {
		e = strings.TrimSpace(e)
		if e != "" && strings.HasPrefix(e, ".") {
			out = append(out, e)
		}
	}
	return out
}

// resolveBareExecutable resolves a bare executable name against PATH only
// (the current/working directory is excluded). Returns an error when the name
// exists as a file in cmdDir but not on PATH (a planted executable), or when
// the PATH resolution lands inside a workspace root. The returned string is
// the absolute executable path when resolution succeeded, or "" when the name
// could not be resolved (the caller may fall back to letting the shell try).
func resolveBareExecutable(name string, cmdDir string, workspaceRoots []string) (string, error) {
	// A same-named file (with or without a PATHEXT extension) sitting in
	// the effective working directory is exactly the plant we refuse to
	// run: PATH resolution will not find it (cwd excluded) and letting cmd
	// fall through would execute it.
	if cmdDir != "" && plantedInDir(cmdDir, name) {
		return "", fmt.Errorf(
			"refusing to execute %q: a file with that name exists in the working directory (%s) but not on PATH; workspace files are data, not programs - use an absolute path or an explicit interpreter",
			name, cmdDir)
	}
	resolved, err := exec.LookPath(name)
	if err != nil {
		// Not on PATH at all: leave it to cmd (it may be an internal
		// command variant or a PATHEXT extension handled by association).
		return "", nil
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		abs = resolved
	}
	if err := rejectIfWorkspaceExecutable(abs, workspaceRoots); err != nil {
		return "", err
	}
	return abs, nil
}

// plantedInDir reports whether dir contains a file that would execute under
// the given bare name (the name itself plus its PATHEXT-extended forms).
func plantedInDir(dir, name string) bool {
	candidates := []string{name}
	for _, ext := range pathextExtensions() {
		candidates = append(candidates, name+ext)
	}
	for _, c := range candidates {
		if fi, err := os.Stat(filepath.Join(dir, c)); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// hardenWindowsShellCommand rewrites the first bare token of a string-form
// command to its absolute PATH-resolved path (belt-and-braces fallback for
// cmd.exe's cwd lookup) and rejects workspace-planted executables. The rest
// of the command line is preserved verbatim so cmd /D /C sees the original
// arguments.
func hardenWindowsShellCommand(command, cmdDir string, workspaceRoots []string) (string, error) {
	idx, tok := firstBareToken(command)
	if idx < 0 || tok == "" {
		return command, nil
	}
	if cmdBuiltins[strings.ToLower(strings.TrimSuffix(tok, filepath.Ext(tok)))] {
		return command, nil
	}
	// Tokens that are really data files with extensions handled by shell
	// association (script.py, doc.bat) are executable from cmd too; the
	// resolveBareExecutable cwd-plant check applies to them as well.
	resolved, err := resolveBareExecutable(tok, cmdDir, workspaceRoots)
	if err != nil {
		return "", err
	}
	if resolved == "" {
		return command, nil
	}
	// Replace the first occurrence of the token in the command line.
	pos := strings.Index(command, tok)
	if pos < 0 {
		return command, nil
	}
	return command[:pos] + resolved + command[pos+len(tok):], nil
}

// resolveExplicitWindowsCommand resolves the explicit (command, args...) form:
// a bare command name is rewritten to its absolute PATH-resolved path (cwd
// excluded); a bare name that exists only as a planted file in cmdDir, or
// whose PATH resolution lands inside a workspace root, is rejected. Explicit
// path-form commands pass through unchanged - they are deliberate choices,
// audited like any other workspace path operand.
func resolveExplicitWindowsCommand(command, cmdDir string, workspaceRoots []string) (string, error) {
	if strings.ContainsAny(command, `/\`) {
		return command, nil
	}
	resolved, err := resolveBareExecutable(command, cmdDir, workspaceRoots)
	if err != nil {
		return "", err
	}
	if resolved == "" {
		// Unresolvable: preserve the lazy "not found in %PATH%" Start error.
		return command, nil
	}
	return resolved, nil
}
