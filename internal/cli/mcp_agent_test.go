package cli

import "testing"

// TestMCPServeAgentFlag checks the --agent dispatch: wired hook receives the
// call, unwired hook is a usage error (exit 2).
func TestMCPServeAgentFlag(t *testing.T) {
	orig := MCPAgentServeFn
	t.Cleanup(func() { MCPAgentServeFn = orig })

	t.Run("wired hook is called", func(t *testing.T) {
		called := 0
		MCPAgentServeFn = func(args []string) int {
			called++
			if len(args) != 0 {
				t.Errorf("hook args = %v, want none (flags consumed by the dispatcher)", args)
			}
			return 7
		}
		if code := RunMCPCLI([]string{"serve", "--agent"}); code != 7 {
			t.Fatalf("exit code = %d, want 7", code)
		}
		if called != 1 {
			t.Fatalf("hook called %d times, want 1", called)
		}
	})

	t.Run("unwired hook is a usage error", func(t *testing.T) {
		MCPAgentServeFn = nil
		if code := RunMCPCLI([]string{"serve", "--agent"}); code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
	})

	t.Run("unexpected argument is a usage error", func(t *testing.T) {
		MCPAgentServeFn = func([]string) int { return 0 }
		defer func() { MCPAgentServeFn = orig }()
		if code := RunMCPCLI([]string{"serve", "--agent", "extra"}); code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
	})

	t.Run("unknown subcommand", func(t *testing.T) {
		if code := RunMCPCLI([]string{"frobnicate"}); code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
	})
}
