// replay_agent.go - agentic replay for markdown-skill evolution (plan SL-011).
//
// Single-shot replay cannot observe tool behavior, so skills whose value is
// tool use (read the right file, touch nothing else) replay here: an
// isolated llmagent + runner with an exhaustive allowlist of read_file +
// vision, nil toolsets (MCP and web-search fallback explicitly off), and a
// per-task timeout. The shape mirrors delegateTaskHandler without its cache,
// gate routing, or interactive tools: replay never carries clarify,
// delegate_task, cronjob, memory, or send_message, and it refuses to start
// when the sandbox is absent or off (audit M1).
package sleep

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/skill"
	"amurru/hakase/internal/vision"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// replayAllowTools is the exhaustive agentic-replay allowlist (plan
// SL-011b). Anything not listed here must never reach a replay agent:
// write_file, patch, system_exec, python, download, and every
// interactive/delegation tool are out.
var replayAllowTools = map[string]bool{
	"read_file": true,
	"vision":    true,
}

// ReplayAudit counts deny-by-default events observed during agentic replay
// (plan SL-040): attempts to call tools the replay agent does not carry,
// and sandbox path refusals surfacing through tool responses. It is
// visibility for the staged diagnostics, not a control - the allowlist and
// sandbox are the controls.
type ReplayAudit struct {
	mu                 sync.Mutex
	DeniedToolAttempts map[string]int `json:"denied_tool_attempts,omitempty"`
	SandboxPathDenials int            `json:"sandbox_path_denials,omitempty"`
}

func (a *ReplayAudit) addToolAttempt(name string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.DeniedToolAttempts == nil {
		a.DeniedToolAttempts = make(map[string]int)
	}
	a.DeniedToolAttempts[name]++
}

func (a *ReplayAudit) addSandboxDenial() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SandboxPathDenials++
}

// snapshot returns a plain copy for storage in results.
func (a *ReplayAudit) snapshot() DeniedToolAttempts {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := DeniedToolAttempts{
		DeniedToolAttempts: make(map[string]int, len(a.DeniedToolAttempts)),
		SandboxPathDenials: a.SandboxPathDenials,
	}
	for k, v := range a.DeniedToolAttempts {
		out.DeniedToolAttempts[k] = v
	}
	return out
}

// DeniedToolAttempts is the diagnostic shape staged with a consolidation:
// which off-allowlist tools the replay probe tried to call (and how often),
// plus how many sandbox path refusals fired.
type DeniedToolAttempts struct {
	DeniedToolAttempts map[string]int `json:"denied_tool_attempts,omitempty"`
	SandboxPathDenials int            `json:"sandbox_path_denials,omitempty"`
}

// sandbox refusal markers from sandbox.ResolveScopedPath (an audit count,
// not a control; drift only undercounts).
var sandboxDenialMarkers = []string{
	"denied root", "denied sensitive file", "outside approved",
}

// auditKey carries the per-group audit through the replay ctx.
type auditKeyType struct{}

var auditKey auditKeyType

// withReplayAudit attaches an audit to ctx for runGroup to collect.
func withReplayAudit(ctx context.Context, audit *ReplayAudit) context.Context {
	return context.WithValue(ctx, auditKey, audit)
}

// auditFrom returns the ctx audit or nil (single-shot replay: no tools).
func auditFrom(ctx context.Context) *ReplayAudit {
	audit, _ := ctx.Value(auditKey).(*ReplayAudit)
	return audit
}

// AgenticOpts configures one agentic replay runner.
type AgenticOpts struct {
	// Model is the target model. Required: replay never falls back to a
	// global, so a nil model fails closed at construction.
	Model model.LLM
	// Log routes tool diagnostics. Nil logs drop (tests).
	Log interfaces.LogFunc
	// Timeout bounds one task replay. Zero uses DefaultReplayTimeout.
	Timeout time.Duration
	// MaxToolCalls is the per-task tool-call budget (plan M2
	// sleep.max_tool_calls_per_task). Zero uses DefaultMaxToolCallsPerTask.
	// A run that exceeds the budget aborts with an error (scored 0), never
	// silently.
	MaxToolCalls int
}

// DefaultMaxToolCallsPerTask bounds one agentic replay when no explicit
// budget is configured.
const DefaultMaxToolCallsPerTask = 50

// buildReplayTools assembles the allowlisted tool list: read_file from the
// sandbox file ops (MCP/toolsets excluded by construction) plus vision.
// Non-allowlisted tools from either source are dropped, never attached.
func buildReplayTools(log interfaces.LogFunc) ([]tool.Tool, error) {
	if log == nil {
		log = func(string) {}
	}
	fileTools, err := sandbox.CreateFileOpsTools(log, nil, "")
	if err != nil {
		return nil, fmt.Errorf("replay file tools: %w", err)
	}
	visionTool, err := vision.CreateVisionTool()
	if err != nil {
		return nil, fmt.Errorf("replay vision tool: %w", err)
	}
	var tools []tool.Tool
	for _, t := range append(fileTools, visionTool) {
		if replayAllowTools[t.Name()] {
			tools = append(tools, t)
		}
	}
	names := make(map[string]bool, len(tools))
	for _, t := range tools {
		names[t.Name()] = true
	}
	if !names["read_file"] {
		return nil, fmt.Errorf("replay allowlist incomplete: read_file missing")
	}
	return tools, nil
}

// AgenticTarget builds a TargetRunner over an isolated runner. The skill
// body becomes the sub-agent instruction; the task intent+context is the
// user message. Tool calls observed during the run feed rule judges.
func AgenticTarget(opts AgenticOpts) (TargetRunner, error) {
	if opts.Model == nil {
		return nil, fmt.Errorf("agentic replay requires a model (nil fails closed)")
	}
	if sandbox.CurrentSandbox == nil || sandbox.CurrentSandbox.Mode == sandbox.SandboxModeOff {
		return nil, fmt.Errorf("agentic replay requires an active sandbox (paths minimum)")
	}
	tools, err := buildReplayTools(opts.Log)
	if err != nil {
		return nil, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultReplayTimeout
	}
	maxCalls := opts.MaxToolCalls
	if maxCalls <= 0 {
		maxCalls = DefaultMaxToolCallsPerTask
	}
	return func(ctx context.Context, skillBody string, task skill.MarkdownTask) (string, []string, error) {
		return runAgenticReplay(ctx, opts.Model, tools, timeout, maxCalls, skillBody, task)
	}, nil
}

// runAgenticReplay executes one task in a throwaway runner: fresh agent,
// fresh InMemoryService, unique AppName, no history injection, no gate
// routing. Text accumulates as the response; function-call names accumulate
// for tool_called judges. Provider hiccups surface as errors (scored 0 by
// ReplayBatch), never as passing text. maxToolCalls is the spend guard from
// plan M2: exceeding it aborts the task with an error.
func runAgenticReplay(ctx context.Context, llm model.LLM, tools []tool.Tool, timeout time.Duration, maxToolCalls int, skillBody string, task skill.MarkdownTask) (string, []string, error) {
	sub, err := llmagent.New(llmagent.Config{
		Name:        "sleep_replay",
		Description: "Offline replay sub-agent for skill validation. Runs one task and returns text.",
		Instruction: "You are an offline replay probe. Follow the skill document, complete the task, and reply with your final answer as text.\n\n## Skill\n" + skillBody,
		Model:       llm,
		Tools:       tools,
		// No Toolsets: MCP and web-search fallback stay off in replay.
		GenerateContentConfig: &genai.GenerateContentConfig{},
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{
			vision.VisionInjectionCallback,
			hakaseagent.ToolResultGuard,
		},
	})
	if err != nil {
		return "", nil, fmt.Errorf("replay agent: %w", err)
	}
	r, err := runner.New(runner.Config{
		AppName:           fmt.Sprintf("hakase_sleep_replay_%d", time.Now().UnixNano()),
		Agent:             sub,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("replay runner: %w", err)
	}

	msg := task.Intent
	if strings.TrimSpace(task.ContextExcerpt) != "" {
		msg += "\n\nContext:\n" + task.ContextExcerpt
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var text strings.Builder
	var called []string
	seen := make(map[string]bool)
	toolCalls := 0
	audit := auditFrom(ctx)
	for ev, err := range r.Run(runCtx, "sleep", fmt.Sprintf("sleep-replay-%d", time.Now().UnixNano()), genai.NewContentFromText(msg, genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			return "", nil, fmt.Errorf("replay run: %w", err)
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, part := range ev.Content.Parts {
			if part == nil {
				continue
			}
			if part.Text != "" && !part.Thought {
				text.WriteString(part.Text)
			}
			if part.FunctionCall != nil {
				toolCalls++
				if toolCalls > maxToolCalls {
					return "", called, fmt.Errorf("tool-call budget exceeded (%d > %d): aborting task", toolCalls, maxToolCalls)
				}
				// Deny-by-default audit (SL-040): the allowlist keeps
				// non-allowlisted tools unattached, but the model can still
				// emit calls for them - record every such attempt.
				if !replayAllowTools[part.FunctionCall.Name] {
					audit.addToolAttempt(part.FunctionCall.Name)
				}
				if !seen[part.FunctionCall.Name] {
					seen[part.FunctionCall.Name] = true
					called = append(called, part.FunctionCall.Name)
				}
			}
			if part.FunctionResponse != nil {
				// Sandbox path refusals come back as tool-response errors;
				// count the recognizable refusal markers for the audit.
				rendered := fmt.Sprintf("%v", part.FunctionResponse.Response)
				for _, marker := range sandboxDenialMarkers {
					if strings.Contains(rendered, marker) {
						audit.addSandboxDenial()
						break
					}
				}
			}
		}
	}
	return text.String(), called, nil
}
