package main

import (
	"amurru/hakase/internal/config"
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/session"
	"amurru/hakase/internal/vision"

	"google.golang.org/adk/v2/model"
)

func init() {
	sandbox.SubdirContextHintFunc = hctx.SubdirContextHint

	// evaluateCommand returns root's GateDecision; sandbox expects sandbox.GateDecision.
	// Both structs have identical fields.
	sandbox.EvaluateCommandFunc = func(sb *sandbox.SandboxConfig, command string, args []string) sandbox.GateDecision {
		d := evaluateCommand(sb, command, args)
		return sandbox.GateDecision{
			Action: sandbox.GateAction(d.Action),
			Risk:   sandbox.CommandRisk(d.Risk),
			Reason: d.Reason,
		}
	}

	// approveExec takes root's ApprovalRequest; sandbox expects interfaces.ApprovalRequest.
	sandbox.ApproveFunc = func(req interfaces.ApprovalRequest) (bool, error) {
		return approveExec(ApprovalRequest{
			Tool:      req.Tool,
			Command:   req.Command,
			Args:      req.Args,
			Risk:      req.Risk,
			Reason:    req.Reason,
			Source:    req.Source,
			ExpiresAt: req.ExpiresAt,
		})
	}

	sandbox.ApprovalExpiryFunc = approvalExpiry

	// auditCommandExec takes root's CommandAuditEntry; sandbox expects sandbox.CommandAuditEntry.
	sandbox.AuditCommandFunc = func(entry sandbox.CommandAuditEntry) {
		auditCommandExec(CommandAuditEntry{
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

	// Session hooks: bridge root functions to the session package.
	session.SeedHintedContextPathsHook = hctx.SeedHintedContextPaths
	session.BuildHintedPathsHook = hctx.BuildHintedPaths

	// Context hooks: wire root globals into the context package.
	hctx.FindProjectRootFunc = FindProjectRoot
	hctx.CurrentModelFunc = func() model.LLM { return currentModel }

	// Vision hooks: wire root globals into the vision package.
	// currentModelInfo and currentConfig are set later by setupRunner/main.
	vision.CurrentModelInfo = func() *interfaces.ModelInfo { return currentModelInfo }
	vision.CurrentConfig = func() *config.Config { return currentConfig }
}
