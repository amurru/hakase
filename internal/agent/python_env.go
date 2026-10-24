// python_env.go - scrubbed python child environment + skill loader (S20).
//
// runScript used to merge the ambient process env (plus the captured
// delegation parentEnv) and set PYTHONPATH=.:./skills, so a poisoned
// PYTHONPATH or a malicious ./skills/*.py shadowed stdlib on every exec.
// S20 builds the child env from scratch (no ambient inheritance, no
// PYTHONPATH-family keys) and replaces search-path skill discovery with
// an absolute-path loader preamble (ADR conditions 6+7).
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"amurru/hakase/internal/sandbox"
)

// The PYTHONPATH-family scrub list itself lives in the sandbox choke
// (sandbox.ScrubEnv, enforced after every env merge): this file builds
// the python child env from scratch so those keys are never emitted in
// the first place, and the choke scrub is the second layer that drops
// them even when an overlay sets them explicitly.

// pythonEnvAllowlist is the explicit inheritance allowlist (S20 contract):
// a minimal PATH plus temp-dir and locale keys. Nothing else ambient
// reaches the child (in particular no parentEnv merge, no os.Environ
// passthrough, no secrets).
var pythonEnvAllowlist = []string{"TMPDIR", "TEMP", "TMP", "LANG", "LC_ALL"}

// buildPythonChildEnv builds the scrubbed child env for python/pip
// subprocesses from scratch: PATH is minimal (the venv bin dir plus
// system dirs), the allowlist above is copied from the ambient env when
// present, and the PYTHONPATH-family keys are never emitted. The input
// pyBin selects the PATH head; pass the pip binary for pip installs.
func buildPythonChildEnv(pyBin string) []string {
	sep := string(os.PathListSeparator)
	minPath := filepath.Dir(pyBin)
	if runtime.GOOS == "windows" {
		if sysRoot := os.Getenv("SYSTEMROOT"); sysRoot != "" {
			minPath += sep + filepath.Join(sysRoot, "System32")
		} else {
			minPath += sep + `C:\Windows\System32`
		}
	} else {
		minPath += sep + "/usr/bin" + sep + "/bin"
	}
	env := []string{"PATH=" + minPath}
	if runtime.GOOS == "windows" {
		if v, ok := os.LookupEnv("SYSTEMROOT"); ok {
			env = append(env, "SYSTEMROOT="+v)
		}
	}
	for _, k := range pythonEnvAllowlist {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// pythonQuote renders s as a Python single-quoted string literal.
func pythonQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return "'" + s + "'"
}

// pythonLoaderPreamble is the audited loader injected ahead of model code
// (ADR condition 7, ~20 lines): it keeps sys.path[0] as the sandbox script
// dir, drops every search-path shadowing leg ("", ".", ./skills, the
// skills dir, the run cwd) while keeping the stdlib and venv entries, and
// installs a meta-path finder that imports skills by absolute path
// (importlib.util.spec_from_file_location), so `import skills.foo` keeps
// working without any search path. A ./skills/os.py fixture can no longer
// shadow stdlib os: `import os` resolves to the stdlib, never the finder
// (the finder only answers the skills top-level name).
func pythonLoaderPreamble(scriptDir, skillsDir string) string {
	return "import sys as _sys, os as _os, importlib.util as _ilu\n" +
		"_hk_script = " + pythonQuote(scriptDir) + "\n" +
		"_hk_skills = " + pythonQuote(skillsDir) + "\n" +
		"_hk_drop = {'', '.', 'skills', './skills', _hk_skills, _os.getcwd()}\n" +
		"_hk_kept = []\n" +
		"for _hk_p in _sys.path:\n" +
		"    _hk_a = _os.path.abspath(_hk_p or '.')\n" +
		"    if _hk_p not in _hk_drop and _hk_a not in _hk_drop and _hk_a != _hk_skills:\n" +
		"        _hk_kept.append(_hk_p)\n" +
		"_sys.path = [_hk_script] + _hk_kept\n" +
		"class _SkillsFinder:\n" +
		"    def find_spec(self, name, path=None, target=None):\n" +
		"        if name != 'skills' and not name.startswith('skills.'):\n" +
		"            return None\n" +
		"        if name == 'skills':\n" +
		"            p = _os.path.join(_hk_skills, '__init__.py')\n" +
		"        else:\n" +
		"            p = _os.path.join(_hk_skills, name.split('.', 1)[1].split('.')[0] + '.py')\n" +
		"        if not _os.path.isfile(p):\n" +
		"            return None\n" +
		"        return _ilu.spec_from_file_location(name, p)\n" +
		"_sys.meta_path.insert(0, _SkillsFinder())\n"
}

// pythonWorkDir returns the sandbox run directory for python scripts (the
// workspace root when the sandbox pins one, "" for the process cwd),
// mirroring the existing cmd.Dir pinning.
func pythonWorkDir() string {
	if deps != nil && deps.SandboxConfig != nil {
		if root := deps.SandboxConfig.WorkspaceRoot(); root != "" {
			return root
		}
	}
	return ""
}

// skillsDirAbs resolves the ./skills package directory the saver writes
// to, as an absolute path for the loader preamble.
func skillsDirAbs() string {
	base := pythonWorkDir()
	if base == "" {
		if cwd, err := os.Getwd(); err == nil {
			base = cwd
		} else {
			base = "."
		}
	}
	return filepath.Join(base, "skills")
}

// writePythonScript writes the preamble + code to scriptPath (0600).
// writePythonScript writes the preamble + code to scriptPath (0600) via
// the S24 scoped write (O_NOFOLLOW: a planted symlink refuses instead of
// redirecting the model-controlled code).
func writePythonScript(scriptPath, code, scriptDir, skillsDir string) error {
	body := pythonLoaderPreamble(scriptDir, skillsDir) + "\n" + code
	if _, err := sandbox.ScopedWriteFile(scriptPath, []byte(body), 0600, true); err != nil {
		return fmt.Errorf("failed to write script file: %w", err)
	}
	return nil
}
