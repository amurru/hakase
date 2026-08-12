// init.go - package-level initialization hooks for internal packages.
// These were formerly in root (sandbox_hooks_init.go) and wired global
// state that internal packages consume via function variables. They MUST
// run before any internal package code references the hooked variables.
package main

import (
	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/config"
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/session"
	"amurru/hakase/internal/skill"
	"amurru/hakase/internal/vision"
	"time"

	"google.golang.org/adk/v2/model"
)

func init() {
	// Sandbox hook: subdirectory context hints for file operations.
	sandbox.SubdirContextHintFunc = hctx.SubdirContextHint

	// Sandbox hook: wrap untrusted file content (read_file, search_files).
	sandbox.WrapUntrustedDataFunc = hctx.WrapUntrustedData

	// Sandbox hook: command evaluation gate. Uses the agent package's
	// gate logic (EvaluateCommand) and converts agent.GateDecision to
	// sandbox.GateDecision.
	sandbox.EvaluateCommandFunc = func(sb *sandbox.SandboxConfig, command string, args []string) sandbox.GateDecision {
		d := hakaseagent.EvaluateCommand(sb, command, args)
		return sandbox.GateDecision{
			Action: sandbox.GateAction(d.Action),
			Risk:   sandbox.CommandRisk(d.Risk),
			Reason: d.Reason,
		}
	}

	// Sandbox hook: approval. Wires through to the agent's approval gate.
	sandbox.ApproveFunc = func(req interfaces.ApprovalRequest) (bool, error) {
		return hakaseagent.ApproveExec(hakaseagent.ApprovalRequest{
			Tool:      req.Tool,
			Command:   req.Command,
			Args:      req.Args,
			Risk:      req.Risk,
			Reason:    req.Reason,
			Source:    req.Source,
			ExpiresAt: req.ExpiresAt,
		})
	}

	// Sandbox hook: approval expiry timeout (default 60 seconds).
	sandbox.ApprovalExpiryFunc = func() time.Duration {
		return hakaseagent.ApprovalExpiry()
	}

	// Sandbox hook: audit logging.
	sandbox.AuditCommandFunc = func(entry sandbox.CommandAuditEntry) {
		hakaseagent.AuditCommandExec(hakaseagent.CommandAuditEntry{
			Timestamp:   entry.Timestamp,
			Tool:        entry.Tool,
			Command:     entry.Command,
			Args:        entry.Args,
			CWD:         entry.CWD,
			SandboxMode: entry.SandboxMode,
			Decision:    entry.Decision,
			Risk:        entry.Risk,
			Reason:      entry.Reason,
			DurationMs:  entry.DurationMs,
			ExitCode:    entry.ExitCode,
		})
	}

	// Session hooks: bridge session functions into the context package.
	session.SeedHintedContextPathsHook = hctx.SeedHintedContextPaths
	session.BuildHintedPathsHook = hctx.BuildHintedPaths

	// Context hooks: wire project-finding and model func into the context package.
	hctx.FindProjectRootFunc = skill.FindProjectRoot
	hctx.CurrentModelFunc = func() model.LLM { return nil }

	// Vision hooks: default to nil (overridden at TUI launch in main.go).
	vision.CurrentModelInfo = func() *interfaces.ModelInfo { return nil }
	vision.CurrentConfig = func() *config.Config { return nil }

	// Test-only bridge: allocate DiscoverMarkdownSkillsForTest.
	skill.DiscoverMarkdownSkillsForTest = func(cwd string, extraDirs []string, log interfaces.LogFunc) []skill.MarkdownSkill {
		return skill.DiscoverMarkdownSkills(cwd, extraDirs, log)
	}
}
