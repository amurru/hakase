// fileops_test.go - tests for the file operation tools in fileops.go:
// read_file, write_file, patch, and search_files.
package main

import (
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/sandbox"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

// runTool invokes a function tool with the given args through the ADK
// interface and returns the raw output map.
func runTool(t *testing.T, tl tool.Tool, args map[string]any) (map[string]any, error) {
	t.Helper()
	runnable, ok := tl.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("tool %T does not expose Run", tl)
	}
	ctx := agent.NewContext(&agent.ContextMock{})
	return runnable.Run(ctx, args)
}

// unwrapData strips a single <UNTRUSTED_DATA> open/close pair,
// returning the inner content. If the string is not wrapped, it is
// returned unchanged. Empty strings are passed through.
func unwrapData(s string) string {
	if s == "" {
		return s
	}
	prefix := "\n<UNTRUSTED_DATA>\n"
	suffix := "\n</UNTRUSTED_DATA>\n"
	if strings.HasPrefix(s, prefix) && strings.HasSuffix(s, suffix) {
		return s[len(prefix) : len(s)-len(suffix)]
	}
	return s
}

// writeTempFile writes content to <dir>/<name> and returns the absolute path.
func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTempFile: %v", err)
	}
	return path
}

func TestReadFileToolFull(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	path := writeTempFile(t, t.TempDir(), "sample.txt", "line1\nline2\nline3\nline4\nline5\n")

	out, err := runTool(t, tools[0], map[string]any{"path": path})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if got := unwrapData(out["content"].(string)); got != "line1\nline2\nline3\nline4\nline5" {
		t.Errorf("content: expected all lines, got %q", got)
	}
	if got := out["lines"]; got != float64(5) {
		t.Errorf("lines: expected 5, got %v", got)
	}
	if got := out["offset"]; got != float64(1) {
		t.Errorf("offset: expected 1, got %v", got)
	}
	if got := out["truncated"]; got != false {
		t.Errorf("truncated: expected false, got %v", got)
	}
}

func TestReadFileToolRange(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	path := writeTempFile(t, t.TempDir(), "sample.txt", "line1\nline2\nline3\nline4\nline5\n")

	out, err := runTool(t, tools[0], map[string]any{"path": path, "offset": 2, "limit": 2})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if got := unwrapData(out["content"].(string)); got != "line2\nline3" {
		t.Errorf("content: expected lines 2-3, got %q", got)
	}
	if got := out["offset"]; got != float64(2) {
		t.Errorf("offset: expected 2, got %v", got)
	}
	if got := out["truncated"]; got != true {
		t.Errorf("truncated: expected true, got %v", got)
	}

	// Offset past the end yields empty content without error.
	out, err = runTool(t, tools[0], map[string]any{"path": path, "offset": 99})
	if err != nil {
		t.Fatalf("read_file (past end): %v", err)
	}
	if got := out["content"]; got != "" {
		t.Errorf("content: expected empty past end, got %q", got)
	}
}

func TestReadFileToolMissing(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	_, err = runTool(t, tools[0], map[string]any{"path": filepath.Join(t.TempDir(), "nope.txt")})
	if err == nil {
		t.Fatal("read_file: expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "failed to read file") {
		t.Errorf("read_file: expected error mentioning read failure, got %v", err)
	}
}

func TestReadFileToolAttachesSubdirContext(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}

	// Workspace: git root with root AGENTS.md (in the prompt) and a subdir
	// AGENTS.md (hinted on read).
	root := makeGitDir(t, t.TempDir())
	sub := filepath.Join(root, "pkg", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	subAgents := filepath.Join(sub, "AGENTS.md")
	writeContextFile(t, filepath.Join(root, "AGENTS.md"), "# root rules\n")
	writeContextFile(t, subAgents, "# sub rules\n")
	hctx.InitContextState(root, &config.Config{}, []hctx.InstructionFile{{Path: filepath.Join(root, "AGENTS.md"), Content: "# root rules\n"}})

	file := writeTempFile(t, sub, "x.go", "package sub\n")

	out, err := runTool(t, tools[0], map[string]any{"path": file})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	ctxHint, _ := out["context"].(string)
	if !strings.Contains(ctxHint, "### SUBDIRECTORY CONTEXT:") ||
		!strings.Contains(ctxHint, "# sub rules") {
		t.Errorf("read_file should attach the subdir AGENTS.md hint, got %q", ctxHint)
	}

	// A second read does not re-attach (session dedup).
	out, err = runTool(t, tools[0], map[string]any{"path": file})
	if err != nil {
		t.Fatalf("read_file (second): %v", err)
	}
	if ctxHint, _ := out["context"].(string); ctxHint != "" {
		t.Errorf("expected dedup on second read, got %q", ctxHint)
	}
}

func TestSearchFilesToolAttachesSubdirContext(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}

	root := makeGitDir(t, t.TempDir())
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	writeContextFile(t, filepath.Join(root, "AGENTS.md"), "# root rules\n")
	writeContextFile(t, filepath.Join(sub, "AGENTS.md"), "# pkg rules\n")
	writeTempFile(t, sub, "x.go", "package pkg\n// TODO marker\n")
	hctx.InitContextState(root, &config.Config{}, []hctx.InstructionFile{{Path: filepath.Join(root, "AGENTS.md"), Content: "# root rules\n"}})

	out, err := runTool(t, tools[3], map[string]any{"pattern": "TODO", "path": sub})
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}
	ctxHint, _ := out["context"].(string)
	if !strings.Contains(ctxHint, "# pkg rules") {
		t.Errorf("search_files should attach the search-root AGENTS.md hint, got %q", ctxHint)
	}
}

func TestWriteFileToolCreates(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "nested", "new.txt")

	out, err := runTool(t, tools[1], map[string]any{"path": path, "content": "hello world"})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if got := out["created"]; got != true {
		t.Errorf("created: expected true, got %v", got)
	}
	if got := out["bytes_written"]; got != float64(11) {
		t.Errorf("bytes_written: expected 11, got %v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("file content: expected %q, got %q", "hello world", string(data))
	}
}

func TestWriteFileToolOverwriteGuard(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	path := writeTempFile(t, t.TempDir(), "existing.txt", "original")

	// Without overwrite the write must fail.
	if _, err := runTool(t, tools[1], map[string]any{"path": path, "content": "replacement"}); err == nil {
		t.Fatal("write_file: expected error when overwriting without overwrite=true")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Errorf("file must be unchanged, got %q", string(data))
	}

	// With overwrite=true it must succeed and report created=false.
	out, err := runTool(t, tools[1], map[string]any{"path": path, "content": "replacement", "overwrite": true})
	if err != nil {
		t.Fatalf("write_file (overwrite): %v", err)
	}
	if got := out["created"]; got != false {
		t.Errorf("created: expected false on overwrite, got %v", got)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "replacement" {
		t.Errorf("file content: expected %q, got %q", "replacement", string(data))
	}
}

func TestPatchToolReplaceFirst(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	path := writeTempFile(t, t.TempDir(), "patch.txt", "foo bar foo baz")

	out, err := runTool(t, tools[2], map[string]any{"path": path, "old_string": "foo", "new_string": "qux"})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if got := out["replaced"]; got != float64(1) {
		t.Errorf("replaced: expected 1, got %v", got)
	}
	if got := out["matches"]; got != float64(2) {
		t.Errorf("matches: expected 2, got %v", got)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "qux bar foo baz" {
		t.Errorf("file content: expected %q, got %q", "qux bar foo baz", string(data))
	}
}

func TestPatchToolReplaceAll(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	path := writeTempFile(t, t.TempDir(), "patch.txt", "foo bar foo baz")

	out, err := runTool(t, tools[2], map[string]any{"path": path, "old_string": "foo", "new_string": "qux", "replace_all": true})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if got := out["replaced"]; got != float64(2) {
		t.Errorf("replaced: expected 2, got %v", got)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "qux bar qux baz" {
		t.Errorf("file content: expected %q, got %q", "qux bar qux baz", string(data))
	}
}

func TestPatchToolNotFound(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	path := writeTempFile(t, t.TempDir(), "patch.txt", "hello world")

	_, err = runTool(t, tools[2], map[string]any{"path": path, "old_string": "nope", "new_string": "x"})
	if err == nil {
		t.Fatal("patch: expected error when old_string is absent")
	}
	if !strings.Contains(err.Error(), "old_string not found") {
		t.Errorf("patch: expected 'old_string not found' error, got %v", err)
	}
}

// buildSearchSandbox creates a temp directory with known files for search tests.
func buildSearchSandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTempFile(t, dir, "a.go", "func main() {\n\tprintln(\"hello\")\n}\n")
	writeTempFile(t, dir, "b.py", "def main():\n    pass\n")
	writeTempFile(t, dir, "c.txt", "no matches here\n")
	// Binary file that must be skipped.
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{0x00, 0x01, 0x02, 0x03}, 0o644); err != nil {
		t.Fatalf("buildSearchSandbox: %v", err)
	}
	return dir
}

func TestSearchFilesToolContent(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	dir := buildSearchSandbox(t)

	out, err := runTool(t, tools[3], map[string]any{"pattern": "main", "path": dir})
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}
	matches, ok := out["matches"].([]any)
	if !ok {
		t.Fatalf("matches: expected []any, got %T", out["matches"])
	}
	if len(matches) != 2 {
		t.Fatalf("matches: expected 2 results, got %d (%v)", len(matches), matches)
	}
	first := matches[0].(map[string]any)
	if !strings.HasSuffix(first["path"].(string), "a.go") {
		t.Errorf("first match: expected a.go, got %v", first["path"])
	}
	if first["line"] != float64(1) {
		t.Errorf("first match line: expected 1, got %v", first["line"])
	}
	if !strings.Contains(first["content"].(string), "func main()") {
		t.Errorf("first match content: expected main line, got %q", first["content"])
	}
	if got := out["total"]; got != float64(2) {
		t.Errorf("total: expected 2, got %v", got)
	}
}

func TestSearchFilesToolFilesWithMatches(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	dir := buildSearchSandbox(t)

	out, err := runTool(t, tools[3], map[string]any{"pattern": "main", "path": dir, "output_mode": "files_with_matches"})
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}
	matches, ok := out["matches"].([]any)
	if !ok {
		t.Fatalf("matches: expected []any, got %T", out["matches"])
	}
	if len(matches) != 2 {
		t.Fatalf("matches: expected 2 unique files, got %d", len(matches))
	}
}

func TestSearchFilesToolCount(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	dir := t.TempDir()
	writeTempFile(t, dir, "a.go", "main main main\n")
	writeTempFile(t, dir, "b.py", "no match\n")

	out, err := runTool(t, tools[3], map[string]any{"pattern": "main", "path": dir, "output_mode": "count"})
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}
	matches, ok := out["matches"].([]any)
	if !ok {
		t.Fatalf("matches: expected []any, got %T", out["matches"])
	}
	if len(matches) != 1 {
		t.Fatalf("matches: expected 1 file with matches, got %d", len(matches))
	}
	first := matches[0].(map[string]any)
	if got := first["count"]; got != float64(3) {
		t.Errorf("count: expected 3, got %v", got)
	}
}

func TestSearchFilesToolIncludeFilter(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	dir := buildSearchSandbox(t)

	out, err := runTool(t, tools[3], map[string]any{"pattern": "main", "path": dir, "include": []any{"*.py"}})
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}
	matches, ok := out["matches"].([]any)
	if !ok {
		t.Fatalf("matches: expected []any, got %T", out["matches"])
	}
	if len(matches) != 1 {
		t.Fatalf("matches: expected 1 result for *.py filter, got %d", len(matches))
	}
	first := matches[0].(map[string]any)
	if !strings.HasSuffix(first["path"].(string), "b.py") {
		t.Errorf("first match: expected b.py, got %v", first["path"])
	}
}

func TestSearchFilesToolHeadLimit(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeTempFile(t, dir, fmt.Sprintf("f%d.go", i), "func main() {}\n")
	}

	out, err := runTool(t, tools[3], map[string]any{"pattern": "main", "path": dir, "head_limit": 2})
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}
	matches, ok := out["matches"].([]any)
	if !ok {
		t.Fatalf("matches: expected []any, got %T", out["matches"])
	}
	if len(matches) != 2 {
		t.Fatalf("matches: expected 2 results with head_limit=2, got %d", len(matches))
	}
	if got := out["truncated"]; got != true {
		t.Errorf("truncated: expected true, got %v", got)
	}
}

func TestSearchFilesToolErrors(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	dir := buildSearchSandbox(t)

	// Invalid regex.
	if _, err := runTool(t, tools[3], map[string]any{"pattern": "(", "path": dir}); err == nil {
		t.Error("search_files: expected error for invalid pattern")
	}
	// Search root that is a file, not a directory.
	filePath := filepath.Join(dir, "a.go")
	if _, err := runTool(t, tools[3], map[string]any{"pattern": "main", "path": filePath}); err == nil {
		t.Error("search_files: expected error for non-directory search root")
	}
	// Invalid output mode.
	if _, err := runTool(t, tools[3], map[string]any{"pattern": "main", "path": dir, "output_mode": "bogus"}); err == nil {
		t.Error("search_files: expected error for invalid output_mode")
	}
}

func TestFileOpsToolsetNames(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	want := []string{"read_file", "write_file", "patch", "search_files"}
	if len(tools) != len(want) {
		t.Fatalf("expected %d tools, got %d", len(want), len(tools))
	}
	for i, w := range want {
		if got := tools[i].Name(); got != w {
			t.Errorf("tool[%d]: expected name %q, got %q", i, w, got)
		}
	}
}

// TestSearchFilesDefaultHeadLimit verifies that when head_limit is 0/unset,
// the handler defaults to 100 and marks the result Truncated.
func TestSearchFilesDefaultHeadLimit(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	dir := t.TempDir()
	// Create 150 matching files - more than the default head_limit of 100.
	for i := 0; i < 150; i++ {
		writeTempFile(t, dir, fmt.Sprintf("f%03d.go", i), "func main() {}\n")
	}

	out, err := runTool(t, tools[3], map[string]any{"pattern": "main", "path": dir})
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}
	matches, ok := out["matches"].([]any)
	if !ok {
		t.Fatalf("matches: expected []any, got %T", out["matches"])
	}
	if len(matches) > 100 {
		t.Errorf("matches: expected at most 100 (default head_limit), got %d", len(matches))
	}
	if got := out["truncated"]; got != true {
		t.Errorf("truncated: expected true, got %v", got)
	}
}

// TestSearchFilesTimeout verifies that a tiny sandbox.SearchTimeout causes the walk
// to stop gracefully, returning partial results with Truncated=true and no
// error, even when the tree has many files.
func TestSearchFilesTimeout(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}
	dir := t.TempDir()
	// Create ~3000 small files so the walk takes long enough to hit a
	// 1ms deadline. Each file matches the pattern.
	for i := 0; i < 3000; i++ {
		writeTempFile(t, dir, fmt.Sprintf("f%04d.go", i), "func main() {}\n")
	}

	// Inject a tiny deadline.
	origTimeout := sandbox.SearchTimeout
	sandbox.SearchTimeout = 1 * time.Millisecond
	t.Cleanup(func() { sandbox.SearchTimeout = origTimeout })

	out, err := runTool(t, tools[3], map[string]any{"pattern": "main", "path": dir})
	if err != nil {
		t.Fatalf("search_files: expected no error on timeout, got %v", err)
	}
	if got := out["truncated"]; got != true {
		t.Errorf("truncated: expected true on timeout, got %v", got)
	}
	// matches may be nil or []any depending on how many files were
	// processed before the deadline; both are valid partial results.
	if matches, ok := out["matches"].([]any); ok {
		if len(matches) > 3000 {
			t.Errorf("matches: expected partial results, got %d (full set)", len(matches))
		}
	}
	// If matches is nil (deadline fired before any file was visited),
	// that's still a valid graceful partial result.
}

// TestSearchFilesSandboxConfinement verifies that when sandbox.CurrentSandbox is
// active with mode "paths", write_file to a path inside the workspace root
// succeeds, write_file to /etc/passwd fails with "outside approved
// workspace", and read_file of a file inside the root succeeds.
func TestSearchFilesSandboxConfinement(t *testing.T) {
	tools, err := sandbox.CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("sandbox.CreateFileOpsTools: %v", err)
	}

	// Activate sandbox confinement with a temp dir as the workspace root.
	root := t.TempDir()
	origSandbox := sandbox.CurrentSandbox
	sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(&sandbox.SandboxJSON{Mode: "paths", WorkspaceRoots: []string{root}})
	t.Cleanup(func() { sandbox.CurrentSandbox = origSandbox })

	// write_file inside the root succeeds.
	innerPath := filepath.Join(root, "hello.txt")
	out, err := runTool(t, tools[1], map[string]any{"path": innerPath, "content": "hello"})
	if err != nil {
		t.Fatalf("write_file inside root: expected success, got %v", err)
	}
	if got := out["created"]; got != true {
		t.Errorf("created: expected true, got %v", got)
	}

	// write_file to /etc/passwd fails with "outside approved workspace".
	_, err = runTool(t, tools[1], map[string]any{"path": "/etc/passwd", "content": "evil"})
	if err == nil {
		t.Fatal("write_file to /etc/passwd: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "outside approved workspace") {
		t.Errorf("write_file to /etc/passwd: expected error mentioning 'outside approved workspace', got %v", err)
	}

	// read_file of a file inside the root succeeds.
	out, err = runTool(t, tools[0], map[string]any{"path": innerPath})
	if err != nil {
		t.Fatalf("read_file inside root: expected success, got %v", err)
	}
	if got := unwrapData(out["content"].(string)); got != "hello" {
		t.Errorf("content: expected %q, got %v", "hello", got)
	}
}
