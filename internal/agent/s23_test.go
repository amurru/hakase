// s23_test.go - single-choke regression tests (S23, lands last).
package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"amurru/hakase/internal/sandbox"
)

// agentPkgDir returns the directory of this package's non-test sources.
func agentPkgDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

// s23AgentChokeFiles is the choke-only allowlist for internal/agent: raw
// spawn literals may appear only here (the two-layer choke: python run +
// venv create stay raw with the scrubbed S20 env; pip + system_exec go
// through the shared sandbox.BuildExecCommand choke).
var s23AgentChokeFiles = map[string]bool{"exec_choke.go": true}

// s23SandboxChokeFiles is the constructor allowlist for internal/sandbox:
// the shared choke owns its construction sites (shell/direct forms per
// platform plus the bwrap wrapper). Every site carries a
// "// choke-allow:" justification, enforced below.
var s23SandboxChokeFiles = map[string]bool{
	"exec_unix.go": true, "exec_windows.go": true, "sandboxexec.go": true,
}

// isSpawnNode reports whether n constructs a process-spawn primitive:
// an exec.Cmd composite literal, an exec.Command/CommandContext call,
// or a syscall.StartProcess/ForkExec call.
func isSpawnNode(n ast.Node) bool {
	switch v := n.(type) {
	case *ast.CompositeLit:
		if sel, ok := v.Type.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "exec" && sel.Sel.Name == "Cmd" {
				return true
			}
		}
	case *ast.CallExpr:
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				if id.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
					return true
				}
				if id.Name == "syscall" && (sel.Sel.Name == "StartProcess" || sel.Sel.Name == "ForkExec") {
					return true
				}
			}
		}
	}
	return false
}

// scanSpawnSites AST-scans dir's non-test .go files for spawn
// constructions. Hits outside allowed fail; hits inside allowed need a
// "// choke-allow:" comment on the same or immediately preceding line.
// It returns the violations and the annotated choke-site count.
func scanSpawnSites(t *testing.T, dir string, allowed map[string]bool) ([]string, int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	var violations []string
	chokeSites := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		allowLines := map[int]bool{}
		for _, cg := range f.Comments {
			if !strings.Contains(cg.Text(), "choke-allow:") {
				continue
			}
			for _, c := range cg.List {
				pos := fset.Position(c.Pos())
				end := fset.Position(c.End())
				for ln := pos.Line; ln <= end.Line; ln++ {
					allowLines[ln] = true
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil || !isSpawnNode(n) {
				return true
			}
			ln := fset.Position(n.Pos()).Line
			if !allowed[name] {
				violations = append(violations, name+":"+itoa(ln)+": raw spawn outside the choke")
				return true
			}
			if !allowLines[ln] && !allowLines[ln-1] {
				violations = append(violations, name+":"+itoa(ln)+": choke site lacks a // choke-allow: justification")
				return true
			}
			chokeSites++
			return true
		})
	}
	return violations, chokeSites
}

// TestS23SingleChoke is the reintroduction regression: no non-test file in
// internal/agent outside exec_choke.go may construct process-spawn
// primitives (AST: exec.Cmd literals, exec.Command/CommandContext calls,
// syscall.StartProcess/ForkExec), and the sandbox constructors are pinned
// to their allowlisted files with the same annotation contract. Test-only
// raw exec in _test.go is excluded by construction (test files are not
// scanned).
func TestS23SingleChoke(t *testing.T) {
	agentDir := agentPkgDir(t)
	violations, chokeSites := scanSpawnSites(t, agentDir, s23AgentChokeFiles)
	if chokeSites < 2 {
		t.Errorf("choke must own at least the python and venv spawn sites, found %d", chokeSites)
	}
	sandboxDir := filepath.Join(filepath.Dir(agentDir), "sandbox")
	sandboxViolations, sandboxSites := scanSpawnSites(t, sandboxDir, s23SandboxChokeFiles)
	violations = append(violations, sandboxViolations...)
	if sandboxSites < 3 {
		t.Errorf("sandbox choke must own its shell/direct/bwrap constructors, found %d", sandboxSites)
	}
	if len(violations) > 0 {
		t.Fatalf("raw spawn outside the choke:\n%s", strings.Join(violations, "\n"))
	}
}

// TestS23ChokeInventory pins the post-S23 table: every agent tool spawn
// resolves to a choke entry point (stub-injectable), and the canonical
// identities bind the expected choke keys.
func TestS23ChokeInventory(t *testing.T) {
	rows := []struct {
		tool string
		ok   bool
	}{
		{"python_interpreter run", execPythonScript != nil},
		{"pip install", pipChokeBuild != nil},
		{"venv create", execCreateVenv != nil},
		{"choke wait/run", startWaitChokeCmd != nil},
		{"pip pre-approval option", sandbox.ExecPreApproved("test") != nil},
		{"pip profile option", sandbox.ExecPipInstall() != nil},
		{"replace-env option", sandbox.ExecReplaceEnv(nil) != nil},
	}
	for _, r := range rows {
		if !r.ok {
			t.Errorf("choke inventory: %s has no wired choke entry point", r.tool)
		}
	}
	if canonicalFlowTool("system_exec_start") != "system_exec" {
		t.Error("inventory: system_exec_start must resolve to the BuildExecCommand choke identity")
	}
	if canonicalFlowTool("grep") != "search_files" {
		t.Error("inventory: grep must resolve to the search_files choke identity")
	}
	if canonicalGrantTool("system_exec_start") != "system_exec" {
		t.Error("inventory: grant tool must canonicalize to system_exec")
	}
}
