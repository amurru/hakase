// fileops.go - file operation tools for the general-purpose agent:
// read_file, write_file, patch (targeted edits), and search_files.
//
// Workspace confinement: when the package-level currentSandbox is non-nil
// and its Mode is not SandboxModeOff, all path resolution goes through
// (*SandboxConfig).resolveScopedPath, which confines reads to read roots
// and writes to workspace roots (deny roots always rejected). When
// currentSandbox is nil (the default), paths resolve via the legacy
// resolveTaskPath/resolvePath logic, preserving backward compatibility.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// maxSearchFileSize caps how large a file search_files will scan (5 MB).
// Larger files are skipped to keep the walk fast and bounded.
const maxSearchFileSize = 5 << 20

// maxSearchEntries caps the total number of directory + file entries
// search_files will visit in a single walk. When exhausted, the walk
// stops and the result is marked Truncated. This bounds the worst-case
// scan over huge or pathological directory trees (e.g. mounted drives
// throwing I/O errors).
const maxSearchEntries = 50000

// searchTimeout is the per-call wall-clock deadline for search_files.
// On expiry the walk stops gracefully and partial matches are returned
// with Truncated: true (no error). It is a package var so tests can
// inject a tiny deadline.
var searchTimeout = 30 * time.Second

// ReadFileInput is the input schema for the read_file tool.
type ReadFileInput struct {
	Path   string `json:"path"             doc:"Path to the file to read (absolute or relative to the working directory)"`
	Offset int    `json:"offset,omitempty" doc:"1-based line number to start reading from (defaults to 1)"`
	Limit  int    `json:"limit,omitempty"  doc:"Maximum number of lines to return (defaults to the whole file, capped at 2000)"`
}

// ReadFileOutput is the output schema of the read_file tool.
type ReadFileOutput struct {
	Path      string `json:"path"      doc:"Resolved absolute path of the file that was read"`
	Content   string `json:"content"   doc:"File contents, restricted to the requested line range"`
	Lines     int    `json:"lines"     doc:"Total number of lines in the file"`
	Offset    int    `json:"offset"    doc:"The 1-based line number the returned content starts at"`
	Truncated bool   `json:"truncated" doc:"True if the file has more lines than the returned range"`
	// Context is a project context hint (AGENTS.md) for the file's
	// directory, attached when one exists below the workspace root and has
	// not already been attached this session. Omitted when empty.
	Context string `json:"context,omitempty" doc:"Project context hint (AGENTS.md) for the file's directory, when one exists"`
}

// WriteFileInput is the input schema for the write_file tool.
type WriteFileInput struct {
	Path      string `json:"path"               doc:"Path of the file to write (absolute or relative to the working directory)"`
	Content   string `json:"content"            doc:"Full content to write to the file"`
	Overwrite *bool  `json:"overwrite,omitempty" doc:"Allow overwriting an existing file (defaults to false)"`
}

// WriteFileOutput is the output schema of the write_file tool.
type WriteFileOutput struct {
	Path         string `json:"path"          doc:"Resolved absolute path of the file that was written"`
	BytesWritten int64  `json:"bytes_written" doc:"Number of bytes written to disk"`
	Created      bool   `json:"created"       doc:"True if a new file was created, false if an existing file was overwritten"`
}

// PatchInput is the input schema for the patch tool (targeted edits).
type PatchInput struct {
	Path       string `json:"path"               doc:"Path of the file to edit (absolute or relative to the working directory)"`
	OldString  string `json:"old_string"         doc:"Exact text to find; must match the file content byte-for-byte, including whitespace and newlines"`
	NewString  string `json:"new_string"         doc:"Replacement text"`
	ReplaceAll *bool  `json:"replace_all,omitempty" doc:"Replace every occurrence of old_string instead of only the first (defaults to false)"`
}

// PatchOutput is the output schema of the patch tool.
type PatchOutput struct {
	Path     string `json:"path"     doc:"Resolved absolute path of the file that was edited"`
	Replaced int    `json:"replaced" doc:"Number of occurrences that were replaced"`
	Matches  int    `json:"matches"  doc:"Total number of occurrences of old_string found in the file"`
}

// SearchFilesInput is the input schema for the search_files tool.
type SearchFilesInput struct {
	Pattern    string   `json:"pattern"              doc:"Regular expression (RE2 syntax) to match against file contents"`
	Path       string   `json:"path,omitempty"       doc:"Directory to search recursively (defaults to the working directory)"`
	Include    []string `json:"include,omitempty"    doc:"Optional glob patterns (Go filepath.Match syntax, e.g. *.go) to filter file names; when empty, all files are searched"`
	OutputMode string   `json:"output_mode,omitempty" doc:"'content' (default) shows matching lines, 'files_with_matches' lists unique file paths, 'count' shows the number of matches per file"`
	HeadLimit  int      `json:"head_limit,omitempty" doc:"Maximum number of results to return (defaults to 100 when 0 or unset)"`
}

// SearchMatch describes a single search result.
type SearchMatch struct {
	Path    string `json:"path"              doc:"Path of the file containing the match"`
	Line    int    `json:"line"              doc:"1-based line number of the match (0 in files_with_matches mode)"`
	Content string `json:"content,omitempty" doc:"Text of the matching line (content mode only)"`
	Count   int    `json:"count,omitempty"   doc:"Number of matches in the file (count mode only)"`
}

// SearchFilesOutput is the output schema of the search_files tool.
type SearchFilesOutput struct {
	Matches   []SearchMatch `json:"matches"   doc:"Matching files and lines"`
	Total     int           `json:"total"     doc:"Total number of results"`
	Truncated bool          `json:"truncated" doc:"True if the result list was capped by head_limit"`
	// Context is a project context hint (AGENTS.md) for the search root
	// directory, attached when one exists below the workspace root and has
	// not already been attached this session. Omitted when empty.
	Context string `json:"context,omitempty" doc:"Project context hint (AGENTS.md) for the search root, when one exists"`
}

// createFileOpsTools builds the four-tool file operations toolset shared by
// the general-purpose agent. All tools operate on absolute or
// working-directory-relative paths. When a sessionManager and taskID are
// provided, file operations are scoped to the per-task sandbox root.
func createFileOpsTools(log LogFunc, sessionManager *SessionManager, taskID string) ([]tool.Tool, error) {
	// Determine the sandbox root for this task, if a session
	// manager and task ID are available.
	sandboxRoot := ""
	if sessionManager != nil && taskID != "" {
		fos := sessionManager.GetFileOps(taskID, "")
		if fos != nil {
			sandboxRoot = fos.RootDir
		}
	}
	// read_file: read whole files or a line range.
	readTool, err := newDocTool(functiontool.Config{
		Name:        "read_file",
		Description: "Reads the contents of a file, optionally restricted to a line range (offset/limit) for large files.",
	}, func(ctx agent.Context, input ReadFileInput) (ReadFileOutput, error) {
		path, err := taskResolve(input.Path, false, sandboxRoot)
		if err != nil {
			return ReadFileOutput{}, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return ReadFileOutput{}, fmt.Errorf("failed to read file %q: %w", path, err)
		}
		lines := splitLines(string(data))
		total := len(lines)

		offset := input.Offset
		if offset < 1 {
			offset = 1
		}
		limit := input.Limit
		if limit < 1 || limit > 2000 {
			limit = 2000
		}
		if offset > total {
			offset = total + 1
		}
		end := offset + limit - 1
		if end > total {
			end = total
		}

		content := ""
		if offset <= total {
			content = strings.Join(lines[offset-1:end], "\n")
		}
		if log != nil {
			log(fmt.Sprintf("📖 [fileops] Read %s (%d lines total)", path, total))
		}
		return ReadFileOutput{
			Path:      path,
			Content:   content,
			Lines:     total,
			Offset:    offset,
			Truncated: end < total,
			Context:   subdirContextHint(filepath.Dir(path)),
		}, nil
	})
	if err != nil {
		return nil, err
	}

	// write_file: create new files, or overwrite existing ones when allowed.
	writeTool, err := newDocTool(functiontool.Config{
		Name:        "write_file",
		Description: "Creates a new file with the given content (creating parent directories as needed), or overwrites an existing file when overwrite=true.",
	}, func(ctx agent.Context, input WriteFileInput) (WriteFileOutput, error) {
		path, err := taskResolve(input.Path, true, sandboxRoot)
		if err != nil {
			return WriteFileOutput{}, err
		}
		overwrite := false
		if input.Overwrite != nil {
			overwrite = *input.Overwrite
		}
		created := false
		if _, err := os.Stat(path); err == nil {
			if !overwrite {
				return WriteFileOutput{}, fmt.Errorf("file %q already exists; set overwrite=true to replace it", path)
			}
		} else if !os.IsNotExist(err) {
			return WriteFileOutput{}, fmt.Errorf("failed to stat file %q: %w", path, err)
		} else {
			created = true
		}

		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return WriteFileOutput{}, fmt.Errorf("failed to create parent directories for %q: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(input.Content), 0644); err != nil {
			return WriteFileOutput{}, fmt.Errorf("failed to write file %q: %w", path, err)
		}
		if log != nil {
			action := "created"
			if !created {
				action = "overwrote"
			}
			log(fmt.Sprintf("📝 [fileops] %s %s (%d bytes)", action, path, len(input.Content)))
		}
		return WriteFileOutput{
			Path:         path,
			BytesWritten: int64(len(input.Content)),
			Created:      created,
		}, nil
	})
	if err != nil {
		return nil, err
	}

	// patch: targeted string replacement inside an existing file.
	patchTool, err := newDocTool(functiontool.Config{
		Name:        "patch",
		Description: "Makes a targeted edit in an existing file by replacing old_string with new_string. Read the file first so old_string matches exactly.",
	}, func(ctx agent.Context, input PatchInput) (PatchOutput, error) {
		if input.OldString == "" {
			return PatchOutput{}, fmt.Errorf("old_string must not be empty")
		}
		path, err := taskResolve(input.Path, true, sandboxRoot)
		if err != nil {
			return PatchOutput{}, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return PatchOutput{}, fmt.Errorf("failed to read file %q: %w", path, err)
		}
		content := string(data)
		matches := strings.Count(content, input.OldString)
		if matches == 0 {
			return PatchOutput{}, fmt.Errorf("old_string not found in %q; read the file first to copy the exact text", path)
		}

		replaceAll := false
		if input.ReplaceAll != nil {
			replaceAll = *input.ReplaceAll
		}
		var updated string
		replaced := 1
		if replaceAll {
			updated = strings.ReplaceAll(content, input.OldString, input.NewString)
			replaced = matches
		} else {
			updated = strings.Replace(content, input.OldString, input.NewString, 1)
		}

		if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
			return PatchOutput{}, fmt.Errorf("failed to write file %q: %w", path, err)
		}
		if log != nil {
			log(fmt.Sprintf("🔧 [fileops] Patched %s (%d/%d occurrences replaced)", path, replaced, matches))
		}
		return PatchOutput{Path: path, Replaced: replaced, Matches: matches}, nil
	})
	if err != nil {
		return nil, err
	}

	// search_files: recursive regex search over file contents.
	searchTool, err := newDocTool(functiontool.Config{
		Name:        "search_files",
		Description: "Recursively searches file contents in a directory for a regular expression, returning matching files and lines (content), unique file paths (files_with_matches), or per-file match counts (count).",
	}, func(ctx agent.Context, input SearchFilesInput) (SearchFilesOutput, error) {
		re, err := regexp.Compile(input.Pattern)
		if err != nil {
			return SearchFilesOutput{}, fmt.Errorf("invalid pattern %q: %w", input.Pattern, err)
		}
		root := input.Path
		if root == "" {
			root = "."
		}
		rootAbs, err := taskResolve(root, false, sandboxRoot)
		if err != nil {
			return SearchFilesOutput{}, err
		}
		info, err := os.Stat(rootAbs)
		if err != nil {
			return SearchFilesOutput{}, fmt.Errorf("failed to stat search root %q: %w", rootAbs, err)
		}
		if !info.IsDir() {
			return SearchFilesOutput{}, fmt.Errorf("search path %q is not a directory", rootAbs)
		}

		mode := input.OutputMode
		switch mode {
		case "", "content", "files_with_matches", "count":
		default:
			return SearchFilesOutput{}, fmt.Errorf("invalid output_mode %q (valid: content, files_with_matches, count)", mode)
		}
		if mode == "" {
			mode = "content"
		}

		var matches []SearchMatch
		truncated := false

		// Default head_limit to 100 when unset so unbounded searches
		// (the common case when the model omits the field) are capped.
		headLimit := input.HeadLimit
		if headLimit <= 0 {
			headLimit = 100
		}

		// Per-tool deadline: bound the walk even when the tree is
		// pathologically large (e.g. mounted drives throwing I/O errors).
		// On expiry we stop gracefully and return partial results.
		// We derive from context.Background() rather than ctx because the
		// ADK ContextMock used in tests panics on Deadline()/Done(); the
		// walk deadline is a per-tool guardrail independent of the agent
		// context's own cancellation.
		walkCtx, cancel := context.WithTimeout(context.Background(), searchTimeout)
		defer cancel()

		entriesVisited := 0

		walkErr := filepath.WalkDir(rootAbs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			// Scan budget: count every entry visited (dirs + files).
			entriesVisited++
			if entriesVisited >= maxSearchEntries {
				truncated = true
				return filepath.SkipAll
			}
			// Per-tool deadline check.
			if walkCtx.Err() != nil {
				return walkCtx.Err()
			}
			if d.IsDir() {
				if path != rootAbs && (d.Name() == ".git" || d.Name() == ".venv" || d.Name() == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			if !includeFile(d.Name(), input.Include) {
				return nil
			}
			fi, err := d.Info()
			if err != nil || fi.Size() > maxSearchFileSize {
				return nil
			}
			matches = append(matches, searchFile(path, re, mode)...)

			if len(matches) >= headLimit {
				matches = matches[:headLimit]
				truncated = true
				return filepath.SkipAll
			}
			return nil
		})
		if walkErr != nil {
			// Deadline/cancel: return partial results, not an error.
			if errors.Is(walkErr, context.DeadlineExceeded) || errors.Is(walkErr, context.Canceled) {
				truncated = true
			} else {
				return SearchFilesOutput{}, fmt.Errorf("search walk failed: %w", walkErr)
			}
		}
		return SearchFilesOutput{Matches: matches, Total: len(matches), Truncated: truncated, Context: subdirContextHint(rootAbs)}, nil
	})
	if err != nil {
		return nil, err
	}

	return []tool.Tool{readTool, writeTool, patchTool, searchTool}, nil
}

// taskResolve is the path-resolution entry point for all file-ops tools.
// When the package-level currentSandbox is active (non-nil and not off),
// it delegates to (*SandboxConfig).resolveScopedPath for workspace
// confinement. Otherwise it falls back to the legacy resolveTaskPath
// behavior (sandbox-root join or plain resolvePath). The write flag
// selects write vs read containment in sandbox mode.
func taskResolve(path string, write bool, sandboxRoot string) (string, error) {
	if currentSandbox != nil && currentSandbox.Mode != SandboxModeOff {
		return currentSandbox.resolveScopedPath(path, write)
	}
	return resolveTaskPath(path, sandboxRoot)
}

// resolveTaskPath scopes a path to the task sandbox root when set,
// otherwise falls back to normal absolute resolution.
func resolveTaskPath(path string, sandboxRoot string) (string, error) {
	if sandboxRoot == "" {
		return resolvePath(path)
	}
	abs, err := filepath.Abs(filepath.Join(sandboxRoot, path))
	if err != nil {
		return "", fmt.Errorf("failed to resolve path %q within sandbox %q: %w", path, sandboxRoot, err)
	}
	return abs, nil
}

// resolvePath converts a possibly-relative path into an absolute path.
func resolvePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path %q: %w", p, err)
	}
	return abs, nil
}

// splitLines splits file content into lines, ignoring a trailing newline.
func splitLines(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// includeFile reports whether a file name matches any of the include globs.
// An empty include list matches everything.
func includeFile(name string, includes []string) bool {
	if len(includes) == 0 {
		return true
	}
	for _, pat := range includes {
		ok, err := filepath.Match(pat, name)
		if err == nil && ok {
			return true
		}
	}
	return false
}

// searchFile scans a single file with the compiled regex and returns matches
// shaped according to the output mode. Binary files and unreadable files
// produce no matches.
func searchFile(path string, re *regexp.Regexp, mode string) []SearchMatch {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil // binary file
	}

	switch mode {
	case "count":
		count := len(re.FindAll(data, -1))
		if count > 0 {
			return []SearchMatch{{Path: path, Count: count}}
		}
		return nil
	case "files_with_matches":
		if re.Match(data) {
			return []SearchMatch{{Path: path}}
		}
		return nil
	default: // content
		var out []SearchMatch
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		line := 0
		for sc.Scan() {
			line++
			text := sc.Text()
			if re.MatchString(text) {
				out = append(out, SearchMatch{Path: path, Line: line, Content: text})
			}
		}
		return out
	}
}
