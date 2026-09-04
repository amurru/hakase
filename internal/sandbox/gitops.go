package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
// server pointed at a code directory), then the project root for ctx (the
// context-scoped root set for registered-project sessions, falling back to
// the process-wide session root), then the process working directory.
func resolveRepoDir(ctx context.Context, repoDir string, write bool) (string, error) {
	if strings.TrimSpace(repoDir) != "" {
		return taskResolve(ctx, repoDir, write, "")
	}
	// The sandbox that applies to this run (context-scoped override for
	// project-bound sessions, else the process CurrentSandbox).
	sb := ConfigFrom(ctx)
	// Pinned sandbox roots win over every other default: BuildExecCommand
	// rejects working directories outside the roots, so this is also the
	// only sandbox-safe fallback while a workspace is configured.
	if sb != nil && sb.Mode != SandboxModeOff {
		if root := sb.WorkspaceRoot(); root != "" {
			return root, nil
		}
	}
	// Project root for this run (context-scoped for registered-project
	// sessions; process-wide root otherwise) defaults repo-wide git
	// operations to the repository root. Under an active sandbox it is used
	// only when it sits inside the approved scope.
	if pr := project.RootFrom(ctx); pr != "" {
		if sb == nil || sb.Mode == SandboxModeOff {
			return pr, nil
		}
		if _, err := sb.ResolveScopedPath(pr, false); err == nil {
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
// os/exec drains the two pipes on separate goroutines, so remaining/truncated
// are mutex-guarded.
type cappedCapture struct {
	mu        sync.Mutex
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
	c.mu.Lock()
	defer c.mu.Unlock()
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
	return runGitOpt(ctx, repoDir, args, write, log)
}

// runGitOperator is runGit under operator authority (see
// ExecOperatorAuthorized): same hardening, but the interactive approval gate
// is bypassed because the human operator issued the command directly. Used by
// the project-registry materialization, never by agent-facing tools.
func runGitOperator(ctx context.Context, repoDir string, args []string, write bool, log interfaces.LogFunc) (gitResult, error) {
	return runGitOpt(ctx, repoDir, args, write, log, ExecOperatorAuthorized())
}

func runGitOpt(ctx context.Context, repoDir string, args []string, write bool, log interfaces.LogFunc, opts ...ExecOption) (gitResult, error) {
	if len(args) == 0 {
		return gitResult{}, fmt.Errorf("git: no subcommand")
	}
	dir, err := resolveRepoDir(ctx, repoDir, write)
	if err != nil {
		return gitResult{}, err
	}

	// GIT_TERMINAL_PROMPT=0 makes credential/confirmation prompts fail fast
	// instead of hanging the run waiting on a TTY. The sandbox comes from the
	// run context so a project-bound session's pinned sandbox constrains git.
	cmd, err := BuildExecCommandFor(ctx, "git", args, dir, map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
	}, opts...)
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

	var timedOut atomic.Bool
	timeoutTimer := time.AfterFunc(gitExecTimeout, func() {
		timedOut.Store(true)
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
		TimedOut:  timedOut.Load(),
		Truncated: capture.truncated,
	}

	if timedOut.Load() {
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
	dir, err := resolveRepoDir(ctx, input.RepoDir, false)
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
	dir, err := resolveRepoDir(ctx, input.RepoDir, false)
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
	dir, err := resolveRepoDir(ctx, input.RepoDir, false)
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
	dir, err := resolveRepoDir(ctx, input.RepoDir, false)
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
	dir, err := resolveRepoDir(ctx, input.RepoDir, true)
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
	dir, err := resolveRepoDir(ctx, input.RepoDir, true)
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
		// Slice the raw hash before wrapping: wrapUntrustedData prefixes the
		// framing markers, so taking the first characters after wrapping would
		// capture marker text, not the commit.
		sha := splitGitOut(shaRes.Stdout)
		out.Sha = wrapUntrustedData(sha)
		if len(sha) > 7 {
			out.ShortSha = sha[:7]
		}
	}
	if subjRes, serr := runGit(ctx, dir, []string{"log", "-1", "--pretty=%s"}, false, log); serr == nil && subjRes.ExitCode == 0 {
		out.Subject = wrapUntrustedData(splitGitOut(subjRes.Stdout))
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// git_clone / git_push / git_pull (remote operations)
// ---------------------------------------------------------------------------

// GitCloneInput is the input schema for the git_clone tool.
type GitCloneInput struct {
	URL    string `json:"url"              doc:"Remote repository URL (https://, git://, ssh://, or file:// when no sandbox is active)"`
	Dir    string `json:"dir"              doc:"Target directory for the clone (must not exist or must be empty)"`
	Branch string `json:"branch,omitempty" doc:"Clone only this branch (git clone --branch)"`
}

// GitCloneOutput is the output schema of the git_clone tool.
type GitCloneOutput struct {
	Dir     string `json:"dir"`
	Message string `json:"message,omitempty" doc:"Bounded git output (wrapped as untrusted data)"`
}

// GitPushInput is the input schema for the git_push tool.
type GitPushInput struct {
	RepoDir     string `json:"repo_dir,omitempty"     doc:"Repository directory (defaults to the project root / working directory)"`
	Remote      string `json:"remote,omitempty"       doc:"Remote name (defaults to origin)"`
	Branch      string `json:"branch,omitempty"       doc:"Branch to push (defaults to the branch's configured upstream, if any)"`
	SetUpstream *bool  `json:"set_upstream,omitempty" doc:"Set the upstream tracking ref (git push --set-upstream; defaults to false)"`
}

// GitPushOutput is the output schema of the git_push tool.
type GitPushOutput struct {
	RepoDir  string `json:"repo_dir"`
	Remote   string `json:"remote"`
	Branch   string `json:"branch,omitempty"`
	Message  string `json:"message,omitempty" doc:"Bounded git output (wrapped as untrusted data)"`
	NotARepo bool   `json:"not_a_repo,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// GitPullInput is the input schema for the git_pull tool.
type GitPullInput struct {
	RepoDir string `json:"repo_dir,omitempty" doc:"Repository directory (defaults to the project root / working directory)"`
	Remote  string `json:"remote,omitempty"   doc:"Remote name (defaults to origin)"`
	Branch  string `json:"branch,omitempty"   doc:"Branch to pull (defaults to the branch's configured upstream, if any)"`
}

// GitPullOutput is the output schema of the git_pull tool.
type GitPullOutput struct {
	RepoDir  string `json:"repo_dir"`
	Remote   string `json:"remote"`
	Branch   string `json:"branch,omitempty"`
	Message  string `json:"message,omitempty" doc:"Bounded git output (wrapped as untrusted data)"`
	NotARepo bool   `json:"not_a_repo,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// validGitRemote and validGitRef keep remote/branch inputs in the character
// set git itself accepts for names. validGitRevision additionally allows the
// revision syntax reset accepts (HEAD~2, a1b2c3d^, @{u}, origin/main). They
// are validation for UX and argv clarity - there is no shell involved, so
// nothing here is an injection boundary.
func validGitRemote(s string) bool {
	return validGitName(s, false)
}

func validGitRef(s string) bool {
	return validGitName(s, true)
}

func validGitRevision(s string) bool {
	if s == "" || s[0] == '-' || strings.ContainsAny(s, " \t\r\n\x00") {
		return false
	}
	if strings.Contains(s, "..") || strings.HasSuffix(s, "/") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._/@~^:{}-", r):
		default:
			return false
		}
	}
	return true
}

func validGitName(s string, allowSlash bool) bool {
	if s == "" || s[0] == '-' || strings.ContainsAny(s, " \t\r\n\x00~^:?*[\\") {
		return false
	}
	if strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") {
		return false
	}
	if !allowSlash && strings.Contains(s, "/") {
		return false
	}
	return true
}

// validGitTagName adds git's check-ref-format rules on top of validGitName:
// no "..", no "@{", no leading ".", and no ".lock" suffix. These produce refs
// git itself would reject, so they are caught here for clearer errors.
func validGitTagName(s string) bool {
	if !validGitName(s, true) {
		return false
	}
	if strings.HasPrefix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.HasSuffix(s, ".lock") {
		return false
	}
	return true
}

// validateCloneSource checks where git_clone may read from. Remote URLs need
// an explicit allowlisted scheme. file:// URLs and scheme-less local paths
// read the host filesystem directly - bypassing the sandbox read roots - so
// they are accepted only when the *effective* sandbox (context override, else
// CurrentSandbox) is off: local runs where the operator/agent already acts
// with the user's filesystem trust. An active sandbox keeps clone sources
// strictly remote. The context form matters for operator-issued registry
// clones (DP-11): those run with a sandbox-off context, so a local bare
// remote stays usable even when the host runs an agent sandbox.
func validateCloneSource(ctx context.Context, source string) error {
	sb := ConfigFrom(ctx)
	s := strings.TrimSpace(source)
	if s == "" || strings.ContainsAny(s, "\x00\r\n") {
		return fmt.Errorf("invalid clone source")
	}
	// A scheme-less source beginning with "-" would be parsed by git as an
	// option (e.g. `clone -upload-pack ...`), never as a path.
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("invalid clone source %q: looks like a git option; use a URL or a path starting with ./", s)
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid clone source: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https", "http", "git", "ssh":
		return nil
	case "file":
		if sb == nil || sb.Mode == SandboxModeOff {
			return nil
		}
		return fmt.Errorf("file:// clone sources are not allowed while the sandbox is active (they bypass the sandbox read roots); clone from https://, git://, or ssh:// instead")
	case "":
		// Scheme-less input is a local path; same rule as file://.
		if sb == nil || sb.Mode == SandboxModeOff {
			return nil
		}
		return fmt.Errorf("local-path clone sources are not allowed while the sandbox is active (they bypass the sandbox read roots); clone from https://, git://, or ssh:// instead")
	default:
		return fmt.Errorf("unsupported clone scheme %q (allowed: https, git, ssh, http, and file:// or a local path when no sandbox is active)", u.Scheme)
	}
}

// gitCloneContent is the package-level handler for the git_clone tool.
func gitCloneContent(ctx context.Context, input GitCloneInput, log interfaces.LogFunc) (GitCloneOutput, error) {
	return cloneContent(ctx, input, log, false)
}

// OperatorClone clones through the same engine as git_clone but under direct
// operator authority (no interactive approval - the human issuing the command
// is the authorizer). Used by the project-registry materialization.
func OperatorClone(ctx context.Context, input GitCloneInput, log interfaces.LogFunc) (GitCloneOutput, error) {
	return cloneContent(ctx, input, log, true)
}

func cloneContent(ctx context.Context, input GitCloneInput, log interfaces.LogFunc, operator bool) (GitCloneOutput, error) {
	out := GitCloneOutput{}
	if err := validateCloneSource(ctx, input.URL); err != nil {
		return out, fmt.Errorf("git_clone: %w", err)
	}
	if strings.TrimSpace(input.Dir) == "" {
		return out, fmt.Errorf("git_clone: a target directory is required")
	}
	if input.Branch != "" && !validGitRef(input.Branch) {
		return out, fmt.Errorf("git_clone: invalid branch %q", input.Branch)
	}

	// Resolve the target through write containment, then let git create it.
	target, err := taskResolve(ctx, input.Dir, true, "")
	if err != nil {
		return out, err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return out, fmt.Errorf("git_clone: cannot create target parent: %w", err)
	}

	args := []string{"clone"}
	if input.Branch != "" {
		args = append(args, "--branch", input.Branch)
	}
	args = append(args, strings.TrimSpace(input.URL), filepath.Base(target))

	// clone is a mutating + network op: MEDIUM risk, approval-gated. The
	// target parent is the working directory; git receives the bare name so
	// the URL stays the only remote-controlled token in argv.
	run := runGit
	if operator {
		run = runGitOperator
	}
	res, err := run(ctx, parent, args, true, log)
	if err != nil {
		return out, err
	}
	out.Dir = wrapUntrustedData(target)
	out.Message = wrapUntrustedData(splitGitOut(res.Stderr))
	return out, nil
}

// gitPushContent is the package-level handler for the git_push tool.
func gitPushContent(ctx context.Context, input GitPushInput, log interfaces.LogFunc) (GitPushOutput, error) {
	out := GitPushOutput{}
	dir, err := resolveRepoDir(ctx, input.RepoDir, true)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	remote := strings.TrimSpace(input.Remote)
	if remote == "" {
		remote = "origin"
	}
	if !validGitRemote(remote) {
		return out, fmt.Errorf("git_push: invalid remote %q", remote)
	}
	branch := strings.TrimSpace(input.Branch)
	if branch != "" && !validGitRef(branch) {
		return out, fmt.Errorf("git_push: invalid branch %q", branch)
	}
	out.Remote = remote
	out.Branch = branch

	args := []string{"push"}
	if input.SetUpstream != nil && *input.SetUpstream {
		args = append(args, "--set-upstream")
	}
	args = append(args, remote)
	if branch != "" {
		args = append(args, branch)
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
	out.Message = wrapUntrustedData(splitGitOut(res.Stderr))
	return out, nil
}

// gitPullContent is the package-level handler for the git_pull tool.
func gitPullContent(ctx context.Context, input GitPullInput, log interfaces.LogFunc) (GitPullOutput, error) {
	return pullContent(ctx, input, log, false)
}

// OperatorPull mirrors git_pull under direct operator authority (see
// OperatorClone). Used by the project-registry sync.
func OperatorPull(ctx context.Context, input GitPullInput, log interfaces.LogFunc) (GitPullOutput, error) {
	return pullContent(ctx, input, log, true)
}

func pullContent(ctx context.Context, input GitPullInput, log interfaces.LogFunc, operator bool) (GitPullOutput, error) {
	out := GitPullOutput{}
	dir, err := resolveRepoDir(ctx, input.RepoDir, true)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	remote := strings.TrimSpace(input.Remote)
	if remote == "" {
		remote = "origin"
	}
	if !validGitRemote(remote) {
		return out, fmt.Errorf("git_pull: invalid remote %q", remote)
	}
	branch := strings.TrimSpace(input.Branch)
	if branch != "" && !validGitRef(branch) {
		return out, fmt.Errorf("git_pull: invalid branch %q", branch)
	}
	out.Remote = remote
	out.Branch = branch

	// --ff-only: an agent must never silently create merge commits. When the
	// branches diverged, git refuses and the model can choose a rebase/merge
	// workflow through system_exec with the user's approval.
	args := []string{"pull", "--ff-only"}
	args = append(args, remote)
	if branch != "" {
		args = append(args, branch)
	}
	run := runGit
	if operator {
		run = runGitOperator
	}
	res, err := run(ctx, dir, args, true, log)
	if err != nil {
		if isNotARepoErr(res) {
			out.NotARepo = true
			out.Stderr = wrapUntrustedData(res.Stderr)
			return out, nil
		}
		return out, err
	}
	out.Message = wrapUntrustedData(splitGitOut(res.Stderr))
	return out, nil
}

// ---------------------------------------------------------------------------
// Operator fetch / repo state (registry status, operator authority)
// ---------------------------------------------------------------------------

// GitFetchInput is the input for OperatorFetch.
type GitFetchInput struct {
	RepoDir string `json:"repo_dir,omitempty" doc:"Repository directory (defaults to the project root / working directory)"`
	Remote  string `json:"remote,omitempty"   doc:"Remote name to fetch from (defaults to origin)"`
}

// GitFetchOutput is the output of OperatorFetch.
type GitFetchOutput struct {
	RepoDir string `json:"repo_dir"`
	Remote  string `json:"remote,omitempty"`
	Message string `json:"message,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
}

// OperatorFetch updates the remote-tracking refs of a checkout (git fetch)
// under operator authority, without touching the working tree. Used by the
// registry status endpoint so "behind" counts reflect the remote; the engine's
// terminal-prompt disable makes an auth failure fail fast instead of hanging.
func OperatorFetch(ctx context.Context, input GitFetchInput, log interfaces.LogFunc) (GitFetchOutput, error) {
	return fetchContent(ctx, input, log, true)
}

func fetchContent(ctx context.Context, input GitFetchInput, log interfaces.LogFunc, operator bool) (GitFetchOutput, error) {
	out := GitFetchOutput{}
	dir, err := resolveRepoDir(ctx, input.RepoDir, true)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	remote := strings.TrimSpace(input.Remote)
	if remote == "" {
		remote = "origin"
	}
	if !validGitRemote(remote) {
		return out, fmt.Errorf("git_fetch: invalid remote %q", remote)
	}
	out.Remote = remote

	run := runGit
	if operator {
		run = runGitOperator
	}
	// --quiet keeps progress chatter off the bounded output; errors still land
	// on stderr.
	res, err := run(ctx, dir, []string{"fetch", "--quiet", remote}, true, log)
	if err != nil {
		return out, err
	}
	out.Message = wrapUntrustedData(splitGitOut(res.Stderr))
	return out, nil
}

// RepoState is a parsed `git status -sb` summary of one checkout, read under
// operator authority for the registry status endpoint (branch/upstream,
// ahead/behind, and the workspace-snapshot dirty counts).
type RepoState struct {
	Branch    string
	Upstream  string
	Ahead     int
	Behind    int
	Staged    int
	Modified  int
	Untracked int
	Conflicts int
	Stderr    string
}

// OperatorRepoState reports the current branch/upstream, ahead/behind, and
// working-tree counts of the repo at repoDir without consulting the approval
// gate (the operator issued the read). Values are raw repo strings - callers
// that surface them to a human model should treat them as untrusted.
func OperatorRepoState(ctx context.Context, repoDir string, log interfaces.LogFunc) (RepoState, error) {
	var out RepoState
	dir, err := resolveRepoDir(ctx, repoDir, false)
	if err != nil {
		return out, err
	}
	res, err := runGitOperator(ctx, dir, []string{"status", "--porcelain=v1", "-b"}, false, log)
	if err != nil {
		return out, err
	}
	out.Stderr = res.Stderr
	branch, upstream, ahead, behind, entries := parsePorcelainStatus(res.Stdout)
	out.Branch = branch
	out.Upstream = upstream
	out.Ahead = ahead
	out.Behind = behind
	out.Staged, out.Modified, out.Untracked, out.Conflicts = countGitEntries(entries)
	return out, nil
}

// ---------------------------------------------------------------------------
// git_checkout / git_reset / git_clean (destructive operations)
// ---------------------------------------------------------------------------

// GitCheckoutInput is the input schema for the git_checkout tool.
type GitCheckoutInput struct {
	RepoDir string `json:"repo_dir,omitempty" doc:"Repository directory (defaults to the project root / working directory)"`
	Branch  string `json:"branch,omitempty"   doc:"Branch to switch to (mutually exclusive with path)"`
	Create  *bool  `json:"create,omitempty"   doc:"Create the branch first (git checkout -b; defaults to false)"`
	Path    string `json:"path,omitempty"     doc:"Repository-relative path to restore from the index (mutually exclusive with branch)"`
}

// GitCheckoutOutput is the output schema of the git_checkout tool.
type GitCheckoutOutput struct {
	RepoDir  string `json:"repo_dir"`
	Branch   string `json:"branch,omitempty"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message,omitempty" doc:"Bounded git output (wrapped as untrusted data)"`
	NotARepo bool   `json:"not_a_repo,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// GitResetInput is the input schema for the git_reset tool.
type GitResetInput struct {
	RepoDir string `json:"repo_dir,omitempty" doc:"Repository directory (defaults to the project root / working directory)"`
	Mode    string `json:"mode,omitempty"     doc:"Reset mode: soft, mixed, or hard (defaults to mixed; hard destroys working-tree changes and is HIGH-risk)"`
	Ref     string `json:"ref,omitempty"      doc:"Commit to reset to (defaults to HEAD)"`
}

// GitResetOutput is the output schema of the git_reset tool.
type GitResetOutput struct {
	RepoDir  string `json:"repo_dir"`
	Mode     string `json:"mode"`
	Ref      string `json:"ref,omitempty"`
	Message  string `json:"message,omitempty" doc:"Bounded git output (wrapped as untrusted data)"`
	NotARepo bool   `json:"not_a_repo,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// GitCleanInput is the input schema for the git_clean tool.
type GitCleanInput struct {
	RepoDir        string   `json:"repo_dir,omitempty"        doc:"Repository directory (defaults to the project root / working directory)"`
	DryRun         *bool    `json:"dry_run,omitempty"         doc:"Only list what would be removed (git clean -n; defaults to false)"`
	IncludeDirs    *bool    `json:"include_dirs,omitempty"    doc:"Also remove untracked directories (git clean -d; defaults to false)"`
	IncludeIgnored *bool    `json:"include_ignored,omitempty" doc:"Also remove ignored files (git clean -x; defaults to false)"`
	Paths          []string `json:"paths,omitempty"           doc:"Optional repository-relative paths to limit the clean to"`
}

// GitCleanOutput is the output schema of the git_clean tool.
type GitCleanOutput struct {
	RepoDir  string   `json:"repo_dir"`
	Removed  []string `json:"removed,omitempty" doc:"Paths the clean removed (or would remove with dry_run)"`
	NotARepo bool     `json:"not_a_repo,omitempty"`
	Stderr   string   `json:"stderr,omitempty"`
}

// validRelRepoPath validates a repository-relative path input: no absolute
// forms, no NUL, no traversal segments. Returns the cleaned path.
func validRelRepoPath(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("invalid path")
	}
	clean := filepath.Clean(filepath.FromSlash(p))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path must stay inside the repository")
	}
	return clean, nil
}

// gitCheckoutContent is the package-level handler for the git_checkout tool.
func gitCheckoutContent(ctx context.Context, input GitCheckoutInput, log interfaces.LogFunc) (GitCheckoutOutput, error) {
	out := GitCheckoutOutput{}
	branch := strings.TrimSpace(input.Branch)
	path := strings.TrimSpace(input.Path)
	if (branch == "") == (path == "") {
		return out, fmt.Errorf("git_checkout: exactly one of branch or path is required")
	}
	if branch != "" && !validGitRef(branch) {
		return out, fmt.Errorf("git_checkout: invalid branch %q", branch)
	}

	dir, err := resolveRepoDir(ctx, input.RepoDir, true)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	// Path restores resolve against the index; the working tree content the
	// path currently holds is overwritten, so this is destructive by design
	// and stays approval-gated. Never passes -f: without it git refuses to
	// switch branches when it would clobber uncommitted changes (D13).
	var args []string
	switch {
	case branch != "":
		if input.Create != nil && *input.Create {
			args = []string{"checkout", "-b", branch}
		} else {
			args = []string{"checkout", branch}
		}
		out.Branch = branch
	case path != "":
		cleanPath, perr := validRelRepoPath(path)
		if perr != nil {
			return out, fmt.Errorf("git_checkout: %w", perr)
		}
		args = []string{"checkout", "--", cleanPath}
		out.Path = cleanPath
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
	if msg := splitGitOut(res.Stdout); msg != "" {
		out.Message = wrapUntrustedData(msg)
	} else {
		out.Message = wrapUntrustedData(splitGitOut(res.Stderr))
	}
	return out, nil
}

// gitResetContent is the package-level handler for the git_reset tool.
func gitResetContent(ctx context.Context, input GitResetInput, log interfaces.LogFunc) (GitResetOutput, error) {
	out := GitResetOutput{}
	mode := strings.ToLower(strings.TrimSpace(input.Mode))
	if mode == "" {
		mode = "mixed"
	}
	switch mode {
	case "soft", "mixed", "hard":
	default:
		return out, fmt.Errorf("git_reset: mode must be soft, mixed, or hard (got %q)", input.Mode)
	}
	ref := strings.TrimSpace(input.Ref)
	if ref != "" && !validGitRevision(ref) {
		return out, fmt.Errorf("git_reset: invalid ref %q", ref)
	}

	dir, err := resolveRepoDir(ctx, input.RepoDir, true)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir
	out.Mode = mode
	out.Ref = ref

	// hard passes through verbatim so classifyGitRisk sees --hard and the
	// approval card shows the real command (D14).
	args := []string{"reset"}
	if mode != "mixed" {
		args = append(args, "--"+mode)
	}
	if ref != "" {
		args = append(args, ref)
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
	out.Message = wrapUntrustedData(splitGitOut(res.Stdout))
	return out, nil
}

// gitCleanContent is the package-level handler for the git_clean tool.
func gitCleanContent(ctx context.Context, input GitCleanInput, log interfaces.LogFunc) (GitCleanOutput, error) {
	out := GitCleanOutput{}
	dir, err := resolveRepoDir(ctx, input.RepoDir, true)
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	args := []string{"clean"}
	dryRun := input.DryRun != nil && *input.DryRun
	if dryRun {
		args = append(args, "-n")
	} else {
		args = append(args, "-f")
	}
	if input.IncludeDirs != nil && *input.IncludeDirs {
		args = append(args, "-d")
	}
	if input.IncludeIgnored != nil && *input.IncludeIgnored {
		args = append(args, "-x")
	}
	if len(input.Paths) > 0 {
		cleaned := make([]string, 0, len(input.Paths))
		for _, p := range input.Paths {
			cp, perr := validRelRepoPath(p)
			if perr != nil {
				return out, fmt.Errorf("git_clean: %w", perr)
			}
			cleaned = append(cleaned, cp)
		}
		args = append(args, "--")
		args = append(args, cleaned...)
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
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		// clean prints one line per path: "Removing X" (-f) or
		// "Would remove X" (-n). Keep the path, skip the rest.
		switch {
		case strings.HasPrefix(line, "Removing "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "Removing "))
		case strings.HasPrefix(line, "Would remove "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "Would remove "))
		}
		if line != "" {
			out.Removed = append(out.Removed, wrapUntrustedData(line))
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// git_stash / git_tag (workspace hygiene, v3 - Phase 7 first slice)
// ---------------------------------------------------------------------------

// GitStashInput is the input schema for the git_stash tool.
type GitStashInput struct {
	RepoDir          string `json:"repo_dir,omitempty"     doc:"Repository directory (defaults to the project root / working directory)"`
	Operation        string `json:"operation"              doc:"Operation: list, push, or pop"`
	Message          string `json:"message,omitempty"      doc:"Push message (git stash push -m; push only)"`
	IncludeUntracked *bool  `json:"include_untracked,omitempty" doc:"Also stash untracked files (git stash push -u; push only)"`
	Stash            string `json:"stash,omitempty"        doc:"Which stash entry to pop, e.g. stash@{1} (defaults to the newest)"`
}

// GitStashOutput is the output schema of the git_stash tool.
type GitStashOutput struct {
	RepoDir   string   `json:"repo_dir"`
	Operation string   `json:"operation"`
	Stashes   []string `json:"stashes,omitempty" doc:"Stash entries (list operation only)"`
	Message   string   `json:"message,omitempty" doc:"Bounded git output (wrapped as untrusted data)"`
	NotARepo  bool     `json:"not_a_repo,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
}

// GitTagInput is the input schema for the git_tag tool.
type GitTagInput struct {
	RepoDir   string `json:"repo_dir,omitempty" doc:"Repository directory (defaults to the project root / working directory)"`
	Operation string `json:"operation"          doc:"Operation: list, create, or delete"`
	Name      string `json:"name,omitempty"     doc:"Tag name (create/delete)"`
	Message   string `json:"message,omitempty"  doc:"Annotated-tag message (create only; absent creates a lightweight tag)"`
	Ref       string `json:"ref,omitempty"      doc:"Commit-ish the tag points at (create only; defaults to HEAD)"`
}

// GitTagOutput is the output schema of the git_tag tool.
type GitTagOutput struct {
	RepoDir   string   `json:"repo_dir"`
	Operation string   `json:"operation"`
	Tags      []string `json:"tags,omitempty" doc:"Tag names (list operation only)"`
	Message   string   `json:"message,omitempty" doc:"Bounded git output (wrapped as untrusted data)"`
	NotARepo  bool     `json:"not_a_repo,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
}

// gitStashContent is the package-level handler for the git_stash tool.
func gitStashContent(ctx context.Context, input GitStashInput, log interfaces.LogFunc) (GitStashOutput, error) {
	out := GitStashOutput{}
	op := strings.TrimSpace(input.Operation)
	switch op {
	case "list", "push", "pop":
	default:
		return out, fmt.Errorf("git_stash: unsupported operation %q (allowed: list, push, pop)", input.Operation)
	}
	out.Operation = op

	// list only reads; push/pop move working-tree state.
	dir, err := resolveRepoDir(ctx, input.RepoDir, op != "list")
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	args := []string{"stash"}
	switch op {
	case "list":
		args = append(args, "list")
	case "push":
		args = append(args, "push")
		if msg := strings.TrimSpace(input.Message); msg != "" {
			args = append(args, "-m", msg)
		}
		if input.IncludeUntracked != nil && *input.IncludeUntracked {
			args = append(args, "-u")
		}
	case "pop":
		args = append(args, "pop")
		if stash := strings.TrimSpace(input.Stash); stash != "" {
			if !validGitRevision(stash) {
				return out, fmt.Errorf("git_stash: invalid stash ref %q", stash)
			}
			args = append(args, stash)
		}
	}

	res, err := runGit(ctx, dir, args, op != "list", log)
	if err != nil {
		if isNotARepoErr(res) {
			out.NotARepo = true
			out.Stderr = wrapUntrustedData(res.Stderr)
			return out, nil
		}
		return out, err
	}
	if op == "list" {
		for _, line := range strings.Split(res.Stdout, "\n") {
			line = strings.TrimSpace(strings.TrimRight(line, "\r"))
			if line != "" {
				out.Stashes = append(out.Stashes, wrapUntrustedData(line))
			}
		}
	} else {
		out.Message = wrapUntrustedData(firstGitMessage(res))
	}
	return out, nil
}

// firstGitMessage returns the operation's informational line, preferring
// stderr (where git usually writes progress) but falling back to stdout -
// some operations (e.g. tag delete) report on stdout.
func firstGitMessage(res gitResult) string {
	if msg := splitGitOut(res.Stderr); msg != "" {
		return msg
	}
	return splitGitOut(res.Stdout)
}

// gitTagContent is the package-level handler for the git_tag tool.
func gitTagContent(ctx context.Context, input GitTagInput, log interfaces.LogFunc) (GitTagOutput, error) {
	out := GitTagOutput{}
	op := strings.TrimSpace(input.Operation)
	switch op {
	case "list", "create", "delete":
	default:
		return out, fmt.Errorf("git_tag: unsupported operation %q (allowed: list, create, delete)", input.Operation)
	}
	out.Operation = op
	if op != "create" && strings.TrimSpace(input.Ref) != "" {
		return out, fmt.Errorf("git_tag: ref is only valid for the create operation")
	}

	dir, err := resolveRepoDir(ctx, input.RepoDir, op != "list")
	if err != nil {
		return out, err
	}
	out.RepoDir = dir

	name := strings.TrimSpace(input.Name)
	args := []string{"tag"}
	switch op {
	case "list":
		args = append(args, "--list")
	case "create":
		if !validGitTagName(name) {
			return out, fmt.Errorf("git_tag: invalid tag name %q", name)
		}
		if msg := strings.TrimSpace(input.Message); msg != "" {
			// Annotated tag (has a message) vs lightweight (name only).
			args = append(args, "-a", name, "-m", msg)
		} else {
			args = append(args, name)
		}
		if ref := strings.TrimSpace(input.Ref); ref != "" {
			if !validGitRevision(ref) {
				return out, fmt.Errorf("git_tag: invalid ref %q", ref)
			}
			args = append(args, ref)
		}
	case "delete":
		if !validGitTagName(name) {
			return out, fmt.Errorf("git_tag: invalid tag name %q", name)
		}
		args = append(args, "-d", name)
	}

	res, err := runGit(ctx, dir, args, op != "list", log)
	if err != nil {
		if isNotARepoErr(res) {
			out.NotARepo = true
			out.Stderr = wrapUntrustedData(res.Stderr)
			return out, nil
		}
		return out, err
	}
	if op == "list" {
		for _, line := range strings.Split(res.Stdout, "\n") {
			line = strings.TrimSpace(strings.TrimRight(line, "\r"))
			if line != "" {
				out.Tags = append(out.Tags, wrapUntrustedData(line))
			}
		}
	} else {
		out.Message = wrapUntrustedData(firstGitMessage(res))
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Tool registration
// ---------------------------------------------------------------------------

// CreateGitOpsTools builds the fourteen-tool git toolset shared by the
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

	cloneTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_clone",
		Description: "Clones a remote repository into a target directory (https://, git://, ssh:// URLs; file:// or a local path only when no sandbox is active). Mutating + network: approval-gated like system_exec.",
	}, func(ctx agent.Context, input GitCloneInput) (GitCloneOutput, error) {
		return gitCloneContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	pushTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_push",
		Description: "Pushes commits to a remote (default remote origin; set_upstream=true adds --set-upstream). Mutating + network: approval-gated like system_exec. Force-push is intentionally not exposed - use system_exec with explicit approval for that.",
	}, func(ctx agent.Context, input GitPushInput) (GitPushOutput, error) {
		return gitPushContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	pullTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_pull",
		Description: "Fast-forwards the current branch from a remote (git pull --ff-only; default remote origin). Refuses to create merge commits - diverged branches need an explicit rebase/merge workflow via system_exec. Mutating + network: approval-gated like system_exec.",
	}, func(ctx agent.Context, input GitPullInput) (GitPullOutput, error) {
		return gitPullContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	checkoutTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_checkout",
		Description: "Switches to a branch (create=true makes git checkout -b) or restores one repository-relative path from the index (checkout -- path; overwrites local edits to that path). Mutating: approval-gated like system_exec. Force flags are never passed - git itself refuses switches that would clobber uncommitted changes.",
	}, func(ctx agent.Context, input GitCheckoutInput) (GitCheckoutOutput, error) {
		return gitCheckoutContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	resetTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_reset",
		Description: "Resets the current branch to a ref (default HEAD): mode=mixed unstages (default), soft moves HEAD only, hard ALSO destroys working-tree changes and is HIGH-risk - approval-gated like system_exec, never silent.",
	}, func(ctx agent.Context, input GitResetInput) (GitResetOutput, error) {
		return gitResetContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	cleanTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_clean",
		Description: "Removes untracked files (git clean -f; dry_run=true only lists them with -n). include_dirs/-ignored add -d/-x and their combination is HIGH-risk. Only untracked data is ever touched. Mutating: approval-gated like system_exec.",
	}, func(ctx agent.Context, input GitCleanInput) (GitCleanOutput, error) {
		return gitCleanContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	stashTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_stash",
		Description: "Stashes uncommitted changes away and brings them back: list shows the stash, push saves the working tree (message + include_untracked supported), pop restores a saved stash (default the newest). Mutating for push/pop; approval-gated like system_exec. Conflicts on pop are left in the working tree by git and reported in the message.",
	}, func(ctx agent.Context, input GitStashInput) (GitStashOutput, error) {
		return gitStashContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	tagTool, err := util.NewDocTool(functiontool.Config{
		Name:        "git_tag",
		Description: "Manages tags: list shows the repository tags, create makes a lightweight tag (or an annotated one when message is set) at a commit-ish ref (default HEAD), delete removes a local tag (-d). Mutating for create/delete; approval-gated like system_exec.",
	}, func(ctx agent.Context, input GitTagInput) (GitTagOutput, error) {
		return gitTagContent(ctx, input, log)
	})
	if err != nil {
		return nil, err
	}

	return []tool.Tool{statusTool, diffTool, logTool, branchTool, stageTool, commitTool, cloneTool, pushTool, pullTool, checkoutTool, resetTool, cleanTool, stashTool, tagTool}, nil
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
	dir, err := resolveRepoDir(ctx, repoDir, false)
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
