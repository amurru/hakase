// Package agentrun drives a single agent turn against the ADK runner,
// transport-neutral: it persists the reply, handles project-bound sessions
// and sandbox pinning, repairs malformed tool calls, and reports progress
// through an EventSink. Consumers are the web chat handler (sink = SSE
// bridge) and the communication channels (sink = chat status updates);
// both previously (or would otherwise have to) carry their own copy of this
// loop, as the TUI and cron runner still do.
package agentrun

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/project"
	"amurru/hakase/internal/registry"
	"amurru/hakase/internal/sandbox"
	hakasesession "amurru/hakase/internal/session"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"
)

// EventSink receives progress from a turn. It mirrors the sse.EventBridge
// senders the web handler used, so transports map it onto their own output
// (SSE frames, chat status messages, ...).
type EventSink interface {
	// OnStream reports a text delta: content for the answer, thinking for
	// reasoning deltas (either may be empty).
	OnStream(sessionID, content, thinking string)
	// OnLog reports one activity line (tool call/response, errors).
	OnLog(sessionID, line string)
	// OnUsage reports a token usage update.
	OnUsage(sessionID string, tokens, percent int)
	// OnDone signals the turn has completed (success, error, or panic).
	OnDone(sessionID string)
}

// Driver runs agent turns against the ADK runner and persists results to the
// session store. Safe for concurrent use; per-transport concurrency limits
// are the caller's concern (the web handler keeps its per-session semaphore).
type Driver struct {
	Runner   *runner.Runner
	Sessions *hakasesession.SessionService
}

// New creates a Driver.
func New(r *runner.Runner, s *hakasesession.SessionService) *Driver {
	return &Driver{Runner: r, Sessions: s}
}

// ActiveProjectRuns tracks in-flight agent runs per registered project id so
// the registry endpoints refuse to sync/delete a checkout an agent is
// actively working in (project-ui.md). Counts, not booleans: several sessions
// may run against the same project concurrently. Follows the package-global
// precedent of registry.Current.
var ActiveProjectRuns = newProjectRunTracker()

// projectRunTracker counts active agent runs per project id.
type projectRunTracker struct {
	mu    sync.Mutex
	count map[string]int
}

func newProjectRunTracker() *projectRunTracker {
	return &projectRunTracker{count: map[string]int{}}
}

func (t *projectRunTracker) Begin(projectID string) {
	if projectID == "" {
		return
	}
	t.mu.Lock()
	t.count[projectID]++
	t.mu.Unlock()
}

func (t *projectRunTracker) End(projectID string) {
	if projectID == "" {
		return
	}
	t.mu.Lock()
	if n := t.count[projectID]; n <= 1 {
		delete(t.count, projectID)
	} else {
		t.count[projectID] = n - 1
	}
	t.mu.Unlock()
}

// CountOn returns how many agent runs are active on projectID.
func (t *projectRunTracker) CountOn(projectID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count[projectID]
}

// boundProject resolves the registered project a chat session is bound to
// (session.project_id, project-registry DP-7). Returns nil when the session is
// unbound or the binding is unusable (entry missing, or not in status ready),
// so a run always proceeds - just without a project anchor.
func (d *Driver) boundProject(sessionID string) *registry.Project {
	if d.Sessions == nil || registry.Current == nil {
		return nil
	}
	sess, err := d.Sessions.Store().Load(sessionID)
	if err != nil || strings.TrimSpace(sess.ProjectID) == "" {
		return nil
	}
	p, err := registry.Current.Store().Get(sess.ProjectID)
	if err != nil {
		log.Printf("agentrun: session %s bound to unknown project %s: %v", sessionID, sess.ProjectID, err)
		return nil
	}
	if p.Status != registry.StatusReady {
		log.Printf("agentrun: session %s bound to project %s in status %s; skipping project anchor", sessionID, p.Name, p.Status)
		return nil
	}
	return &p
}

// WithBoundSandbox pins the effective sandbox of a project-bound run to the
// checkout (sandbox.PinnedTo -> sandbox.WithConfig), closing DP-7: while the
// host sandbox is active, the session's workspace/read roots are the project
// checkout. When the host sandbox is off nothing changes - confinement
// disabled stays disabled and the checkout is simply the git project root.
func WithBoundSandbox(ctx context.Context, checkout string) context.Context {
	base := sandbox.CurrentSandbox
	if base == nil || base.Mode == sandbox.SandboxModeOff {
		return ctx
	}
	return sandbox.WithConfig(ctx, sandbox.PinnedTo(base, checkout))
}

// projectWorkspaceSnapshot renders a fresh GIT WORKSPACE snapshot for a bound
// project checkout. Mirrors agent.SetupRunner's boot-time snapshot (status +
// latest commits) but reflects the session's project at run start. ctx carries
// the bound project root/sandbox so the snapshot resolves under confinement.
func projectWorkspaceSnapshot(ctx context.Context, checkout string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return sandbox.BuildGitWorkspaceBlock(runCtx, checkout, nil)
}

// RunTurn runs one agent turn for sessionID with the given user content,
// streaming progress to sink and persisting the final answer. It never
// panics past its own frame: a panic in the runner is recovered, persisted
// (partial output), and reported as done. ctx should be detached from any
// request lifecycle by the caller - cancelling it cancels the run.
func (d *Driver) RunTurn(ctx context.Context, sessionID string, content *genai.Content, sink EventSink) {
	var contentBuf, thinkBuf strings.Builder
	var lastUsage *genai.GenerateContentResponseUsageMetadata

	// boundProject is resolved below (after the defer is installed), so keep
	// the resolved id here: the defer releases the active-run slot exactly
	// once, whether the run ends normally, on error, or on a panic.
	var runProjectID string

	defer func() {
		if runProjectID != "" {
			ActiveProjectRuns.End(runProjectID)
		}
		if r := recover(); r != nil {
			log.Printf("agentrun: panic in agent run for session %s: %v", sessionID, r)
			d.persistAgentResponse(sessionID, contentBuf.String(), thinkBuf.String(), lastUsage)
			sink.OnDone(sessionID)
		}
	}()

	runCtx := ctx
	msg := content

	// Project-bound sessions (project-registry DP-7): anchor the run to the
	// registered project's checkout so git tools default there (resolveRepoDir
	// consults project.RootFrom), pin the effective sandbox to the checkout
	// when the host sandbox is active (per-run workspace roots), and inject a
	// fresh per-session GIT WORKSPACE snapshot ahead of the user message.
	// Delegated runs inherit the ctx root and sandbox override (delegate.go
	// reuses the agent.Context as the sub-run ctx).
	if bind := d.boundProject(sessionID); bind != nil {
		// Register the active run against the bound project before the first
		// agent step so the registry refuses to sync/delete under it.
		runProjectID = bind.ID
		ActiveProjectRuns.Begin(bind.ID)
		checkout := registry.Current.Store().CheckoutDir(*bind)
		runCtx = project.WithRoot(runCtx, checkout)
		runCtx = WithBoundSandbox(runCtx, checkout)
		if snap, serr := projectWorkspaceSnapshot(runCtx, checkout); serr == nil && strings.TrimSpace(snap) != "" {
			header := fmt.Sprintf("\n### GIT WORKSPACE — session bound to registered project %q\n(checkout: %s)\n%s", bind.Name, checkout, snap)
			msg = &genai.Content{
				Role:  content.Role,
				Parts: append([]*genai.Part{{Text: header}}, content.Parts...),
			}
		}
	}

	// Generate task ID once before the retry loop so all repair attempts
	// preserve the same session context. The task doubles as the ADK session
	// id, so register the hakase session under it: gate prompts raised inside
	// tool execution resolve their session through it (gate prompt routing).
	taskID := hakasesession.GenerateTaskID()
	interfaces.RegisterTaskSession(taskID, sessionID)
	defer interfaces.UnregisterTask(taskID)

outer:
	for attempt := 0; ; attempt++ {
		var parseErr error
		for ev, err := range d.Runner.Run(runCtx, "user-1", taskID, msg, adkagent.RunConfig{}) {
			if err != nil {
				// Malformed tool-call JSON: re-enter the runner with a
				// corrective user message instead of aborting the run. This
				// mirrors internal/tui/ui.go:runAgentTask and
				// internal/agent/delegate.go so every transport survives the
				// same provider hiccup.
				if hakaseagent.IsToolCallJSONErr(err) && attempt < hakaseagent.MaxToolCallRepairAttempts {
					parseErr = err
					break
				}
				log.Printf("agentrun: agent error for session %s: %v", sessionID, err)
				sink.OnLog(sessionID, fmt.Sprintf("Error: %v", err))
				break outer
			}
			if ev == nil {
				continue
			}
			// Send usage update.
			if ev.UsageMetadata != nil {
				lastUsage = ev.UsageMetadata
				tokens := int(ev.UsageMetadata.TotalTokenCount)
				if tokens <= 0 {
					tokens = int(ev.UsageMetadata.PromptTokenCount + ev.UsageMetadata.CandidatesTokenCount)
				}
				sink.OnUsage(sessionID, tokens, 0)
			}
			if ev.Content != nil {
				for _, part := range ev.Content.Parts {
					if part.Text != "" {
						if part.Thought {
							thinkBuf.WriteString(part.Text)
							sink.OnStream(sessionID, "", part.Text)
						} else {
							contentBuf.WriteString(part.Text)
							sink.OnStream(sessionID, part.Text, "")
						}
					}
					if part.FunctionCall != nil {
						sink.OnLog(sessionID,
							fmt.Sprintf("Call: %s(%v)", part.FunctionCall.Name, part.FunctionCall.Args),
						)
					}
					if part.FunctionResponse != nil {
						sink.OnLog(sessionID,
							fmt.Sprintf("Response: %s", part.FunctionResponse.Name),
						)
					}
				}
			}
		}
		if parseErr != nil {
			log.Printf("agentrun: tool call repair for session %s (attempt %d): %v", sessionID, attempt+1, parseErr)
			msg = hakaseagent.ToolCallRepairMessage(parseErr, attempt)
			continue
		}
		break
	}
	d.persistAgentResponse(sessionID, contentBuf.String(), thinkBuf.String(), lastUsage)
	sink.OnDone(sessionID)
}

// persistAgentResponse saves the agent's answer to the session store so a UI
// can render it after a reload. The message is appended to the session
// identified by sessionID directly (not the active session) so concurrent
// runs in different sessions cannot misroute replies. A run that produced no
// text (e.g. it only made tool calls and then errored) writes nothing.
func (d *Driver) persistAgentResponse(sessionID, content, thinking string, usage *genai.GenerateContentResponseUsageMetadata) {
	if d.Sessions == nil || strings.TrimSpace(content) == "" && strings.TrimSpace(thinking) == "" {
		return
	}
	tokens := 0
	if usage != nil {
		tokens = int(usage.TotalTokenCount)
		if tokens <= 0 {
			tokens = int(usage.PromptTokenCount + usage.CandidatesTokenCount)
		}
	}
	store := d.Sessions.Store()
	sess, err := store.Load(sessionID)
	if err != nil {
		log.Printf("agentrun: warning: failed to load session %s for persistence: %v", sessionID, err)
		return
	}
	sess.AddMessageWithMetaAndAttachments("agent", content, thinking, tokens, hakasesession.MessageKindText, nil)
	if err := store.Save(sess); err != nil {
		log.Printf("agentrun: warning: failed to persist agent reply for session %s: %v", sessionID, err)
	}
}
