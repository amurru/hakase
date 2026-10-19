// fixtures_test.go - T0.1 replay fixture driver (FG-006, senior review).
//
// Scripted {tool, args, label_before, expect} fixtures drive
// LookupWithLabel + Fold without running tools (mirrors `appa replay`,
// no binary). The 3 seeds must pass before engine work lands.
package permissions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixtureLabel struct {
	Trust    string `json:"trust"`
	Audience string `json:"audience"`
}

type fixtureStep struct {
	Tool         string       `json:"tool"`
	Args         []string     `json:"args"`
	LabelBefore  fixtureLabel `json:"label_before"`
	ApplyDeltaOn string       `json:"apply_delta_on"`
	Expect       string       `json:"expect"`
}

type fixtureFile struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Policy      Policy        `json:"policy"`
	Steps       []fixtureStep `json:"steps"`
}

func parseFixtureLabel(t *testing.T, fl fixtureLabel) SessionLabel {
	t.Helper()
	tr, err := ParseTrust(fl.Trust)
	if err != nil {
		t.Fatalf("bad trust %q: %v", fl.Trust, err)
	}
	au, err := ParseAudience(fl.Audience)
	if err != nil {
		t.Fatalf("bad audience %q: %v", fl.Audience, err)
	}
	return SessionLabel{Trust: tr, Audience: au}
}

func TestFlowFixtures(t *testing.T) {
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	dir := filepath.Join("..", "..", "docs", "flow-guard", "fixtures")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir fixtures: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no fixtures, want 3 seeds")
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		var fx fixtureFile
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		t.Run(fx.Name, func(t *testing.T) {
			cp, err := Compile(fx.Policy)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			for i, st := range fx.Steps {
				label := parseFixtureLabel(t, st.LabelBefore)
				eff, _, _ := cp.EvaluateWithLabel(&label, st.Tool, st.Args...)
				if st.Expect != "" && string(eff) != st.Expect {
					t.Errorf("step %d (%s %v): verdict = %q, want %q", i, st.Tool, st.Args, eff, st.Expect)
				}
				if st.ApplyDeltaOn != "" {
					// Fold the union of matching deltas (success AND
					// error fold conservatively, C1) and check the next
					// step's label_before matches the taint.
					matched := cp.matchingRules(nil, st.Tool, st.Args...)
					var ds []compiledDelta
					for _, m := range matched {
						if m.hasDelta {
							ds = append(ds, m.delta)
						}
					}
					next := applyDelta(label, unionDelta(ds))
					if i+1 >= len(fx.Steps) {
						t.Fatalf("step %d applies delta with no next step", i)
					}
					want := parseFixtureLabel(t, fx.Steps[i+1].LabelBefore)
					if next.Trust != want.Trust || next.Audience != want.Audience {
						t.Errorf("step %d fold = %v/%v, next label_before = %v/%v",
							i, next.Trust, next.Audience, want.Trust, want.Audience)
					}
				}
			}
		})
	}
}

// TestExampleLoads pins the T1.6 starter example compiling as v2 after
// comment-stripping (the file is JSONC documentation; the strict loader
// takes comment-free JSON).
func TestExampleLoads(t *testing.T) {
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "flow-guard", "permissions.example.jsonc"))
	if err != nil {
		t.Fatalf("ReadFile example: %v", err)
	}
	var lines []string
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "//") {
			continue
		}
		lines = append(lines, ln)
	}
	var p Policy
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &p); err != nil {
		t.Fatalf("Unmarshal example: %v", err)
	}
	cp, err := Compile(p)
	if err != nil {
		t.Fatalf("Compile example: %v", err)
	}
	if !cp.FlowEnabled() {
		t.Error("example not flow-enabled")
	}
	// Starter invariant: no sanitizable:true on send_message *.
	for _, cr := range cp.rules {
		if cr.rule.Action == "send_message" && cr.rule.Sanitizable {
			t.Errorf("starter rule allows sanitizable send: %+v", cr.rule)
		}
	}
}

// TestFixtureCount pins the 3 T0.1 seeds exist by name.
func TestFixtureCount(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "flow-guard", "fixtures")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"read-private-then-send-deny", "webfetch-then-send-ask", "v1-parity-unchanged"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing seed fixture %q (have %v)", want, names)
		}
	}
}
