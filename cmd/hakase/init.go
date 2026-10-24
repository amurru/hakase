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
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/session"
	"amurru/hakase/internal/skill"
	"amurru/hakase/internal/vision"
	"context"
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
			Action:     sandbox.GateAction(d.Action),
			Risk:       sandbox.CommandRisk(d.Risk),
			Reason:     d.Reason,
			PolicyRule: d.PolicyRule,
		}
	}

	// Sandbox hook: flow-aware command evaluation gate (FG-003). Preferred
	// over EvaluateCommandFunc when set; resolves the session label from
	// ctx and enforces requires gates plus unknown-sink fail-closed.
	sandbox.EvaluateCommandWithLabelFunc = func(sb *sandbox.SandboxConfig, command string, args []string, label *permissions.SessionLabel) sandbox.GateDecision {
		d := hakaseagent.EvaluateCommandWithLabel(sb, command, args, label)
		return sandbox.GateDecision{
			Action:     sandbox.GateAction(d.Action),
			Risk:       sandbox.CommandRisk(d.Risk),
			Reason:     d.Reason,
			PolicyRule: d.PolicyRule,
		}
	}

	// Sandbox hook: Flow Guard label resolution. Narrowed session labels
	// resolve from the registry; empty sessions and v1 (flow off) stay at
	// start (v1 parity). A present-but-unregistered session returns nil when
	// flow is enabled (fail-closed, mirroring flowLabelForCtx), so an
	// unknown label can never pass as widest.
	sandbox.FlowLabelFunc = func(ctx context.Context) *permissions.SessionLabel {
		// H1 live child label: the delegate mutable state (and the
		// session snapshot) resolve first, so sandbox gates see the
		// same label the pre-dispatch bound. Falls back to the
		// registry/start logic inside the agent accessor.
		if l := hakaseagent.FlowLabelForSandbox(ctx); l != nil {
			return l
		}
		// Nil under v2 (present-but-unregistered) fails closed
		// inside LookupWithLabel; return nil rather than widest.
		if permissions.FlowEnabled() {
			return nil
		}
		l := permissions.StartLabel()
		return &l
	}

	// Sandbox hook: approval. Wires through to the agent's approval gate.
	// Flow fields ride along so a sandbox-raised flow ask keeps its
	// (argsHash, labelVersion) binding into the pause record and the
	// resume grant (H3); without them the pause cannot pre-grant safely
	// and fails closed on re-drive.
	sandbox.ApproveFunc = func(ctx context.Context, req interfaces.ApprovalRequest) (bool, error) {
		return hakaseagent.ApproveExec(ctx, sandboxApprovalRequest(req))
	}

	// Sandbox hook: approval expiry timeout (default 60 seconds).
	sandbox.ApprovalExpiryFunc = func() time.Duration {
		return hakaseagent.ApprovalExpiry()
	}

	// Sandbox hook: approval.mode=deny short-circuit (H1). Refuses
	// everything up front, including allow-path commands and file tools
	// that never reach ApproveExec.
	sandbox.ApprovalDenyAllFunc = hakaseagent.ApprovalDenyAll

	// Sandbox hook: audit logging.
	sandbox.AuditCommandFunc = func(entry sandbox.CommandAuditEntry) {
		hakaseagent.AuditCommandExec(hakaseagent.CommandAuditEntry{
			Timestamp:   entry.Timestamp,
			Tool:        entry.Tool,
			Command:     entry.Command,
			Args:        entry.Args,
			CWD:         entry.CWD,
			SessionID:   entry.SessionID,
			SandboxMode: entry.SandboxMode,
			Decision:    entry.Decision,
			Risk:        entry.Risk,
			Reason:      entry.Reason,
			DurationMs:  entry.DurationMs,
			ExitCode:    entry.ExitCode,
			PolicyRule:  entry.PolicyRule,
		})
	}

	// Session hooks: bridge session functions into the context package.
	session.SeedHintedContextPathsHook = hctx.SeedHintedContextPaths
	session.BuildHintedPathsHook = hctx.BuildHintedPaths

	// Context hooks: wire project-finding and model func into the context package.
	hctx.FindProjectRootFunc = skill.FindProjectRoot
	hctx.CurrentModelFunc = func() model.LLM { return nil }

	// Vision hooks: default to nil. Each entry point overrides CurrentConfig
	// with the loaded config (main.go TUI, web.go, cronjob bootstrap) - the
	// BeforeModel callback needs it to rewrite attached-image parts before
	// the OpenAI-compatible adapter rejects InlineData. CurrentModelInfo is
	// overridden async once model capabilities are fetched.
	vision.CurrentModelInfo = func() *interfaces.ModelInfo { return nil }
	vision.CurrentConfig = func() *config.Config { return nil }

	// Test-only bridge: allocate DiscoverMarkdownSkillsForTest.
	skill.DiscoverMarkdownSkillsForTest = func(cwd string, extraDirs []string, log interfaces.LogFunc) []skill.MarkdownSkill {
		return skill.DiscoverMarkdownSkills(cwd, extraDirs, log)
	}
}

// sandboxApprovalRequest maps a sandbox approval request onto the agent
// gate request without dropping the Flow Guard binding (H3): a
// sandbox-raised flow ask (system_exec/python via the sandbox path)
// must keep FlowReason/FlowArgsHash/FlowLabelVersion so the pause record
// scopes the resume grant to the exact call. Named for tests.
func sandboxApprovalRequest(req interfaces.ApprovalRequest) hakaseagent.ApprovalRequest {
	return hakaseagent.ApprovalRequest{
		Tool:             req.Tool,
		Command:          req.Command,
		Args:             req.Args,
		Risk:             req.Risk,
		Reason:           req.Reason,
		Source:           req.Source,
		ExpiresAt:        req.ExpiresAt,
		SessionID:        req.SessionID,
		FlowReason:       req.FlowReason,
		FlowArgsHash:     req.FlowArgsHash,
		FlowLabelVersion: req.FlowLabelVersion,
	}
}
