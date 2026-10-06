// replay_policy.go - tool replay classification for durable resume.
//
// When a run is interrupted mid-gate and later resumed, completed tool
// calls must not silently re-execute: a resumed clarify turn reuses the
// persisted history (the engine never re-runs settled calls — pinned by
// the spike tests), but an approval resume re-drives a FRESH turn in
// which the model re-issues every call. The resume driver consults this
// table before allowing an approval re-drive: any completed
// never-replay call in the paused turn's history refuses auto-resume.
//
// Tiers:
//   - ReplaySafe: read-only. Retry is harmless.
//   - ReplayIdempotent: same inputs converge to the same state.
//     Retry is acceptable.
//   - ReplayNever: side effects (exec, network, mutation, new identity
//     per call). Must never auto-replay.
//
// Unknown tool names (custom MCP tools, future tools) default to
// ReplayNever: fail-closed. Add new tools here explicitly when
// audited. Part of durable resume (docs/durable-resume/plan.md Phase 6).
package agent

// ReplayPolicy classifies a tool for resume replay.
type ReplayPolicy string

const (
	// ReplaySafe marks read-only tools: retry cannot change state.
	ReplaySafe ReplayPolicy = "safe"
	// ReplayIdempotent marks tools whose same-input retry converges
	// to the same state (overwrite/upsert semantics).
	ReplayIdempotent ReplayPolicy = "idempotent"
	// ReplayNever marks side-effecting tools that must never
	// auto-replay on resume (exec, network, mutation, per-call
	// identity). Also the default for unaudited tool names.
	ReplayNever ReplayPolicy = "never"
)

// replaySafeTools are read-only: introspection, search, status reads.
var replaySafeTools = map[string]struct{}{
	"read_file": {}, "search_files": {},
	"git_status": {}, "git_diff": {}, "git_log": {}, "git_branch": {},
	"get_task": {}, "list_tasks": {},
	"get_session": {}, "list_sessions": {},
	"list_skills": {}, "load_markdown_skill": {}, "skill": {},
	"list_knowledge": {}, "search_knowledge": {},
	"recall_knowledge": {}, "cite_knowledge": {},
	"lint_knowledge":     {},
	"system_exec_status": {}, "system_exec_list": {},
	"web_search": {}, "browse": {}, "extract_pdf": {},
	"env": {}, "rules": {}, "help": {},
	"projects": {}, "channels": {}, "session": {}, "task": {},
	"memory": {},
}

// replayIdempotentTools converge on same-input retry (overwrite/upsert).
var replayIdempotentTools = map[string]struct{}{
	"write_file":       {},
	"update_task":      {},
	"save_skill":       {},
	"save_knowledge":   {},
	"update_knowledge": {},
}

// replayNeverTools have side effects and must never auto-replay. This
// doubles as documentation of the audit; the default below covers any
// name missing from all three tables, so entries here are the
// load-bearing pins (exec, delegation, scheduling, network, mutation,
// per-call identity, gates).
var replayNeverTools = map[string]struct{}{
	// Execution: arbitrary host/Python code.
	"system_exec": {}, "system_exec_start": {}, "system_exec_kill": {},
	"python_interpreter": {}, "code_interpreter": {},
	// Delegation/scheduling: spawns runs or timers.
	"delegate_task": {}, "cron": {}, "cronjob": {}, "ask_sidekick": {},
	// Network/mutation: new state per call.
	"download_file": {}, "deploy": {}, "generate_image": {},
	"git_clone": {}, "git_push": {}, "git_pull": {},
	"git_commit": {}, "git_stage": {}, "git_checkout": {},
	"git_reset": {}, "git_clean": {}, "git_stash": {}, "git_tag": {},
	// Identity per call / destructive.
	"create_task": {}, "delete_task": {}, "archive_task": {},
	"forget_memory": {}, "link_knowledge": {},
	// Gates: answered through the resume driver, never replayed.
	"clarify": {}, "elicit": {},
	"exit": {},
}

// ReplayPolicyFor returns the resume-replay policy for a tool name.
// Unknown names return ReplayNever (fail-closed: unaudited tools are
// treated as side-effecting until explicitly classified).
func ReplayPolicyFor(toolName string) ReplayPolicy {
	if _, ok := replaySafeTools[toolName]; ok {
		return ReplaySafe
	}
	if _, ok := replayIdempotentTools[toolName]; ok {
		return ReplayIdempotent
	}
	return ReplayNever
}
