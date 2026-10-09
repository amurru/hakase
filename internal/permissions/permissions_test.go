package permissions

import (
	"os"
	"path/filepath"
	"testing"
)

func mustCompile(t *testing.T, p Policy) *CompiledPolicy {
	t.Helper()
	cp, err := Compile(p)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return cp
}

// TestPrecedenceMatrix pins deny > ask > allow regardless of rule order.
func TestPrecedenceMatrix(t *testing.T) {
	orders := [][]Rule{
		{
			{Action: "shell", Resource: "git *", Effect: EffectAllow},
			{Action: "shell", Resource: "git *", Effect: EffectAsk},
			{Action: "shell", Resource: "git *", Effect: EffectDeny},
		},
		{
			{Action: "shell", Resource: "git *", Effect: EffectDeny},
			{Action: "shell", Resource: "git *", Effect: EffectAllow},
			{Action: "shell", Resource: "git *", Effect: EffectAsk},
		},
		{
			{Action: "shell", Resource: "git *", Effect: EffectAsk},
			{Action: "shell", Resource: "git *", Effect: EffectDeny},
			{Action: "shell", Resource: "git *", Effect: EffectAllow},
		},
	}
	for oi, rules := range orders {
		cp := mustCompile(t, Policy{Rules: rules})
		for _, tc := range []struct {
			action string
			res    string
			want   Effect
		}{
			{"shell", "git status", EffectDeny},
			{"shell", "git push origin", EffectDeny},
		} {
			if got, _ := cp.Evaluate(tc.action, tc.res); got != tc.want {
				t.Errorf("order %d: Evaluate(%q, %q) = %q, want %q", oi, tc.action, tc.res, got, tc.want)
			}
		}
	}

	// Ask beats allow without any deny present.
	cp := mustCompile(t, Policy{Rules: []Rule{
		{Action: "read", Resource: "*", Effect: EffectAllow},
		{Action: "read", Resource: "*.env", Effect: EffectAsk},
	}})
	if got, _ := cp.Evaluate("read", ".env"); got != EffectAsk {
		t.Errorf("Evaluate(read, .env) = %q, want ask", got)
	}
	if got, _ := cp.Evaluate("read", "main.go"); got != EffectAllow {
		t.Errorf("Evaluate(read, main.go) = %q, want allow", got)
	}
}

// TestMultiResourceDenyWins pins deny-wins across resources in one call.
func TestMultiResourceDenyWins(t *testing.T) {
	cp := mustCompile(t, Policy{Rules: []Rule{
		{Action: "edit", Resource: "*", Effect: EffectAllow},
		{Action: "edit", Resource: "*.env", Effect: EffectDeny},
	}})
	if got, _ := cp.Evaluate("edit", "main.go", "notes.txt"); got != EffectAllow {
		t.Errorf("all-allow multi = %q, want allow", got)
	}
	if got, _ := cp.Evaluate("edit", "main.go", ".env"); got != EffectDeny {
		t.Errorf("one-denied multi = %q, want deny", got)
	}
}

// TestNoMatchDefault pins default ask, custom defaults, and nil-rule hits.
func TestNoMatchDefault(t *testing.T) {
	cp := mustCompile(t, Policy{})
	if got, rule := cp.Evaluate("shell", "anything"); got != EffectAsk || rule != nil {
		t.Errorf("empty policy = %q/%v, want ask/nil", got, rule)
	}

	cp = mustCompile(t, Policy{Default: EffectAllow})
	if got, rule := cp.Evaluate("shell", "anything"); got != EffectAllow || rule != nil {
		t.Errorf("allow-default = %q/%v, want allow/nil", got, rule)
	}
}

// TestActionWildcard pins "*" action matching and case-insensitivity.
func TestActionWildcard(t *testing.T) {
	cp := mustCompile(t, Policy{Rules: []Rule{
		{Action: "*", Resource: "secret*", Effect: EffectDeny},
	}})
	for _, action := range []string{"read", "edit", "glob", "SHELL"} {
		if got, _ := cp.Evaluate(action, "secret.txt"); got != EffectDeny {
			t.Errorf("Evaluate(%q, secret.txt) = %q, want deny", action, got)
		}
	}
	if got, _ := cp.Evaluate("read", "public.txt"); got != EffectAsk {
		t.Errorf("non-matching = %q, want default ask", got)
	}
}

// TestGlobSemantics pins "*" spanning separators, "?" single-char, and
// absolute-path cleaning before match.
func TestGlobSemantics(t *testing.T) {
	cp := mustCompile(t, Policy{Rules: []Rule{
		{Action: "read", Resource: "/root/*.env", Effect: EffectDeny},
		{Action: "read", Resource: "/root/?oo.go", Effect: EffectAsk},
	}})
	cases := []struct {
		res  string
		want Effect
	}{
		{"/root/.env", EffectDeny},
		{"/root/sub/dir/.env", EffectDeny}, // * spans separators
		{"/root/foo.go", EffectAsk},        // ? matches one char
		{"/root/.//sub/../foo.go", EffectAsk},
		{"/root/fooo.go", EffectAsk}, // default ask, ? matches exactly one
		{"/other/.env", EffectAsk},   // default ask
	}
	for _, tc := range cases {
		if got, _ := cp.Evaluate("read", tc.res); got != tc.want {
			t.Errorf("Evaluate(read, %q) = %q, want %q", tc.res, got, tc.want)
		}
	}
}

// TestHomeExpansion pins "~" and "$HOME" pattern expansion.
func TestHomeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	cp := mustCompile(t, Policy{Rules: []Rule{
		{Action: "read", Resource: "~/.secrets/*", Effect: EffectDeny},
		{Action: "read", Resource: "$HOME/.keys/*", Effect: EffectDeny},
	}})
	for _, res := range []string{
		filepath.Join(home, ".secrets", "a"),
		filepath.Join(home, ".keys", "b"),
	} {
		if got, _ := cp.Evaluate("read", res); got != EffectDeny {
			t.Errorf("Evaluate(read, %q) = %q, want deny", res, got)
		}
	}
}

// TestCompileErrors pins strict validation at compile time.
func TestCompileErrors(t *testing.T) {
	for _, p := range []Policy{
		{Default: "sometimes"},
		{Rules: []Rule{{Action: "", Resource: "*", Effect: EffectAllow}}},
		{Rules: []Rule{{Action: "read", Resource: "", Effect: EffectAllow}}},
		{Rules: []Rule{{Action: "read", Resource: "*", Effect: "permit"}}},
		{Rules: []Rule{{Action: "read", Resource: "[unterminated", Effect: EffectAllow}}},
	} {
		if _, err := Compile(p); err == nil {
			t.Errorf("Compile(%+v) = nil error, want error", p)
		}
	}
}

// TestWinningRule pins that Evaluate reports the rule behind the verdict
// (audit metadata needs the source triple).
func TestWinningRule(t *testing.T) {
	cp := mustCompile(t, Policy{
		Default: EffectAllow,
		Rules: []Rule{
			{Action: "shell", Resource: "git *", Effect: EffectAllow},
			{Action: "shell", Resource: "git push *", Effect: EffectDeny},
		},
	})
	got, rule := cp.Evaluate("shell", "git push origin")
	if got != EffectDeny || rule == nil || rule.Resource != "git push *" {
		t.Errorf("Evaluate = %q/%v, want deny/git push * rule", got, rule)
	}
}
