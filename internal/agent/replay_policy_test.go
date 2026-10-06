// replay_policy_test.go - pins the Phase 6 tool replay audit: the
// load-bearing never-replay classifications, the safe/idempotent
// tiers, and the fail-closed default for unaudited names.
package agent

import (
	"testing"
)

func TestReplayPolicyNeverPins(t *testing.T) {
	for _, name := range []string{
		"system_exec", "system_exec_start", "delegate_task",
		"cron", "cronjob", "download_file",
		"python_interpreter", "clarify",
		"git_push", "git_commit", "create_task",
	} {
		if got := ReplayPolicyFor(name); got != ReplayNever {
			t.Errorf("ReplayPolicyFor(%q) = %q, want never", name, got)
		}
	}
}

func TestReplayPolicySafeAndIdempotent(t *testing.T) {
	for _, name := range []string{
		"read_file", "search_files", "git_status", "git_diff",
		"git_log", "get_task", "list_sessions", "system_exec_list",
	} {
		if got := ReplayPolicyFor(name); got != ReplaySafe {
			t.Errorf("ReplayPolicyFor(%q) = %q, want safe", name, got)
		}
	}
	for _, name := range []string{"write_file", "update_task", "save_skill"} {
		if got := ReplayPolicyFor(name); got != ReplayIdempotent {
			t.Errorf("ReplayPolicyFor(%q) = %q, want idempotent", name, got)
		}
	}
}

func TestReplayPolicyUnknownDefaultsNever(t *testing.T) {
	for _, name := range []string{"", "some_future_mcp_tool", "SYSTEM_EXEC"} {
		if got := ReplayPolicyFor(name); got != ReplayNever {
			t.Errorf("ReplayPolicyFor(%q) = %q, want never (fail-closed default)", name, got)
		}
	}
}
