// s29_test.go - S29 lane scan + inventory (lands last).
//
// S29 fails the lane on reintroduction: no raw model-controlled file
// write may bypass the S24 scoped choke, no new label-construction site
// may bypass Meet-only discipline, and every S24-S29 entry point must be
// wired (inventory table in docs/flow-guard/s24-s29-inventory.md).
package agent

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
)

// s29RawWriteFiles is the S24 choke allowlist: the scoped constructor
// itself may open files; every other raw os.WriteFile/os.Create/
// os.OpenFile/os.CreateTemp site in the scanned dirs needs a
// "// s24-allow:" justification on the same or preceding line.
var s29RawWriteAllowFiles = map[string]bool{"scopedfile.go": true}

// s29LabelAllowFiles pins label-construction sites (Meet-only): new
// SessionLabel{...} literals may appear only where the lane sanctions
// them (delegate seeding, fail-closed narrowest). Everything else must
// combine labels via permissions.Meet.
var s29LabelAllowFiles = map[string]bool{
	"flowgate.go": true, // S4 delegate seeding + fail-closed taint check
	"delegate.go": true, // pre-existing cache-put zero value (out of lane)
}

// s24AllowLines maps the comment justification lines in one file.
func s24AllowLines(t *testing.T, f *ast.File, fset *token.FileSet) map[int]bool {
	t.Helper()
	allow := map[int]bool{}
	for _, cg := range f.Comments {
		if !strings.Contains(cg.Text(), "s24-allow:") {
			continue
		}
		for _, c := range cg.List {
			pos := fset.Position(c.Pos())
			end := fset.Position(c.End())
			for ln := pos.Line; ln <= end.Line; ln++ {
				allow[ln] = true
			}
		}
	}
	return allow
}

// isRawWriteCall reports os.WriteFile/os.Create/os.OpenFile/
// os.CreateTemp call expressions (the S24 scan surface).
func isRawWriteCall(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "os" {
		return false
	}
	switch sel.Sel.Name {
	case "WriteFile", "Create", "OpenFile", "CreateTemp":
		return true
	}
	return false
}

// scanRawWrites AST-scans dir's non-test .go files for raw write calls
// outside the choke allowlist or without a // s24-allow: justification.
func scanRawWrites(t *testing.T, dir string) []string {
	t.Helper()
	var violations []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if s29RawWriteAllowFiles[name] {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		allow := s24AllowLines(t, f, fset)
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil || !isRawWriteCall(n) {
				return true
			}
			ln := fset.Position(n.Pos()).Line
			if !allow[ln] && !allow[ln-1] {
				violations = append(violations, name+":"+itoa(ln)+": raw file write outside the S24 scoped choke")
			}
			return true
		})
	}
	return violations
}

// scanLabelLiterals scans dir's non-test .go files for SessionLabel{...}
// composite literals outside the sanctioned files.
func scanLabelLiterals(t *testing.T, dir string) []string {
	t.Helper()
	var violations []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if s29LabelAllowFiles[name] {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "SessionLabel{") {
				violations = append(violations, name+":"+itoa(i+1)+": label construction outside sanctioned sites (combine via permissions.Meet)")
			}
		}
	}
	return violations
}

// TestS29ScanInventory is the reintroduction regression: no non-test file
// in internal/agent or internal/sandbox may construct a raw file write
// outside the S24 scoped choke (or without a // s24-allow:
// justification), and no file outside the sanctioned set may construct
// SessionLabel literals (Meet-only).
func TestS29ScanInventory(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	agentDir := filepath.Dir(file)
	sandboxDir := filepath.Join(filepath.Dir(agentDir), "sandbox")
	var violations []string
	violations = append(violations, scanRawWrites(t, agentDir)...)
	violations = append(violations, scanRawWrites(t, sandboxDir)...)
	violations = append(violations, scanLabelLiterals(t, agentDir)...)
	violations = append(violations, scanLabelLiterals(t, sandboxDir)...)
	if len(violations) > 0 {
		t.Fatalf("S29 scan violations:\n%s", strings.Join(violations, "\n"))
	}
}

// TestS29ChokeInventory pins the post-lane table: every S24-S29 tool path
// resolves to its choke entry point (functional round-trips, not just
// non-nil handles).
func TestS29ChokeInventory(t *testing.T) {
	rows := []struct {
		tool string
		ok   bool
	}{
		{"S24 scoped write", func() bool {
			dir := t.TempDir()
			_, err := sandbox.ScopedWriteFileInRoot(dir, "inv.txt", []byte("x"), 0644, false)
			return err == nil
		}()},
		{"S24 scoped stream create", func() bool {
			dir := t.TempDir()
			f, _, err := sandbox.ScopedCreateFile("", filepath.Join(dir, "s.bin"), 0644)
			if err != nil {
				return false
			}
			_ = f.Close()
			return true
		}()},
		{"S25 joint grant dual consume", func() bool {
			b := flowAskBinding{sessionID: "s29", tool: "read", argsHash: "h", labelVersion: 0}
			grantFlowCall(b)
			grantFlowCallV2(b)
			return consumeFlowGrantDual(b) && !consumeFlowGrantDual(b)
		}()},
		{"S25 V1-DELTA register", func() bool {
			permissions.RegisterV1Delta("s29-probe")
			defer permissions.UnregisterV1Delta("s29-probe")
			return permissions.IsV1DeltaRegistered("s29-probe") && !permissions.IsV1DeltaRegistered("s29-other")
		}()},
		{"S26 tag neutralize", func() bool {
			out, changed := neutralizeFlowTags("<b>x</b>")
			return changed && !strings.ContainsAny(out, "<>")
		}()},
		{"S27 pin parse", func() bool {
			_, _, hasPin, err := splitPipPin("requests==2.31.0")
			return err == nil && hasPin
		}()},
		{"S27 venv integrity fail-closed", func() bool {
			return verifyVenvIntegrity(t.TempDir()+"/missing", nil) != nil
		}()},
		{"S28 confinement wired", func() bool {
			return strings.Contains(markdownSkillsInstruction, "deny-first")
		}()},
		{"S24 confined root fallback non-empty", func() bool {
			dir := t.TempDir()
			sb := &sandbox.SandboxConfig{Mode: sandbox.SandboxModePaths, WorkspaceRoots: []string{dir}}
			ctx := sandbox.WithConfig(context.Background(), sb)
			got := sandbox.EffectiveWriteRoot(ctx, "")
			if got == "" {
				return false
			}
			// Explicit per-task root still wins over the ctx fallback.
			if sandbox.EffectiveWriteRoot(ctx, dir) != dir {
				return false
			}
			return true
		}()},
	}
	for _, r := range rows {
		if !r.ok {
			t.Errorf("choke inventory: %s has no wired entry point", r.tool)
		}
	}
}

// TestS29MeetOnlyLabels pins the combination discipline directly: Meet is
// narrowing-only (monotone descent on trust, ceiling-min on audience),
// so any future combiner must go through it - spot-checked here against
// the constructor the scan pins.
func TestS29MeetOnlyLabels(t *testing.T) {
	start := permissions.StartLabel()
	narrowed, err := permissions.FoldLabel(start, permissions.FlowDelta{Trust: "suspicious"})
	if err != nil {
		t.Fatalf("FoldLabel: %v", err)
	}
	merged := permissions.Meet(start, narrowed)
	if merged.Trust != narrowed.Trust || merged.Audience != narrowed.Audience {
		t.Fatalf("Meet must yield the narrowed label, got %+v want %+v", merged, narrowed)
	}
	if merged.Version != narrowed.Version+1 {
		t.Fatalf("Meet bumps the version on a narrowing combine, got %d want %d", merged.Version, narrowed.Version+1)
	}
}
