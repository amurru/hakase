// shell_line_test.go - S14 shared pure shell identity helper.
package permissions

import (
	"testing"
)

// ShellLine is the single identity both the agent mapper and the
// sandbox gate match: TrimSpace(command + " " + Join(args)).
func TestShellLine(t *testing.T) {
	cases := []struct {
		name    string
		command string
		args    []string
		want    string
	}{
		{"bare command", "git status", nil, "git status"},
		{"explicit args", "git", []string{"status", "--short"}, "git status --short"},
		{"empty falls back to command", "", nil, ""},
		{"whitespace trimmed", "  echo hi  ", nil, "echo hi"},
	}
	for _, tc := range cases {
		if got := ShellLine(tc.command, tc.args); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
