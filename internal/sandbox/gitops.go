package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/project"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// Bounded-output limits shared by every git tool. Kept small so a large
// repository can never blow the model context through a single tool result.
const (
	// gitMaxOutputBytes caps the combined stdout+stderr capture of one git
	// run. Beyond it the writers stop buffering and set Truncated.
	gitMaxOutputBytes = 256 * 1024
	// gitMaxDiffLines caps the number of unified-diff lines git_diff returns.
	gitMaxDiffLines = 20000
	// gitMaxLogDefault is the default commit count for git_log.
	gitMaxLogDefault = 20
	// gitMaxLogCap is the hard ceiling for git_log's limit input.
	gitMaxLogCap = 100
	// gitExecTimeout bounds a single synchronous git run.
	gitExecTimeout = 120 * time.Second
)

// gitResult is the bounded outcome of one git run.
type gitResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	TimedOut  bool
	Truncated bool // combined capture hit gitMaxOutputBytes
}

// splitGitOut trims a trailing newline from command output.
func splitGitOut(s string) string {
	return strings.TrimRight(s, "\r\n")
}

// isNotARepoErr reports whether a git failure is the "not a git repository"
// shape (or a bare fatal), which read-only tools surface as NotARepo=true
// instead of an error.
func isNotARepoErr(res gitResult) bool {
	return strings.Contains(res.Stderr, "not a git repository") ||
		strings.Contains(res.Stderr, "fatal:")
}

// resolveRepoDir resolves the tool's repo_dir input through the sandbox.
// write=true for mutating tools selects the stricter write containment.
// Resolution order for an empty input: a pinned sandbox workspace root first
// (an explicitly configured workspace is the deliberate default, e.g. a web
// server pointed at a code directory), then the session project root, then
// the process working directory.
func resolveRepoDir(repoDir string, write bool) (string, error) {
	if strings.TrimSpace(repoDir) != "" {
		return taskResolve(repoDir, write, "")
	}
	// Pinned sandbox roots win over every other default: BuildExecCommand
	// rejects working directories outside the roots, so this is also the
	// only sandbox-safe fallback while a workspace is configured.
	if CurrentSandbox != nil && CurrentSandbox.Mode != SandboxModeOff {
		if root := CurrentSandbox.WorkspaceRoot(); root != "" {
			return root, nil
		}
	}
	// Session project root (set once in SetupRunner from the process cwd)
	// defaults repo-wide git operations to the repository root. Under an
	// active sandbox it is used only when it sits inside the approved scope.
	if pr := project.CurrentRoot(); pr != "" {
		if CurrentSandbox == nil || CurrentSandbox.Mode == SandboxModeOff {
			return pr, nil
		}
		if _, err := CurrentSandbox.ResolveScopedPath(pr, false); err == nil {
			return pr, nil
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("git: cannot determine working directory: %w", err)
	}
	return wd, nil
}

// cappedCapture buffers stdout and stderr against a single combined budget.
type cappedCapture struct {
	remaining int
	stdout    bytes.Buffer
	stderr    bytes.Buffer
	truncated bool
}

type captureWriter struct {
	c *cappedCapture
	w *bytes.Buffer
}

func (cw captureWriter) Write(p []byte) (int, error) {
	c := cw.c
	if c.remaining > 0 {
		n := len(p)
		if n > c.remaining {
			cw.w.Write(p[:c.remaining])
			c.remaining = 0
			c.truncated = true
			return n, nil // claim full length; git does not rely on short writes
		}
		cw.w.Write(p)
		c.remaining -= n
		return n, nil
	}
	c.truncated = true
	return len(p), nil
}

// runGit executes "git <args...>" inside repoDir through BuildExecCommand,
// which applies the harmful-command gate, interactive approval, path audit,
// env scrubbing, and bubblewrap wrapping. argv is always the explicit form
// (never a shell string) and the repo directory is passed as the working
// directory (never -C), so classifyGitRisk sees the subcommand in argv[1]
// (status/log/diff/branch = LOW; add/commit = MEDIUM).
func runGit(ctx context.Context, repoDir string, args []string, write bool, log interfaces.LogFunc) (gitResult, error) {
	if len(args) == 0 {
		return gitResult{}, fmt.Errorf("git: no subcommand")
	}
	dir, err := resolveRepoDir(repoDir, write)
	if err != nil {
		return gitResult{}, err
	}

	// GIT_TERMINAL_PROMPT=0 makes credential/confirmation prompts fail fast
	// instead of hanging the run waiting on a TTY.
	cmd, err := BuildExecCommand("git", args, dir, map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
	})
	if err != nil {
		return gitResult{Stdout: "", Stderr: err.Error()}, err
	}

	capture := &cappedCapture{remaining: gitMaxOutputBytes}
	cmd.Stdout = captureWriter{c: capture, w: &capture.stdout}
	cmd.Stderr = captureWriter{c: capture, w: &capture.stderr}

	if log != nil {
		log(fmt.Sprintf("🔀 [git] Running: git %s", strings.Join(args, " ")))
	}

	// Pdeathsig fires on the OS thread that called Start; lock it so the
	// runtime does not recycle it before Wait reaps the child (golang/go#27505).
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := cmd.Start(); err != nil {
		return gitResult{}, fmt.Errorf("failed to start git %s: %w", args[0], err)
	}
	// Windows: assign the started process to its kill-on-close Job Object
	// (no-op on Unix, where Setpgid already grouped the tree).
	_ = attachProcessTree(cmd)

	// Caller cancellation kills the process tree too, so a torn-down
	// session cannot leave a git child behind.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = killProcessTree(cmd)
		case <-done:
		}
	}()

	timedOut := false
	timeoutTimer := time.AfterFunc(gitExecTimeout, func() {
		timedOut = true
		_ = killProcessTree(cmd)
	})
	defer timeoutTimer.Stop()

	runErr := cmd.Wait()
	releaseProcessTree(cmd)
	canceled := ctx.Err() != nil

	res := gitResult{
		Stdout:    splitGitOut(capture.stdout.String()),
		Stderr:    splitGitOut(capture.stderr.String()),
		ExitCode:  -1,
		TimedOut:  timedOut,
		Truncated: capture.truncated,
	}

	if timedOut {
		return res, fmt.Errorf("git %s timed out after %s", args[0], gitExecTimeout.String())
	}
	if canceled {
		return res, fmt.Errorf("git %s cancelled", args[0])
	}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		res.ExitCode = 0
	case errors.As(runErr, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		return res, fmt.Errorf("failed to run git %s: %w", args[0], runErr)
	}

	if res.ExitCode != 0 {
		hint := strings.TrimSpace(res.Stderr)
		if hint == "" {
			hint = strings.TrimSpace(res.Stdout)
		}
		if hint == "" {
			hint = fmt.Sprintf("git %s exited with status %d", args[0], res.ExitCode)
		}
		util.DebugWarn("git_exec_failed", "args", args, "cwd", dir, "exit_code", res.ExitCode)
		return res, fmt.Errorf("git %s: %s", args[0], wrapUntrustedData(hint))
	}

	return res, nil
}

// ---------------------------------------------------------------------------
// git_status
// ---------------------------------------------------------------------------

// GitStatusInput is the input schema for the git_status tool.
type GitStatusInput struct {
	RepoDir   string `json:"repo_dir,omitempty"  doc:"Repository directory (defaults to the working directory)"`
	Untracked *bool  `json:"untracked,omitempty" doc:"Include untracked files (defaults to true)"`
}

// GitStatusEntry describes one working-tree change.
type GitStatusEntry struct {
	Status string `json:"status" doc:"Two-character porcelain status code, e.g. ' M' or '??'"`
	Staged bool   `json:"staged" doc:"True when the change is staged (first char not space and not ?)"`
	Path   string `json:"path"   doc:"Repository-relative path (renames: target path)"`
	From   string `json:"from,omitempty" doc:"Source path for renames/copies"`
}

// GitStatusOutput is the output schema of the git_status tool.
type GitStatusOutput struct {
	RepoDir  string           `json:"repo_dir"`
	Branch   string           `json:"branch,omitempty"`
	Ahead    int              `json:"ahead,omitempty"`
	Behind   int              `json:"behind,omitempty"`
	Entries  []GitStatusEntry `json:"entries"`
	NotARepo bool             `json:"not_a_repo,omitempty" doc:"True when the directory is not a git repository"`
	Stderr   string           `json:"stderr,omitempty"`
}

// porcBranchRe matches the "[ahead N, behind M]" tracking counts in the
// porcelain -b header. Both counts share one bracket ("[ahead 2, behind 1]"),
// so the counts are matched anywhere in the header, not bracket-anchored.
var porcBranchRe = regexp.MustCompile(`(ahead|behind) (\d+)`)

// parsePorcelainStatus parses `git status --porcelain=v1 -b` output into a
// branch header plus per-file entries. Returns raw (unwrapped) values; the
// tool handler wraps them as untrusted data when building its output schema,
// and the workspace snapshot wraps the whole rendered block once.
func parsePorcelainStatus(raw string) (branch, upstream string, ahead, behind int, entries []GitStatusEntry) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			hdr := line[3:]
			branchPart := hdr
			if idx := strings.Index(hdr, " ["); idx >= 0 {
				branchPart = hdr[:idx]
			}
			branch = strings.SplitN(branchPart, "...", 2)[0]
			if idx := strings.Index(branchPart, "..."); idx >= 0 {
				upstream = branchPart[idx+3:]
			}
			for _, m := range porcBranchRe.FindAllStringSubmatch(hdr, -1) {
				n, _ := strconv.Atoi(m[2])
				if m[1] == "ahead" {
					ahead = n
				} else {
					behind = n
				}
			}
			continue
		}
		// Skip the "?? placeholder" edge; require "XY " shape.
		if len(line) < 4 || (line[2] != ' ' && line[2] != '\t') {
			entries = append(entries, GitStatusEntry{Status: line, Path: line})
			continue
		}
		code := line[:2]
		rest := line[3:]
		staged := code[0] != ' ' && code[0] != '?'
		path := unquotePorcelainPath(rest)
		from := ""
		if idx := strings.Index(rest, " -> "); idx >= 0 {
			from = unquotePorcelainPath(rest[:idx])
			path = unquotePorcelainPath(rest[idx+4:])
		}
		entries = append(entries, GitStatusEntry{
			Status: code,
			Staged: staged,
			Path:   path,
			From:   from,
		})
	}
	return branch, upstream, ahead, behind, entries
}

// unquotePorcelainPath strips porcelain v1 C-style quoting around paths
// containing spaces or non-ASCII characters. Falls back to the raw string.
func unquotePorcelainPath(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if unquoted, err := strconv.Unquote(s); err == nil {
			return unquoted
		}
		return s[1 : len(s)-1]
	}
	return s
}

// gitStatusContent is the package-level handler for the git_status tool.
func gitStatusContent(ctx context.Context, input GitStatusInput, log interfaces.LogFunc) (GitStatusOutput, error) {
	out := GitStatusOutput{}
	dir, err := resolveRepoDir(input.RepoDir, false)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	args := []string{"status", "--porcelain=v1", "-b"}
	if input.Untracked != nil && !*input.Untracked {
		args = append(args, "--untracked-files=no")
	}
	res, err := runGit(ctx, dir, args, false, log)
	if err != nil {
		if isNotARepoErr(res) {
			out.NotARepo = true
			out.Stderr = wrapUntrustedData(res.Stderr)
			return out, nil
		}
		return out, err
	}
	branch, _, ahead, behind, entries := parsePorcelainStatus(res.Stdout)
	out.Branch = wrapUntrustedData(branch)
	out.Ahead = ahead
	out.Behind = behind
	// D7: repo-derived strings are untrusted data. The parser returns raw
	// values; wrap each field once here (the workspace snapshot wraps the
	// whole rendered block instead).
	for i := range entries {
		entries[i].Status = wrapUntrustedData(entries[i].Status)
		entries[i].Path = wrapUntrustedData(entries[i].Path)
		entries[i].From = wrapUntrustedData(entries[i].From)
	}
	out.Entries = entries
	return out, nil
}

// ---------------------------------------------------------------------------
// git_diff
// ---------------------------------------------------------------------------

// GitDiffInput is the input schema for the git_diff tool.
type GitDiffInput struct {
	RepoDir string `json:"repo_dir,omitempty" doc:"Repository directory (defaults to the working directory)"`
	Staged  *bool  `json:"staged,omitempty"   doc:"Show staged changes (git diff --staged; defaults to false)"`
	Path    string `json:"path,omitempty"     doc:"Limit the diff to one repository-relative path or directory"`
}

// GitDiffOutput is the output schema of the git_diff tool.
type GitDiffOutput struct {
	RepoDir   string `json:"repo_dir"`
	Diff      string `json:"diff"      doc:"Unified diff (git diff --no-color), wrapped as untrusted data"`
	Truncated bool   `json:"truncated" doc:"True when the diff exceeded gitMaxDiffLines"`
	NotARepo  bool   `json:"not_a_repo,omitempty"`
}

// gitDiffContent is the package-level handler for the git_diff tool.
func gitDiffContent(ctx context.Context, input GitDiffInput, log interfaces.LogFunc) (GitDiffOutput, error) {
	out := GitDiffOutput{}
	dir, err := resolveRepoDir(input.RepoDir, false)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	args := []string{"diff", "--no-color"}
	if input.Staged != nil && *input.Staged {
		args = append(args, "--staged")
	}
	if strings.TrimSpace(input.Path) != "" {
		args = append(args, "--", input.Path)
	}
	res, err := runGit(ctx, dir, args, false, log)
	if err != nil {
		if isNotARepoErr(res) {
			out.NotARepo = true
			return out, nil
		}
		return out, err
	}

	// Line cap on top of the byte cap.
	lines := strings.Split(res.Stdout, "\n")
	if len(lines) > gitMaxDiffLines {
		lines = lines[:gitMaxDiffLines]
		out.Truncated = true
	}
	out.Truncated = out.Truncated || res.Truncated
	out.Diff = wrapUntrustedData(strings.Join(lines, "\n"))
	return out, nil
}

// ---------------------------------------------------------------------------
// git_log
// ---------------------------------------------------------------------------

// GitLogInput is the input schema for the git_log tool.
type GitLogInput struct {
	RepoDir string `json:"repo_dir,omitempty" doc:"Repository directory (defaults to the working directory)"`
	Limit   int    `json:"limit,omitempty"    doc:"Number of commits (defaults to 20, capped at 100)"`
	Path    string `json:"path,omitempty"     doc:"Only commits touching this repository-relative path"`
}

// GitLogEntry is one recent commit.
type GitLogEntry struct {
	Sha     string `json:"sha"     doc:"Abbreviated commit hash"`
	Author  string `json:"author"  doc:"Commit author name"`
	Date    string `json:"date"    doc:"Commit date (YYYY-MM-DD)"`
	Subject string `json:"subject" doc:"Commit subject line"`
}

// GitLogOutput is the output schema of the git_log tool.
type GitLogOutput struct {
	RepoDir  string        `json:"repo_dir"`
	Commits  []GitLogEntry `json:"commits"`
	NotARepo bool          `json:"not_a_repo,omitempty"`
}

// gitLogContent is the package-level handler for the git_log tool.
func gitLogContent(ctx context.Context, input GitLogInput, log interfaces.LogFunc) (GitLogOutput, error) {
	out := GitLogOutput{}
	dir, err := resolveRepoDir(input.RepoDir, false)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	limit := input.Limit
	if limit <= 0 {
		limit = gitMaxLogDefault
	}
	if limit > gitMaxLogCap {
		limit = gitMaxLogCap
	}

	args := []string{"log", "--pretty=format:%h%x09%an%x09%ad%x09%s", "--date=short", "-n", strconv.Itoa(limit)}
	if strings.TrimSpace(input.Path) != "" {
		args = append(args, "--", input.Path)
	}
	res, err := runGit(ctx, dir, args, false, log)
	if err != nil {
		if isNotARepoErr(res) {
			out.NotARepo = true
			return out, nil
		}
		return out, err
	}

	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		e := GitLogEntry{Subject: line}
		if len(parts) == 4 {
			e.Sha, e.Author, e.Date, e.Subject = parts[0], parts[1], parts[2], parts[3]
		}
		e.Sha = wrapUntrustedData(e.Sha)
		e.Author = wrapUntrustedData(e.Author)
		e.Date = wrapUntrustedData(e.Date)
		e.Subject = wrapUntrustedData(e.Subject)
		out.Commits = append(out.Commits, e)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// git_branch
// ---------------------------------------------------------------------------

// GitBranchInput is the input schema for the git_branch tool.
type GitBranchInput struct {
	RepoDir string `json:"repo_dir,omitempty" doc:"Repository directory (defaults to the working directory)"`
	All     *bool  `json:"all,omitempty"      doc:"Include remote-tracking branches (git branch --all; defaults to false)"`
}

// GitBranchEntry is one branch.
type GitBranchEntry struct {
	Name    string `json:"name"    doc:"Branch name (remote-tracking branches keep the 'remotes/origin/x' form)"`
	Current bool   `json:"current" doc:"True for the checked-out branch"`
}

// GitBranchOutput is the output schema of the git_branch tool.
type GitBranchOutput struct {
	RepoDir  string           `json:"repo_dir"`
	Branches []GitBranchEntry `json:"branches"`
	NotARepo bool             `json:"not_a_repo,omitempty"`
}

// gitBranchContent is the package-level handler for the git_branch tool.
func gitBranchContent(ctx context.Context, input GitBranchInput, log interfaces.LogFunc) (GitBranchOutput, error) {
	out := GitBranchOutput{}
	dir, err := resolveRepoDir(input.RepoDir, false)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	args := []string{"branch", "--list"}
	if input.All != nil && *input.All {
		args = append(args, "--all")
	}
	res, err := runGit(ctx, dir, args, false, log)
	if err != nil {
		if isNotARepoErr(res) {
			out.NotARepo = true
			return out, nil
		}
		return out, err
	}

	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		current := strings.HasPrefix(line, "*")
		name := line
		if len(line) >= 2 {
			name = line[2:]
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out.Branches = append(out.Branches, GitBranchEntry{
			Name:    wrapUntrustedData(name),
			Current: current,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// git_stage (mutating, MEDIUM risk -> approval gate)
// ---------------------------------------------------------------------------

// GitStageInput is the input schema for the git_stage tool.
type GitStageInput struct {
	RepoDir string   `json:"repo_dir"          doc:"Repository directory (defaults to the working directory)"`
	Paths   []string `json:"paths"             doc:"Repository-relative paths to stage (at least one required; '.' stages everything)"`
	Unstage *bool    `json:"unstage,omitempty" doc:"Unstage instead of stage (git rm --cached; defaults to false)"`
}

// GitStageOutput is the output schema of the git_stage tool.
type GitStageOutput struct {
	RepoDir string   `json:"repo_dir"`
	Paths   []string `json:"paths"   doc:"Paths the operation was applied to"`
	Message string   `json:"message" doc:"Bounded git output (wrapped as untrusted data)"`
}

// gitStageContent is the package-level handler for the git_stage tool.
func gitStageContent(ctx context.Context, input GitStageInput, log interfaces.LogFunc) (GitStageOutput, error) {
	out := GitStageOutput{}
	if len(input.Paths) == 0 {
		return out, fmt.Errorf("git_stage: at least one path is required")
	}
	dir, err := resolveRepoDir(input.RepoDir, true)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir
	out.Paths = append([]string(nil), input.Paths...)

	var args []string
	if input.Unstage != nil && *input.Unstage {
		args = append([]string{"rm", "--cached", "--"}, input.Paths...)
	} else {
		args = append([]string{"add", "--"}, input.Paths...)
	}
	res, err := runGit(ctx, dir, args, true, log)
	if err != nil {
		return out, err
	}
	msg := strings.TrimSpace(res.Stdout)
	if msg == "" {
		msg = strings.TrimSpace(res.Stderr)
	}
	out.Message = wrapUntrustedData(msg)
	return out, nil
}

// ---------------------------------------------------------------------------
// git_commit (mutating, MEDIUM risk -> approval gate)
// ---------------------------------------------------------------------------

// GitCommitInput is the input schema for the git_commit tool.
type GitCommitInput struct {
	RepoDir    string `json:"repo_dir"              doc:"Repository directory (defaults to the working directory)"`
	Message    string `json:"message"               doc:"Commit message (required)"`
	StageAll   *bool  `json:"stage_all,omitempty"   doc:"Stage all changes first (git add -A; defaults to false)"`
	AllowEmpty *bool  `json:"allow_empty,omitempty" doc:"Allow committing with no changes (--allow-empty; defaults to false)"`
}

// GitCommitOutput is the output schema of the git_commit tool.
type GitCommitOutput struct {
	RepoDir  string `json:"repo_dir"`
	Sha      string `json:"sha"       doc:"Full commit hash"`
	ShortSha string `json:"short_sha" doc:"Abbreviated commit hash"`
	Subject  string `json:"subject"   doc:"Commit subject line"`
	NotARepo bool   `json:"not_a_repo,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// gitCommitContent is the package-level handler for the git_commit tool.
func gitCommitContent(ctx context.Context, input GitCommitInput, log interfaces.LogFunc) (GitCommitOutput, error) {
	out := GitCommitOutput{}
	if strings.TrimSpace(input.Message) == "" {
		return out, fmt.Errorf("git_commit: a commit message is required")
	}
	dir, err := resolveRepoDir(input.RepoDir, true)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	// Optional pre-step: stage every change so the model can commit the
	// whole working tree in one call.
	if input.StageAll != nil && *input.StageAll {
		if _, err := runGit(ctx, dir, []string{"add", "-A", "--", "."}, true, log); err != nil {
			return out, fmt.Errorf("git_commit: stage_all failed: %w", err)
		}
	}

	args := []string{"commit", "-m", input.Message}
	if input.AllowEmpty != nil && *input.AllowEmpty {
		args = append(args, "--allow-empty")
	}
	res, err := runGit(ctx, dir, args, true, log)
	if err != nil {
		if isNotARepoErr(res) {
			out.NotARepo = true
			out.Stderr = wrapUntrustedData(res.Stderr)
			return out, nil
		}
		return out, err
	}
	// Read the created commit back (read-only, LOW risk) for the response.
	if shaRes, serr := runGit(ctx, dir, []string{"rev-parse", "HEAD"}, false, log); serr == nil && shaRes.ExitCode == 0 {
		out.Sha = wrapUntrustedData(splitGitOut(shaRes.Stdout))
		if len(out.Sha) > 7 {
			out.ShortSha = out.Sha[:7]
		}
	}
	if subjRes, serr := runGit(ctx, dir, []string{"log", "-1", "--pretty=%s"}, false, log); serr == nil && subjRes.ExitCode == 0 {
		out.Subject = wrapUntrustedData(splitGitOut(subjRes.Stdout))
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Tool registration
// ---------------------------------------------------------------------------

// CreateGitOpsTools builds the six-tool git toolset shared by the
// orchestrator and the general-purpose agent. Every tool runs git through
// BuildExecCommand, so the harmful-command policy, approval gate, path
// audit, env scrubbing, and audit log apply to structured git operations
// exactly as they do to system_exec.
func CreateGitOpsTools(log interfaces.LogFunc) ([]tool.Tool, error) {
	statusTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_status",
		Description: "Shows the working tree status as a structured list: current branch, ahead/behind counts, and per-file status codes (staged/unstaged/untracked). Read-only; no approval required. Runs through the same protection policy as system_exec.",
	}, func(ctx agent.Context, input GitStatusInput) (GitStatusOutput, error) {
		return gitStatusContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	diffTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_diff",
		Description: "Returns the unified diff of unstaged changes (or staged changes when staged=true), optionally limited to one repository-relative path. Read-only; no approval required.",
	}, func(ctx agent.Context, input GitDiffInput) (GitDiffOutput, error) {
		return gitDiffContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	logTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_log",
		Description: "Lists recent commits (abbreviated hash, author, date, subject) newest first, optionally limited to commits touching one path. Read-only; no approval required.",
	}, func(ctx agent.Context, input GitLogInput) (GitLogOutput, error) {
		return gitLogContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	branchTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_branch",
		Description: "Lists git branches and marks the current one (use all=true to include remote-tracking branches). Read-only; no approval required.",
	}, func(ctx agent.Context, input GitBranchInput) (GitBranchOutput, error) {
		return gitBranchContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	stageTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_stage",
		Description: "Stages (or unstages with unstage=true) specific repository-relative paths in a git repository. Mutating: it flows through the same harmful-command policy and approval gate as system_exec, so it may require your approval.",
	}, func(ctx agent.Context, input GitStageInput) (GitStageOutput, error) {
		return gitStageContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	commitTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_commit",
		Description: "Creates a commit with the given message, optionally staging all changes first (stage_all=true). Mutating: it flows through the same harmful-command policy and approval gate as system_exec, so it may require your approval.",
	}, func(ctx agent.Context, input GitCommitInput) (GitCommitOutput, error) {
		return gitCommitContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	return []tool.Tool{statusTool, diffTool, logTool, branchTool, stageTool, commitTool}, nil
}

// ---------------------------------------------------------------------------
// Git workspace snapshot (session-start repo awareness)
// ---------------------------------------------------------------------------

// gitWorkspaceCommitCount is how many recent commits the workspace snapshot
// lists. Kept tiny: the block is orientation, not history.
const gitWorkspaceCommitCount = 3

// countGitEntries buckets porcelain entries into the category counts shown in
// the workspace snapshot: staged (index column differs from space), modified
// (worktree column differs while the index is clean), untracked ("??"), and
// conflicted (a U in either column, or both sides added/deleted). Categories
// are disjoint: a conflicted entry is never also staged or modified.
func countGitEntries(entries []GitStatusEntry) (staged, modified, untracked, conflicts int) {
	for _, e := range entries {
		if len(e.Status) < 2 {
			continue
		}
		x, y := e.Status[0], e.Status[1]
		switch {
		case x == '?':
			untracked++
		case x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D'):
			conflicts++
		case x != ' ':
			staged++
		case y != ' ':
			modified++
		}
	}
	return staged, modified, untracked, conflicts
}

// BuildGitWorkspaceBlock renders a compact session-start snapshot of the
// repository at repoDir for injection into agent instructions: branch and
// upstream tracking, per-category working-tree counts, and the newest
// commits. It is a snapshot with an explicit re-check note, never a live
// view, so the prompt stays stable. Returns "" (no error) when repoDir is
// not inside a git repository, so callers can skip the block and keep
// booting; errors are reserved for genuine failures. The whole block is
// wrapped as untrusted data: every repo-derived line is attacker-controllable.
func BuildGitWorkspaceBlock(ctx context.Context, repoDir string, log interfaces.LogFunc) (string, error) {
	dir, err := resolveRepoDir(repoDir, false)
	if err != nil {
		return "", err
	}

	res, err := runGit(ctx, dir, []string{"status", "--porcelain=v1", "-b"}, false, log)
	if err != nil {
		if isNotARepoErr(res) {
			return "", nil
		}
		return "", err
	}
	branch, upstream, ahead, behind, entries := parsePorcelainStatus(res.Stdout)
	staged, modified, untracked, conflicts := countGitEntries(entries)

	detached := strings.HasSuffix(branch, " (no branch)")
	unborn := strings.HasPrefix(branch, "No commits yet on ")
	branchLabel := branch
	if detached {
		branchLabel = "(detached HEAD)"
	} else if unborn {
		branchLabel = strings.TrimPrefix(branch, "No commits yet on ")
	}

	var b strings.Builder
	b.WriteString("### GIT WORKSPACE (snapshot at session start - re-check with git_status/git_diff before acting on it):\n")
	fmt.Fprintf(&b, "Root: %s\n", dir)
	switch {
	case detached:
		fmt.Fprintf(&b, "Branch: %s\n", branchLabel)
	case unborn:
		fmt.Fprintf(&b, "Branch: %s (no commits yet)\n", branchLabel)
	case upstream != "":
		fmt.Fprintf(&b, "Branch: %s -> %s (ahead %d, behind %d)\n", branchLabel, upstream, ahead, behind)
	default:
		fmt.Fprintf(&b, "Branch: %s\n", branchLabel)
	}
	if staged+modified+untracked+conflicts == 0 {
		b.WriteString("Status: clean\n")
	} else {
		var parts []string
		if staged > 0 {
			parts = append(parts, fmt.Sprintf("staged %d", staged))
		}
		if modified > 0 {
			parts = append(parts, fmt.Sprintf("modified %d", modified))
		}
		if untracked > 0 {
			parts = append(parts, fmt.Sprintf("untracked %d", untracked))
		}
		if conflicts > 0 {
			parts = append(parts, fmt.Sprintf("conflicts %d", conflicts))
		}
		fmt.Fprintf(&b, "Status: %s\n", strings.Join(parts, ", "))
	}

	if cres, err := runGit(ctx, dir, []string{"log", "--pretty=format:%h%x09%s", "-n", strconv.Itoa(gitWorkspaceCommitCount)}, false, log); err == nil && strings.TrimSpace(cres.Stdout) != "" {
		b.WriteString("Recent commits (newest first):\n")
		for _, line := range strings.Split(cres.Stdout, "\n") {
			parts := strings.SplitN(line, "\t", 2)
			sha := parts[0]
			subject := ""
			if len(parts) == 2 {
				subject = parts[1]
			}
			fmt.Fprintf(&b, "  %s %s\n", sha, subject)
		}
	}
	return wrapUntrustedData(b.String()), nil
}
