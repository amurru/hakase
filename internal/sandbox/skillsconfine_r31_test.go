// skillsconfine_r31_test.go - R31 executable-suffix confinement tests.
package sandbox

import (
	"strings"
	"testing"
)

// TestR31SkillsStartupSuffixesConfined pins R31 .pth/.so/.dll/.pyw
// confinement: startup-executed and natively-importable files under a
// skills tree refuse with the save_skill alternative, strictly scoped
// to executable suffixes (docs stay writable).
func TestR31SkillsStartupSuffixesConfined(t *testing.T) {
	for _, p := range []string{
		"/ws/skills/startup.pth",
		"/ws/skills/nested/deep.PTH",
		"/ws/x/skills/mod.so",
		"/ws/x/skills/mod.dll",
		"/ws/x/skills/run.pyw",
		"/ws/x/skills/__init__.pyw",
		"/ws/skills/sub/deep/evil.dll",
	} {
		if err := denySkillsWrite(p); err == nil {
			t.Errorf("startup-executable path %q: want confinement", p)
		} else if !strings.Contains(err.Error(), "save_skill") {
			t.Errorf("denial %q must name save_skill, got: %v", p, err)
		}
	}
	for _, p := range []string{
		"/ws/skills/README.md",
		"/ws/skills/notes.txt",
		"/ws/skills/data.json",
		"/ws/notes.txt",
	} {
		if err := denySkillsWrite(p); err != nil {
			t.Errorf("non-code path %q must stay writable, got: %v", p, err)
		}
	}
}
