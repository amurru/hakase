package agent

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/sandbox"
)

// TestEnvChainCoveredTokensTable pins the gate skip span in input
// coordinates. It is deliberately conservative at a -S flag (cover the flag
// and its value, then stop) because the expanded stream has different
// coordinates and a spliced wrapper can put the following input tokens
// behind the utility. Under-reporting only re-scans; the old expanded-stream
// span skipped a real trailing env (see TestGateEnvSplitTrailingEnvChdirAsk).
func TestEnvChainCoveredTokensTable(t *testing.T) {
	cases := []struct {
		name string
		rest []string
		want int
	}{
		{name: "plain utility", rest: ss("ls", "-la"), want: 0},
		{name: "chdir operand", rest: ss("-C", "/hook", "ls"), want: 2},
		{name: "attached chdir", rest: ss("-C/hook", "ls"), want: 1},
		{name: "nested env chdir", rest: ss("env", "-C", "/hook", "git"), want: 3},
		{name: "dashes nested env", rest: ss("--", "env", "-C", "/hook", "ls"), want: 4},
		{name: "split value stops at boundary", rest: ss("-S", "sudo", "env", "-C", "/hook", "git", "status"), want: 2},
		{name: "attached split stops at token", rest: ss("-Ssudo", "env", "-C", "/hook", "git"), want: 1},
		{name: "unknown long stops", rest: ss("--zznope", "ls"), want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envChainCoveredTokens(tc.rest); got != tc.want {
				t.Errorf("envChainCoveredTokens(%q) = %d, want %d", tc.rest, got, tc.want)
			}
		})
	}
}

// TestGateEnvSplitTrailingEnvChdirAsk pins the gate coveredUntil regression
// end to end. Before the span was computed in input coordinates, the
// expanded stream made `env -S 'env' env -C /hook git status` look covered
// through the trailing env, so the -C was skipped and the shape Allowed.
// The trailing env really is in command position (the spliced env is the
// outer utility), so the gate must Ask, in shell and explicit-argv forms.
func TestGateEnvSplitTrailingEnvChdirAsk(t *testing.T) {
	sb := round2AllowSB()
	cases := []struct {
		name string
		cmd  string
		args []string
	}{
		{name: "control chdir", cmd: `env -C /hook git status`},
		{name: "split nested env", cmd: `env -S 'env' env -C /hook git status`},
		{name: "split wrapper", cmd: `env -S 'sudo' env -C /hook git status`},
		{name: "split wrapper interleaved", cmd: `env -S 'sudo env' env -C /hook git status`},
		{name: "explicit split nested env", cmd: "env", args: ss("-S", "env", "env", "-C", "/hook", "git", "status")},
		{name: "explicit split wrapper", cmd: "env", args: ss("-S", "sudo", "env", "-C", "/hook", "git", "status")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := EvaluateCommand(sb, tc.cmd, tc.args)
			if d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q, %q) = %s, want ask (%s)", tc.cmd, tc.args, d.Action, d.Reason)
			} else if !strings.Contains(d.Reason, "env -C") {
				t.Errorf("EvaluateCommand(%q, %q) reason = %q, want env -C reason", tc.cmd, tc.args, d.Reason)
			}
		})
	}
}

// TestHasEnvChdirOptTable pins -C/--chdir detection across spellings:
// attached values, abbreviations, =/separate values, -S expansion, and
// the utility/-- stops. Tri-state: proven -C reports (chdir, false);
// unexpandable -S (unparseable or over-budget) reports (false, unproven)
// so callers use the nesting reason, never the -C one.
func TestHasEnvChdirOptTable(t *testing.T) {
	cases := []struct {
		name         string
		rest         []string
		wantChdir    bool
		wantUnproven bool
	}{
		{name: "short separate", rest: ss("-C", "/hook", "ls"), wantChdir: true},
		{name: "short attached", rest: ss("-C/hook", "ls"), wantChdir: true},
		{name: "short cluster", rest: ss("-iC", "/hook", "ls"), wantChdir: true},
		{name: "short attached S value is chdir", rest: ss("-CS", "ls"), wantChdir: true},
		{name: "short attached Sh value is chdir", rest: ss("-CSh", "ls"), wantChdir: true},
		{name: "cluster iC S value is chdir", rest: ss("-iCS", "ls"), wantChdir: true},
		{name: "unset C attached no chdir", rest: ss("-uC", "ls")},
		{name: "unset S attached no chdir", rest: ss("-uS", "ls")},
		{name: "archive S attached no chdir", rest: ss("-aS", "ls")},
		{name: "long exact separate", rest: ss("--chdir", "/hook", "ls"), wantChdir: true},
		{name: "long exact equals", rest: ss("--chdir=/hook", "ls"), wantChdir: true},
		{name: "long abbrev", rest: ss("--ch", "/hook", "ls"), wantChdir: true},
		{name: "long abbrev equals", rest: ss("--ch=/hook", "ls"), wantChdir: true},
		{name: "split-string splice carries chdir", rest: ss("-S", "-C /hook ls"), wantChdir: true},
		{name: "no chdir", rest: ss("-i", "ls")},
		{name: "unset takes value but no chdir", rest: ss("--unset", "FOO", "ls")},
		{name: "stops at utility", rest: ss("ls", "-C")},
		{name: "SE-1 split splice assign before nested env chdir", rest: ss("-S", "X=1 env -C /evil git status"), wantChdir: true},
		{name: "SE-1 assign before nested env chdir", rest: ss("X=1", "env", "-C", "/evil", "git", "status"), wantChdir: true},
		{name: "stops at dashes", rest: ss("--", "-C")},
		{name: "nested env past dashes chdirs", rest: ss("--", "env", "-C", "/hook", "ls"), wantChdir: true},
		{name: "nested env past dashes silent", rest: ss("--", "env", "-S", "ls")},
		{name: "dangling split-string past dashes unproven", rest: ss("--", "env", "-S"), wantUnproven: true},
		{name: "unparseable split-string unproven", rest: ss("-S", "'unterminated"), wantUnproven: true},
		{name: "dangling split-string unproven", rest: ss("-S"), wantUnproven: true},
		{name: "trailing split-string long unproven", rest: ss("--split-string"), wantUnproven: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotChdir, gotUnproven := hasEnvChdirOpt(tc.rest)
			if gotChdir != tc.wantChdir || gotUnproven != tc.wantUnproven {
				t.Errorf("hasEnvChdirOpt(%q) = (%v, %v), want (%v, %v)", tc.rest, gotChdir, gotUnproven, tc.wantChdir, tc.wantUnproven)
			}
		})
	}
}

// TestEnvChdirAskReasonTable pins the gate Ask leg firing on any env
// position (sudo env -C ...) and staying silent without -C/--chdir.
func TestEnvChdirAskReasonTable(t *testing.T) {
	ask := [][]string{
		ss("env", "-C", "/hook", "ls"),
		ss("env", "-C/hook", "ls"),
		ss("env", "--chdir", "/hook", "ls"),
		ss("env", "--chdir=/hook", "ls"),
		ss("sudo", "env", "-C", "/hook", "ls"),
		ss("env", "-S", "-C /hook ls"),
		ss("env", "-S", "X=1 env -C /hook git status"),
		ss("env", "-CS", "ls"),
		ss("env", "-iCS", "ls"),
		// Every-position H1 scan (like checkEnvPositionHardDeny):
		// an unquoted `echo env -C` still asks fail-closed, exactly
		// like `echo env -S "rm -rf /"` denies. Quoted text stays
		// one token and stays silent (pinned below).
		ss("echo", "env", "-C"),
	}
	for _, seg := range ask {
		t.Run("ask "+strings.Join(seg, " "), func(t *testing.T) {
			if reason := envChdirAskReason(seg); reason == "" {
				t.Errorf("envChdirAskReason(%q) = empty, want ask", seg)
			} else if !strings.Contains(reason, "env -C") {
				t.Errorf("envChdirAskReason(%q) = %q, want env -C reason", seg, reason)
			}
		})
	}
	clean := [][]string{
		ss("env", "ls"),
		ss("env", "-i", "ls"),
		ss("env", "--unset", "FOO", "ls"),
		ss("env", "-uC", "ls"),
		ss("env", "-uS", "ls"),
		ss("ls", "-C"),
		ss(`echo "env -C"`),
		ss("env", "--", "-C"),
		ss("env", "--", "env", "-S", "ls"),
		// Benign -S payloads carry no -C and must stay silent
		// (shell and explicit-args forms alike, #3): the quote-
		// stripped leg asks proven -C only, never artifact nesting.
		ss("env", "-S", "ls"),
		ss("env", "-S", "echo hi"),
		ss("env", "-S", "git status"),
	}
	for _, seg := range clean {
		t.Run("silent "+strings.Join(seg, " "), func(t *testing.T) {
			if reason := envChdirAskReason(seg); reason != "" {
				t.Errorf("envChdirAskReason(%q) = %q, want silent", seg, reason)
			}
		})
	}
	// Unproven split-strings (dangling or unparseable, including past a
	// -- boundary) ask with the nesting reason, never the -C one: -C is
	// not proven for these shapes.
	nesting := [][]string{
		ss("env", "-S"),
		ss("env", "-S", "'unterminated"),
		ss("env", "--split-string"),
		ss("env", "--", "env", "-S"),
	}
	for _, seg := range nesting {
		t.Run("nesting "+strings.Join(seg, " "), func(t *testing.T) {
			reason := envChdirAskReason(seg)
			if reason == "" {
				t.Errorf("envChdirAskReason(%q) = empty, want nesting ask", seg)
			} else if strings.Contains(reason, "env -C") {
				t.Errorf("envChdirAskReason(%q) = %q, want nesting reason, not -C", seg, reason)
			} else if !strings.Contains(reason, "split-string") {
				t.Errorf("envChdirAskReason(%q) = %q, want split-string reason", seg, reason)
			}
		})
	}
}

// TestUnwrapEnvTrampolineTrailingUnknown pins fail-closed bounds checks:
// a trailing value-taking option without its operand (-u/-C/-a, exact or
// cluster tail, or --unset/--chdir/--argv0) yields the unknown sentinel,
// never bare env (bare env classifies low and would Allow).
func TestUnwrapEnvTrampolineTrailingUnknown(t *testing.T) {
	cases := [][]string{
		ss("env", "-u"),
		ss("env", "-C"),
		ss("env", "-a"),
		ss("env", "-iu"),
		ss("env", "-iC"),
		ss("env", "--unset"),
		ss("env", "--chdir"),
		ss("env", "--argv0"),
		ss("env", "-S"),
	}
	for _, argv := range cases {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			eff, isEnv := unwrapEnvTrampoline(argv)
			if !isEnv {
				t.Fatalf("unwrapEnvTrampoline(%q) = not env, want env", argv)
			}
			if len(eff) != 1 || eff[0] != envSplitUnknown {
				t.Errorf("unwrapEnvTrampoline(%q) = %q, want [%q]", argv, eff, envSplitUnknown)
			}
		})
	}
}

// TestUnwrapEnvTrampolineAttachedValueTakers pins legacy -uC behavior:
// an attached value (-uC unsets C, -CS is -C with value S) never consumes
// the following token, so the utility stays visible. -uC resolves to the
// utility with no chdir; -CS resolves with the S chdir carried by the
// hasEnvChdirOpt leg.
func TestUnwrapEnvTrampolineAttachedValueTakers(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{name: "unset C attached keeps utility", argv: ss("env", "-uC", "ls"), want: ss("ls")},
		{name: "unset FOO attached keeps utility", argv: ss("env", "-uFOO", "ls"), want: ss("ls")},
		{name: "chdir S attached keeps utility", argv: ss("env", "-CS", "ls"), want: ss("ls")},
		{name: "cluster iC S attached keeps utility", argv: ss("env", "-iCS", "ls"), want: ss("ls")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eff, isEnv := unwrapEnvTrampoline(tc.argv)
			if !isEnv {
				t.Fatalf("unwrapEnvTrampoline(%q) = not env, want env", tc.argv)
			}
			if !reflect.DeepEqual(eff, tc.want) {
				t.Errorf("unwrapEnvTrampoline(%q) = %q, want %q", tc.argv, eff, tc.want)
			}
		})
	}
}

// TestSplitShellSegmentsLexerParity pins the gate-side lexer contract:
// redirect-aware (&>/>& never split) with escaped-operator tracking. The
// sandbox pre-gate lexer (splitShellSegmentsWithSeps) is a deliberate
// subset without that handling: `2>&1` and `&>` split there but not
// here. Do not "fix" one lexer without updating the other and its pin
// test (TestSplitShellSegmentsLiteOps in internal/sandbox).
func TestSplitShellSegmentsLexerParity(t *testing.T) {
	cases := []struct {
		name  string
		cmd   string
		want  []string
		count int
	}{
		{name: "and chain", cmd: "a && b", want: []string{"a", "b"}},
		{name: "fd duplication stays one segment", cmd: "a 2>&1 b", want: []string{"a 2>&1 b"}},
		{name: "amp redirect stays one segment", cmd: "echo hi &> file", want: []string{"echo hi &> file"}},
		{name: "single amp splits", cmd: "a & b", want: []string{"a", "b"}},
		{name: "quoted chain stays one segment", cmd: `echo "a && b"`, want: []string{`echo "a && b"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitShellSegments(tc.cmd)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitShellSegments(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestInteriorEscapeParity pins M-2: an interior shell escape executes the
// same utility as the bare name (`g\it` runs git, exactly like `\git`),
// so comparison-only unescaping strips every backslash (tokenization and
// risk tables are untouched).
func TestInteriorEscapeParity(t *testing.T) {
	if got := gitScanBase(`g\it`); got != "git" {
		t.Errorf("gitScanBase(`g\\it`) = %q, want %q", got, "git")
	}
	if got := gitScanBase(`e\n\v`); got != "env" {
		t.Errorf("gitScanBase(`e\\n\\v`) = %q, want %q", got, "env")
	}
}

// TestNestedSplitStringFixpoint pins M-3: a nested -S behind a nested env
// utility (`env -S 'env -S "rm -rf /"'`) expands to the fixpoint, so the
// hard-deny leg sees the innermost payload exactly like the sandbox
// recursive splice does.
func TestNestedSplitStringFixpoint(t *testing.T) {
	expanded, bad := expandEnvSplitString([]string{"-S", `env -S "rm -rf /"`})
	if bad {
		t.Fatalf("nested -S must expand, got unknown sentinel")
	}
	found := false
	for _, tok := range expanded {
		if tok == "rm" {
			found = true
		}
	}
	if !found {
		t.Errorf("nested -S fixpoint must surface rm, got %q", expanded)
	}
}

// nestedEnvChdirRest builds a plain nested-env option stream: n nested
// env layers then "-C /evil git status" (no -S quoting involved, so the
// depth budget is exercised without shell-lexing limits).
func nestedEnvChdirRest(n int) []string {
	rest := []string{}
	for i := 0; i < n; i++ {
		rest = append(rest, "env")
	}
	return append(rest, "-C", "/evil", "git", "status")
}

// nestedSplitChdirShell builds a deeply nested -S shell command: the inner
// payload `env -C /evil ls` wrapped n times in `env -S "..."` with
// shell-escaped double quotes, so shlex parses every layer.
func nestedSplitChdirShell(n int) string {
	inner := "env -C /evil ls"
	for i := 0; i < n; i++ {
		esc := ""
		for _, r := range inner {
			if r == '"' || r == '\\' {
				esc += "\\"
			}
			esc += string(r)
		}
		inner = `env -S "` + esc + `"`
	}
	return inner
}

// TestDeepNestingChdirAsk pins High-1: the chdir Ask must fire at 8 nested
// env layers and at 9 nested layers (below the 32-pass splice budget, so
// the -C is specifically detected, not a residual). Unparseable and
// over-budget -S (the unproven leg) must Ask with the distinct
// --split-string nesting reason, never the -C one (#4).
func TestDeepNestingChdirAsk(t *testing.T) {
	for _, n := range []int{8, 9} {
		rest := nestedEnvChdirRest(n)
		if chdir, unproven := hasEnvChdirOpt(rest); !chdir || unproven {
			t.Errorf("hasEnvChdirOpt(%d nested env) = (%v, %v), want (true, false)", n, chdir, unproven)
		}
		seg := append([]string{"env"}, rest...)
		if reason := envChdirAskReason(seg); reason == "" {
			t.Errorf("envChdirAskReason(%d nested env) = empty, want ask", n)
		} else if !strings.Contains(reason, "env -C") {
			t.Errorf("envChdirAskReason(%d nested env) = %q, want specific -C reason (below budget)", n, reason)
		}
	}
	// Unproven -S (unparseable or over-budget) still asks (could hide -C).
	if chdir, unproven := hasEnvChdirOpt(ss("-S", "'unterminated")); chdir || !unproven {
		t.Errorf("hasEnvChdirOpt(unparseable -S) = (%v, %v), want (false, true)", chdir, unproven)
	}
	if chdir, unproven := hasEnvChdirOpt(ss("--split-string")); chdir || !unproven {
		t.Errorf("hasEnvChdirOpt(trailing --split-string) = (%v, %v), want (false, true)", chdir, unproven)
	}
	// The residual/unparseable leg uses the nesting reason, never -C (#4).
	for _, seg := range [][]string{
		ss("env", "-S"),
		ss("env", "-S", "'unterminated"),
		ss("env", "--split-string"),
	} {
		reason := envChdirAskReason(seg)
		if reason == "" {
			t.Errorf("envChdirAskReason(%q) = empty, want nesting ask", seg)
		} else if strings.Contains(reason, "env -C") {
			t.Errorf("envChdirAskReason(%q) = %q, want nesting reason, not -C", seg, reason)
		} else if !strings.Contains(reason, "split-string") {
			t.Errorf("envChdirAskReason(%q) = %q, want split-string reason", seg, reason)
		}
	}
	// Nested -S chdirs surface through the expanded stream at depth,
	// with the specific -C reason below the budget.
	for _, n := range []int{2, 9} {
		cmd := nestedSplitChdirShell(n)
		seg := parseCommandArgv(cmd)
		if len(seg) == 0 {
			t.Fatalf("nested -S shell depth %d failed to parse: %q", n, cmd)
		}
		reason := envChdirAskReason(seg)
		if reason == "" {
			t.Errorf("envChdirAskReason(nested -S depth %d) = empty, want ask (%q)", n, cmd)
		} else if !strings.Contains(reason, "env -C") {
			t.Errorf("envChdirAskReason(nested -S depth %d) = %q, want specific -C reason", n, reason)
		}
		sb := &sandbox.SandboxConfig{Mode: sandbox.SandboxModePaths}
		if dec := EvaluateCommand(sb, cmd, nil); dec.Action == ActionAllow {
			t.Errorf("EvaluateCommand(nested -S depth %d) = allow, want ask/deny (%s)", n, dec.Reason)
		}
	}
}

// TestGitTokenAskReasonDeepNestingFast pins #1: plain nested env layers
// (no -S) must scan in linear time. The unguarded re-expansion recursed
// per env token (n=18 took 355ms, n=24 took 22.9s, n=25 timed out);
// the containsEnvSplitString guard skips recursion without -S, so 300
// layers return immediately with the specific git reason.
func TestGitTokenAskReasonDeepNestingFast(t *testing.T) {
	const n = 300
	argv := make([]string, 0, n+4)
	for i := 0; i < n; i++ {
		argv = append(argv, "env")
	}
	argv = append(argv, "git", "-C", "/x", "status")
	if reason := gitTokenAskReason(argv); reason == "" {
		t.Fatalf("gitTokenAskReason(%d nested env) = empty, want ask", n)
	} else if !strings.Contains(reason, "directory override") {
		t.Fatalf("gitTokenAskReason(%d nested env) = %q, want directory-override reason", n, reason)
	}
}

// TestEnvSplitStringBenignShellSilent pins #3: benign -S payloads must
// fully Allow through the shell form, exactly like the explicit-args
// form. Quote stripping degrades `env -S 'ls'` to a dangling `env -S`;
// the stripped leg asks proven -C only, so the artifact stays silent
// instead of Asking with a false -C or nesting reason.
func TestEnvSplitStringBenignShellSilent(t *testing.T) {
	sb := &sandbox.SandboxConfig{Mode: sandbox.SandboxModePaths}
	cmds := []string{`env -S 'ls'`, `env -S 'echo hi'`, `env -S 'git status'`, `env -- env -S 'ls'`}
	for _, cmd := range cmds {
		seg := parseCommandArgv(cmd)
		if len(seg) == 0 {
			t.Fatalf("failed to parse %q", cmd)
		}
		if reason := envChdirAskReason(seg); reason != "" {
			t.Errorf("envChdirAskReason(%q) = %q, want silent", cmd, reason)
		}
		if dec := EvaluateCommand(sb, cmd, nil); dec.Action != ActionAllow {
			t.Errorf("EvaluateCommand(%q) = %s, want allow (%s)", cmd, dec.Action, dec.Reason)
		}
	}
	// A dangling split-string past -- is unproven nesting (never -C):
	// the value could hide -C, but -C is not proven.
	if dec := EvaluateCommand(sb, `env -- env -S`, nil); dec.Action == ActionAllow {
		t.Errorf("EvaluateCommand(env -- env -S) = allow, want ask")
	} else if strings.Contains(dec.Reason, "env -C") {
		t.Errorf("EvaluateCommand(env -- env -S) = %q, want nesting reason, not -C", dec.Reason)
	} else if !strings.Contains(dec.Reason, "split-string") {
		t.Errorf("EvaluateCommand(env -- env -S) = %q, want split-string reason", dec.Reason)
	}
}

// nestedSplitChdirRest builds a token-level nested -S option stream: each
// fixpoint pass peels exactly one -S layer, so depth d needs d+1 passes
// against the 32-pass splice budget. Inner quotes are re-escaped at every
// layer (like the shell builder below), so the encoding grows
// exponentially with depth: keep test depths feasible (<=12).
func nestedSplitChdirRest(depth int) []string {
	inner := "-C /evil ls"
	for i := 0; i < depth; i++ {
		esc := strings.ReplaceAll(strings.ReplaceAll(inner, `\`, `\\`), `"`, `\"`)
		inner = `env -S "` + esc + `"`
	}
	return []string{"-S", inner}
}

// TestExpandSpliceBudgetPins pins the fixpoint budget legs at feasible
// depths. Each nested -S layer needs one fixpoint pass against the 32-pass
// budget, and each layer re-escapes the inner quotes, so the encoding
// grows exponentially with depth: depth 33+ (budget exhaustion via
// well-formed nesting) is not constructible at a sane size, and the input
// builder itself would hang before the code under test runs. The
// fail-closed unproven leg is therefore pinned where it is reachable:
// a residual/dangling -S (unparseable or missing value) reports unproven
// (nesting Ask, never -C, never Allow), and feasible deep nesting (depth
// 10, 11 passes) still expands fully to the proven -C. The 32-pass cap
// itself bounds adversarial nesting: expansion always terminates.
func TestExpandSpliceBudgetPins(t *testing.T) {
	// Feasible deep nesting expands fully: the -C is proven, not residual.
	for _, depth := range []int{10, 12} {
		if chdir, unproven := hasEnvChdirOpt(nestedSplitChdirRest(depth)); !chdir || unproven {
			t.Errorf("hasEnvChdirOpt(%d nested -S) = (%v, %v), want (true, false)", depth, chdir, unproven)
		}
	}
	// Residual -S (unparseable or dangling value) fails closed to unproven:
	// nesting Ask, never -C, never Allow. expandEnvSplitString reports
	// bad=true (unknown sentinel) for these shapes.
	for _, rest := range [][]string{{"-S", "'unterminated"}, {"-S"}} {
		if _, bad := expandEnvSplitString(rest); !bad {
			t.Errorf("expandEnvSplitString(%q) = ok, want unknown (fail-closed)", rest)
		}
		if chdir, unproven := hasEnvChdirOpt(rest); chdir || !unproven {
			t.Errorf("hasEnvChdirOpt(%q) = (%v, %v), want (false, true)", rest, chdir, unproven)
		}
		seg := append([]string{"env"}, rest...)
		if reason := envChdirAskReason(seg); !strings.Contains(reason, "split-string") || strings.Contains(reason, "env -C") {
			t.Errorf("envChdirAskReason(%q) = %q, want nesting reason, not -C", rest, reason)
		}
	}
	sb := &sandbox.SandboxConfig{Mode: sandbox.SandboxModePaths}
	// End to end: a residual nested -S asks with the nesting reason
	// (never -C, never Allow), in both shell and explicit-args forms.
	if dec := EvaluateCommand(sb, `env -S`, nil); dec.Action == ActionAllow {
		t.Errorf("EvaluateCommand(env -S) = allow, want ask")
	} else if strings.Contains(dec.Reason, "env -C") {
		t.Errorf("EvaluateCommand(env -S) = %q, want nesting reason, not -C", dec.Reason)
	} else if !strings.Contains(dec.Reason, "split-string") {
		t.Errorf("EvaluateCommand(env -S) = %q, want split-string reason", dec.Reason)
	}
	if dec := EvaluateCommand(sb, "env", []string{"-S"}); dec.Action == ActionAllow {
		t.Errorf("EvaluateCommand(env -S explicit) = allow, want ask")
	} else if strings.Contains(dec.Reason, "env -C") {
		t.Errorf("EvaluateCommand(env -S explicit) = %q, want nesting reason, not -C", dec.Reason)
	} else if !strings.Contains(dec.Reason, "split-string") {
		t.Errorf("EvaluateCommand(env -S explicit) = %q, want split-string reason", dec.Reason)
	}
}

// TestEnvChdirAskReasonDeepNestingBounded pins the linear scan: 300 nested
// env layers (no -S, so the fixpoint is a single pass per layer and each
// earlier scan covers the later positions) must report the specific -C
// reason in well under the 5s bound.
func TestEnvChdirAskReasonDeepNestingBounded(t *testing.T) {
	const n = 300
	rest := nestedEnvChdirRest(n)
	seg := append([]string{"env"}, rest...)
	start := time.Now()
	reason := envChdirAskReason(seg)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("envChdirAskReason(%d nested env) took %v, want < 5s", n, elapsed)
	}
	if !strings.Contains(reason, "env -C") {
		t.Errorf("envChdirAskReason(%d nested env) = %q, want specific -C reason", n, reason)
	}
}

// TestEnvChdirPastUtilitySilent pins the utility boundary for spliced -S
// streams: `env -S 'x env -C /evil'` runs x with those operands, so the -C
// is past the utility and must not ask; `env -S 'env -C /evil x'` keeps the
// nested env in command position and asks (the every-position scan stops at
// a settled utility, exactly like the sandbox splice).
func TestEnvChdirPastUtilitySilent(t *testing.T) {
	if seg := parseCommandArgv(`env -S 'x env -C /evil ls'`); len(seg) == 0 {
		t.Fatalf("failed to parse past-utility shape")
	} else if reason := envChdirAskReason(seg); reason != "" {
		t.Errorf("envChdirAskReason(%q) = %q, want silent (env -C past the utility x)", seg, reason)
	}
	if seg := parseCommandArgv(`env -S 'env -C /evil x'`); len(seg) == 0 {
		t.Fatalf("failed to parse nested-env shape")
	} else if reason := envChdirAskReason(seg); reason == "" {
		t.Errorf("envChdirAskReason(%q) = empty, want env -C ask (nested env in command position)", seg)
	}
}

// TestExpandSpliceOptionsTable pins the cross-layer contract for
// option-position inputs: the gate expandEnvSplitString and the sandbox
// spliceEnvLayer splice the same option shapes the same way (M-2 interior
// parity already pins escape handling; this pins -S/--chdir/--unset
// shapes). The sandbox mirror lives in TestSpliceEnvLayerTable
// (internal/sandbox/systemexec_helpers_table_test.go); keep the two
// option-position tables in sync by construction. After the utility
// position the gate deliberately over-approximates (splices -S anywhere
// before "--"); the sandbox stops at the utility. That divergence is
// fail-closed and pinned separately by TestExpandSpliceOverApprox.
func TestExpandSpliceOptionsTable(t *testing.T) {
	cases := []struct {
		name string
		rest []string
		want []string
	}{
		{name: "split-string splices", rest: []string{"-S", "FOO=bar ls"}, want: []string{"FOO=bar", "ls"}},
		{name: "long split-string splices", rest: []string{"--split-string", "FOO=bar ls"}, want: []string{"FOO=bar", "ls"}},
		{name: "long abbrev split-string splices", rest: []string{"--sp", "FOO=bar ls"}, want: []string{"FOO=bar", "ls"}},
		{name: "chdir separate", rest: []string{"-C", "/hook", "ls"}, want: []string{"-C", "/hook", "ls"}},
		{name: "nested split assign before env", rest: []string{"-S", "X=1 env -C /evil git status"}, want: []string{"X=1", "env", "-C", "/evil", "git", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, bad := expandEnvSplitString(tc.rest)
			if bad {
				t.Fatalf("expandEnvSplitString(%q) = unknown, want %q", tc.rest, tc.want)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("expandEnvSplitString(%q) = %q, want %q (keep in sync with sandbox spliceEnvLayer)", tc.rest, got, tc.want)
			}
		})
	}
	// Unparseable shapes fail closed on both layers.
	for _, rest := range [][]string{{"-S", "'unterminated"}, {"-S"}} {
		if _, bad := expandEnvSplitString(rest); !bad {
			t.Errorf("expandEnvSplitString(%q) = ok, want unknown (fail-closed)", rest)
		}
	}
	// Over-budget nesting fails closed to unknown (never Allow).
	if _, bad := expandEnvSplitString(ss("-S")); !bad {
		t.Errorf("expandEnvSplitString(dangling -S) = ok, want unknown (fail-closed)")
	}
}

// TestEnvTimeTrampolineChdirAsk pins the delegator-table regression (Tier1
// F1): time(1) delegates to the following command, so `env time env -C ...`
// must not settle on "time" and hide the inner env's directory override. The
// hard-deny leg sees a -S payload behind the same rung.
func TestEnvTimeTrampolineChdirAsk(t *testing.T) {
	sb := round2AllowSB()
	cases := []struct {
		name string
		cmd  string
		args []string
	}{
		{name: "env time nested chdir", cmd: `env time env -C /hook git status`},
		{name: "time leads chdir", cmd: `time env -C /hook git status`},
		{name: "time option then chdir", cmd: `env time -p env -C /hook git status`},
		{name: "env time long chdir", cmd: `env time env --chdir=/hook git status`},
		{name: "explicit env time chdir", cmd: "env", args: ss("time", "env", "-C", "/hook", "git", "status")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := EvaluateCommand(sb, tc.cmd, tc.args)
			if d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q, %q) = %s, want ask (%s)", tc.cmd, tc.args, d.Action, d.Reason)
			} else if !strings.Contains(d.Reason, "env -C") {
				t.Errorf("EvaluateCommand(%q, %q) reason = %q, want env -C reason", tc.cmd, tc.args, d.Reason)
			}
		})
	}
	if d := EvaluateCommand(sb, `env time env -S 'rm -rf /'`, nil); d.Action != ActionDeny {
		t.Errorf("env time env -S rm = %s, want deny (%s)", d.Action, d.Reason)
	}
}

// TestSettledWordTrailingEnvChdirAsk pins the F-C fix: a settled plain
// utility does not end the chdir scan because the settled word is
// attacker-chosen and may delegate (time) or permute its operands
// (make/tar getopt), leaving a later env -C live.
func TestSettledWordTrailingEnvChdirAsk(t *testing.T) {
	sb := round2AllowSB()
	for _, cmd := range []string{
		`env ls env -C /hook git status`,
		`env cat env --chdir=/hook ls`,
		`env make env -C /hook env`,
		`env tar env -C /hook -cf o.tar .`,
	} {
		t.Run(cmd, func(t *testing.T) {
			d := EvaluateCommand(sb, cmd, nil)
			if d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) = %s, want ask (%s)", cmd, d.Action, d.Reason)
			} else if !strings.Contains(d.Reason, "directory override") {
				t.Errorf("EvaluateCommand(%q) reason = %q, want directory-override reason", cmd, d.Reason)
			}
		})
	}
}

// TestUtilityChdirFlagAsk pins the adjacent make/tar -C gap: their own
// -C/--directory option steers the working directory with no env/git layer,
// so it asks like a git -C override; plain and quoted uses stay green.
func TestUtilityChdirFlagAsk(t *testing.T) {
	sb := round2AllowSB()
	ask := []string{
		`make -C /hook`,
		`make -C/hook target`,
		`make -C/src/of target`,
		`make --directory=/hook`,
		`make --dir /hook`,
		`sudo make -C /hook`,
		`tar -C /hook -cf o.tar .`,
		`tar -xCf archive.tar /hook`,
		`tar --directory=/hook -xf a.tar`,
		`echo hi | tar -C /hook -xf a.tar`,
	}
	for _, cmd := range ask {
		t.Run("ask "+cmd, func(t *testing.T) {
			d := EvaluateCommand(sb, cmd, nil)
			if d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) = %s, want ask (%s)", cmd, d.Action, d.Reason)
			} else if !strings.Contains(d.Reason, "directory override") {
				t.Errorf("EvaluateCommand(%q) reason = %q, want directory-override reason", cmd, d.Reason)
			}
		})
	}
	green := []string{
		`make`,
		`make test`,
		`make -j4 test`,
		`tar -tf archive.tar`,
		`tar -cf o.tar -- -C`,
		`echo "make -C /hook"`,
		`echo "tar -C /hook"`,
	}
	for _, cmd := range green {
		t.Run("allow "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(sb, cmd, nil); d.Action != ActionAllow {
				t.Errorf("EvaluateCommand(%q) = %s, want allow (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// gateEnvLongKindString maps the gate's local classification onto the
// sandbox accessor's canonical strings so the two layers can be compared.
func gateEnvLongKindString(k envLongKind) string {
	switch k {
	case envLongSplitString:
		return "split-string"
	case envLongTakesValue:
		return "value"
	case envLongBoolean:
		return "boolean"
	}
	return ""
}

// TestToolOptionEnvInjectionAskDeny pins the TAR_OPTIONS/MAKEFLAGS/
// GNUMAKEFLAGS gap: make/tar parse these variables as pre-options before
// argv, so a hidden -C/--directory steers the run (Ask) and a hidden tar
// exec flag runs a program (Deny), with no argv flag the ordinary scans
// could see. Quoted values are the real bypass (the shell strips the quotes
// before the utility parses the value), so they are covered alongside the
// bare forms; benign option streams, an assignment after the utility
// (operand, not overlay), and quoted prose stay green.
func TestToolOptionEnvInjectionAskDeny(t *testing.T) {
	sb := round2AllowSB()
	ask := []struct {
		name string
		cmd  string
		args []string
	}{
		{name: "tar options chdir", cmd: `env TAR_OPTIONS='-C /hook' tar -xf a.tar`},
		{name: "tar options chdir attached", cmd: `env TAR_OPTIONS=-C/hook tar -xf a.tar`},
		{name: "tar options long chdir", cmd: `env TAR_OPTIONS='--directory=/hook' tar -xf a.tar`},
		{name: "tar options archive", cmd: `env TAR_OPTIONS='-f /etc/passwd' tar -x`},
		{name: "tar options archive bundled xvf", cmd: `env TAR_OPTIONS='-xvf /etc/passwd' tar -x`},
		{name: "tar options archive bundled tf", cmd: `env TAR_OPTIONS='-tf /etc/passwd' tar -x`},
		{name: "tar options long archive abbreviation", cmd: `env TAR_OPTIONS='--fil /etc/passwd' tar -x`},
		{name: "makeflags chdir", cmd: `env MAKEFLAGS='-C /hook' make`},
		{name: "gnumakeflags chdir", cmd: `env GNUMAKEFLAGS='--directory=/hook' make`},
		{name: "makeflags dashless chdir", cmd: `env MAKEFLAGS='C /hook' make`},
		{name: "gnumakeflags dashless chdir", cmd: `env GNUMAKEFLAGS='C /hook' make`},
		{name: "makeflags dashless chdir explicit", cmd: "env", args: ss("MAKEFLAGS=C /hook", "make")},
		{name: "gtar options chdir", cmd: `env TAR_OPTIONS='-C /hook' gtar -xf a.tar`},
		{name: "bsdtar options chdir", cmd: `env TAR_OPTIONS='-C /hook' bsdtar -xf a.tar`},
		{name: "gmake flags chdir", cmd: `env MAKEFLAGS='-C /hook' gmake`},
		{name: "argv dashless chdir", cmd: `tar cC /tmp -xf a.tar`},
		{name: "piped tail", cmd: `echo hi | env TAR_OPTIONS='-C /hook' tar -xf a.tar`},
		{name: "split-string tar", cmd: `env -S 'TAR_OPTIONS=-C /hook tar -xf a.tar'`},
		{name: "split-string make", cmd: `env -S 'MAKEFLAGS=-C /hook make'`},
		{name: "explicit argv", cmd: "env", args: ss("TAR_OPTIONS=-C /hook", "tar", "-xf", "a.tar")},
	}
	for _, tc := range ask {
		t.Run("ask "+tc.name, func(t *testing.T) {
			d := EvaluateCommand(sb, tc.cmd, tc.args)
			if d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q, %q) = %s, want ask (%s)", tc.cmd, tc.args, d.Action, d.Reason)
			} else if !strings.Contains(d.Reason, "override requires user approval") {
				t.Errorf("EvaluateCommand(%q, %q) reason = %q, want override reason", tc.cmd, tc.args, d.Reason)
			}
		})
	}
	deny := []struct {
		name string
		cmd  string
	}{
		{name: "to-command", cmd: `env TAR_OPTIONS='--to-command=id' tar -xf a.tar`},
		{name: "to-command separate", cmd: `env TAR_OPTIONS='--to-command id' tar -xf a.tar`},
		{name: "use-compress-program", cmd: `env TAR_OPTIONS='--use-compress-program=sh' tar -xf a.tar`},
		{name: "use-compress-program short", cmd: `env TAR_OPTIONS='-Ish' tar -xf a.tar`},
		{name: "checkpoint action", cmd: `env TAR_OPTIONS='--checkpoint-action=exec=id' tar -xf a.tar`},
		{name: "assignment prefix", cmd: `TAR_OPTIONS=--to-command=id tar -xf a.tar`},
		{name: "gtar to-command", cmd: `env TAR_OPTIONS='--to-command=id' gtar -xf a.tar`},
		{name: "bsdtar to-command", cmd: `env TAR_OPTIONS='--to-command=id' bsdtar -xf a.tar`},
		{name: "split-string to-command", cmd: `env -S 'TAR_OPTIONS=--to-command=id tar -xf a.tar'`},
		{name: "info-script short", cmd: `env TAR_OPTIONS='-F id' tar -xf a.tar`},
		{name: "info-script bundled", cmd: `env TAR_OPTIONS='-xF id' tar -xf a.tar`},
		{name: "rsh-command", cmd: `env TAR_OPTIONS='--rsh-command=id' tar -xf a.tar`},
		{name: "rmt-command", cmd: `env TAR_OPTIONS='--rmt-command=id' tar -xf a.tar`},
		{name: "argv info-script", cmd: `tar -F id -xf a.tar`},
		{name: "argv rsh-command", cmd: `tar --rsh-command=id -xf a.tar`},
		{name: "argv rmt-command", cmd: `tar --rmt-command=id -xf a.tar`},
		{name: "argv dashless compress", cmd: `tar cI prog -f a.tar`},
		{name: "argv dashless info-script", cmd: `tar cF prog -f a.tar`},
		{name: "argv dashless extract compress", cmd: `tar xI prog -f a.tar`},
		{name: "sudo argv dashless compress", cmd: `sudo tar cI prog -f a.tar`},
		{name: "piped argv dashless compress", cmd: `printf x | tar cI prog -f a.tar`},
		{name: "make argv file", cmd: `make -f evil.mk`},
		{name: "make argv file attached", cmd: `make -fevil.mk`},
		{name: "make argv file bundled", cmd: `make -nf evil.mk`},
		{name: "sudo make argv file", cmd: `sudo make --file=evil.mk`},
		{name: "make argv makefile", cmd: `make --makefile evil.mk target`},
		{name: "make argv eval", cmd: `make --eval='$(shell id)' target`},
		{name: "makeflags file", cmd: `env MAKEFLAGS='-f evil.mk' make`},
		{name: "makeflags dashless file", cmd: `env MAKEFLAGS='f evil.mk' make`},
		{name: "makeflags dashless bundled", cmd: `env MAKEFLAGS='nf evil.mk' make`},
		{name: "makeflags dashless attached", cmd: `env MAKEFLAGS=fevil.mk make`},
		{name: "gnumakeflags dashless file", cmd: `env GNUMAKEFLAGS='f evil.mk' make`},
		{name: "makeflags dashless assignment prefix", cmd: `MAKEFLAGS='f evil.mk' make`},
		{name: "gnumakeflags eval", cmd: `env GNUMAKEFLAGS='--eval=$(shell id)' make`},
		{name: "makefiles env", cmd: `env MAKEFILES=evil.mk make`},
		{name: "makefiles assignment prefix", cmd: `MAKEFILES=evil.mk make`},
		{name: "makefiles command-line variable", cmd: `make MAKEFILES=evil.mk target`},
		{name: "makeflags command-line variable", cmd: `make MAKEFLAGS='-f evil.mk' target`},
		{name: "make literal param opener", cmd: `make '${' -f evil.mk`},
		{name: "tar options literal param opener", cmd: `env X='${' TAR_OPTIONS='-I evil.sh' tar -xf a.tar`},
		{name: "makeflags literal param opener", cmd: `env X='${' MAKEFLAGS='-f evil.mk' make`},
		{name: "sort compress param default", cmd: `sort ${1:---compress-program=id} file`},
		{name: "sort compress literal param opener", cmd: `env X='${' sort --compress-program=id file`},
	}
	for _, tc := range deny {
		t.Run("deny "+tc.name, func(t *testing.T) {
			if d := EvaluateCommand(sb, tc.cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) = %s, want deny (%s)", tc.cmd, d.Action, d.Reason)
			}
		})
	}
	green := []string{
		`env TAR_OPTIONS='--no-same-owner' tar -xf a.tar`,
		`env MAKEFLAGS='-j4' make test`,
		`env MAKEFLAGS='j4' make test`,
		`env MAKEFLAGS='C=1' make test`,
		`env MAKEFLAGS= make`,
		`env MAKEFILES= make`,
		`echo 'TAR_OPTIONS=--to-command=id tar -xf a.tar'`,
		`tar -xf a.tar TAR_OPTIONS=--to-command=id`,
		`tar cf a.tar file`,
		`tar cvf a.tar file`,
		`tar tvf a.tar`,
		`tar xf a.tar`,
		`tar -f a.tar cI`,
	}
	for _, cmd := range green {
		t.Run("allow "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(sb, cmd, nil); d.Action != ActionAllow {
				t.Errorf("EvaluateCommand(%q) = %s, want allow (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
	// Environment names are byte-exact on non-Windows: tar/make only read
	// the canonical spelling, so a folded name is inert and must not
	// over-deny. Windows folds (names are case-insensitive there).
	if runtime.GOOS != "windows" {
		for _, cmd := range []string{
			`env tar_options='--to-command=id' tar -xf a.tar`,
			`env makefiles=evil.mk make`,
			`env makeflags='f evil.mk' make`,
			`env gnumakeflags='C /hook' make`,
		} {
			t.Run("case-exact "+cmd, func(t *testing.T) {
				if d := EvaluateCommand(sb, cmd, nil); d.Action != ActionAllow {
					t.Errorf("EvaluateCommand(%q) = %s, want allow (byte-exact key) (%s)", cmd, d.Action, d.Reason)
				}
			})
		}
	}
}

// TestTarShortExecFlagLeftToRightTable pins the getopt-style short-cluster
// walk: -I/-F deny before any plain value-taker, and once a plain
// value-taker appears the rest of the token is its argument (a letter F or
// I in a directory/archive name is not an exec flag).
func TestTarShortExecFlagLeftToRightTable(t *testing.T) {
	cases := []struct {
		name string
		form string
		want bool
	}{
		{name: "exact I", form: "-I", want: true},
		{name: "attached I", form: "-Iprog", want: true},
		{name: "bundled I", form: "-xI", want: true},
		{name: "bundled I with value", form: "-xIv", want: true},
		{name: "exact F", form: "-F", want: true},
		{name: "attached F", form: "-Fscript", want: true},
		{name: "bundled F", form: "-xF", want: true},
		{name: "directory name with F", form: "-C/home/Foo", want: false},
		{name: "directory name with I", form: "-C/opt/I", want: false},
		{name: "C consumes the cluster", form: "-Cf", want: false},
		{name: "archive named I", form: "-fI", want: false},
		{name: "archive name with F", form: "-xvfarchive.F", want: false},
		{name: "files-from name with F", form: "-TF", want: false},
		{name: "lowercase i unrelated", form: "-i", want: false},
		{name: "long flag not short", form: "--info-script", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tarShortExecFlag(tc.form); got != tc.want {
				t.Errorf("tarShortExecFlag(%q) = %v, want %v", tc.form, got, tc.want)
			}
		})
	}
}

// TestTarOldStyleExecFlagTable pins GNU tar's old option style: the
// dashless first word after the binary is a flag cluster, so cI/cF/xI deny
// and the value-taking f ends the walk (cf/cvf/tvf stay out). cC is a
// value-taker here too; the directory Ask leg owns it.
func TestTarOldStyleExecFlagTable(t *testing.T) {
	cases := []struct {
		name string
		word string
		want bool
	}{
		{name: "compress program", word: "cI", want: true},
		{name: "info script", word: "cF", want: true},
		{name: "extract compress program", word: "xI", want: true},
		{name: "default compress program", word: "${UNSET:-cI}", want: true},
		{name: "default info script", word: "${X:-cF}", want: true},
		{name: "default extract compress program", word: "${X:-xI}", want: true},
		{name: "default dashed compress program", word: "${X:--cI}", want: true},
		{name: "default benign cluster", word: "${X:-cvf}", want: false},
		{name: "trim operator is not a default", word: "${X#cI}", want: false},
		{name: "chdir value taker", word: "cC", want: false},
		{name: "create archive", word: "cf", want: false},
		{name: "create verbose archive", word: "cvf", want: false},
		{name: "list verbose archive", word: "tvf", want: false},
		{name: "extract archive", word: "xf", want: false},
		{name: "already dashed", word: "-cI", want: false},
		{name: "empty", word: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tarOldStyleExecFlag(tc.word); got != tc.want {
				t.Errorf("tarOldStyleExecFlag(%q) = %v, want %v", tc.word, got, tc.want)
			}
		})
	}
}

// TestTarWindowOldStyleExecFlag pins the string-leg window scan: a packed
// payload whose dashless cluster follows the tar binary denies, while the
// value-taking f forms and a later dashless operand stay out.
func TestTarWindowOldStyleExecFlag(t *testing.T) {
	for _, window := range []string{
		"tar cI prog",
		"tar cF script",
		"gtar xI prog",
		"tar cI${IFS}prog",
	} {
		if !tarWindowHasExecFlag(window, true) {
			t.Errorf("tarWindowHasExecFlag(%q, true) = false, want true", window)
		}
	}
	for _, window := range []string{
		"tar cf out.tar",
		"tar cvf out.tar",
		"tar tvf out.tar",
		"tar xf out.tar",
		"tar -f out.tar cI",
	} {
		if tarWindowHasExecFlag(window, true) {
			t.Errorf("tarWindowHasExecFlag(%q, true) = true, want false", window)
		}
	}
}

// TestCommandPositionForTable pins the shared command-position rule used to
// scope tar's dashless old style: wrapper/delegating prefixes, shell
// command-opening words and their flags stay transparent, while a plain
// command's argument ("echo tar cI") is prose.
func TestCommandPositionForTable(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		i    int
		want bool
	}{
		{name: "leads argv", argv: ss("tar", "cI"), i: 0, want: true},
		{name: "plain argument", argv: ss("echo", "tar"), i: 1},
		{name: "sudo", argv: ss("sudo", "tar"), i: 1, want: true},
		{name: "sudo user operand", argv: ss("sudo", "-u", "root", "tar"), i: 3, want: true},
		{name: "env", argv: ss("env", "tar"), i: 1, want: true},
		{name: "xargs", argv: ss("xargs", "tar"), i: 1, want: true},
		{name: "timeout duration", argv: ss("timeout", "10s", "tar"), i: 2, want: true},
		{name: "find exec", argv: ss("find", ".", "-exec", "tar"), i: 3, want: true},
		{name: "command builtin", argv: ss("command", "tar"), i: 1, want: true},
		{name: "command builtin flag", argv: ss("command", "-p", "tar"), i: 2, want: true},
		{name: "then keyword", argv: ss("then", "tar"), i: 1, want: true},
		{name: "bang keyword", argv: ss("!", "tar"), i: 1, want: true},
		{name: "while condition", argv: ss("while", "tar"), i: 1, want: true},
		{name: "nested keyword then sudo", argv: ss("then", "sudo", "tar"), i: 2, want: true},
		{name: "exec builtin", argv: ss("exec", "tar"), i: 1, want: true},
		{name: "builtin builtin", argv: ss("builtin", "tar"), i: 1, want: true},
		{name: "else keyword", argv: ss("else", "tar"), i: 1, want: true},
		{name: "do keyword", argv: ss("do", "tar"), i: 1, want: true},
		{name: "subshell opener", argv: ss("(", "tar"), i: 1, want: true},
		{name: "group opener", argv: ss("{", "tar"), i: 1, want: true},
		{name: "attached subshell opener", argv: ss("(sudo", "tar"), i: 1, want: true},
		{name: "nested subshell then sudo", argv: ss("(", "sudo", "tar"), i: 2, want: true},
		{name: "arg after builtin", argv: ss("echo", "command", "tar"), i: 2},
		{name: "grep operand", argv: ss("grep", "-r", "tar"), i: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandPositionFor(tc.argv, tc.i); got != tc.want {
				t.Errorf("commandPositionFor(%q, %d) = %v, want %v", tc.argv, tc.i, got, tc.want)
			}
		})
	}
}

// TestShellPrefixWalkAgreement pins the single shared prefix walk behind
// interpreterTailReachable (hard-deny reachability) and effectiveCommandArgv
// (Ask-side effective command). Both drive the same shellPrefixWalk, so for
// any prefix token the two must agree: the interpreter at i is reachable
// exactly when the first opaque token is not before i. Parameter expansions
// are the one deliberate divergence (transparent for reachability, opaque for
// effective-command resolution) and are pinned separately below.
func TestShellPrefixWalkAgreement(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		opaque  int      // index of the first effective command, -1 when all prefix
		wantEff []string // nil when every token is prefix material
	}{
		{name: "bare command", argv: ss("tar", "cI"), opaque: 0, wantEff: ss("tar", "cI")},
		{name: "wrapper", argv: ss("sudo", "tar"), opaque: 1, wantEff: ss("tar")},
		{
			name:    "command then sudo",
			argv:    ss("command", "sudo", "-u", "root", "tar"),
			opaque:  4,
			wantEff: ss("tar"),
		},
		{name: "bang then shell", argv: ss("!", "bash", "-c", "id"), opaque: 1, wantEff: ss("bash", "-c", "id")},
		{name: "then keyword", argv: ss("then", "python3", "x.py"), opaque: 1, wantEff: ss("python3", "x.py")},
		{name: "subshell opener", argv: ss("(", "sudo", "tar"), opaque: 2, wantEff: ss("tar")},
		{name: "attached opener", argv: ss("(sudo", "-s", "tar"), opaque: 2, wantEff: ss("tar")},
		{name: "grouped command closer", argv: ss("(", "tar", ")"), opaque: 1, wantEff: ss("tar")},
		{name: "find exec", argv: ss("find", ".", "-exec", "tar"), opaque: 3, wantEff: ss("tar")},
		{name: "timeout duration", argv: ss("timeout", "10s", "tar"), opaque: 2, wantEff: ss("tar")},
		{name: "prose argument", argv: ss("echo", "tar", "cI"), opaque: 0, wantEff: ss("echo", "tar", "cI")},
		{name: "prose after builtin", argv: ss("echo", "command", "tar"), opaque: 0, wantEff: ss("echo", "command", "tar")},
		{name: "all prefix", argv: ss("sudo", "-u", "root"), opaque: -1, wantEff: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotEff := effectiveCommandArgv(tc.argv)
			if !reflect.DeepEqual(gotEff, tc.wantEff) {
				t.Fatalf("effectiveCommandArgv(%q) = %q, want %q", tc.argv, gotEff, tc.wantEff)
			}
			for i := 0; i < len(tc.argv); i++ {
				want := tc.opaque < 0 || i <= tc.opaque
				if got := interpreterTailReachable(tc.argv, i); got != want {
					t.Errorf("interpreterTailReachable(%q, %d) = %v, want %v (effectiveCommandArgv=%q)",
						tc.argv, i, got, want, gotEff)
				}
			}
		})
	}

	// Deliberate divergence: an unresolvable expansion is transparent to the
	// reachability walk (the word after it stays in command position) but is
	// the unresolvable effective command for the Ask walk.
	for _, tc := range []struct {
		argv []string
		i    int
	}{
		{argv: ss("$CMD", "tar", "cI"), i: 1},
		{argv: ss("sudo", "$CMD", "tar"), i: 2},
		{argv: ss("command", "$CMD", "tar"), i: 2},
	} {
		if !interpreterTailReachable(tc.argv, tc.i) {
			t.Errorf("interpreterTailReachable(%q, %d) = false, want true (expansion transparent)", tc.argv, tc.i)
		}
		if eff := effectiveCommandArgv(tc.argv); len(eff) == 0 || !hasParamExpansion(eff[0]) {
			t.Errorf("effectiveCommandArgv(%q) = %q, want head at the expansion", tc.argv, eff)
		}
	}
}

// TestMakeOptionEnvFormsTable pins the implicit leading dash MAKEFLAGS and
// GNUMAKEFLAGS accept on the first word: `f evil.mk` is `-f evil.mk`,
// `C /tmp` is `-C /tmp`. Only the first word gets the dash; assignments,
// the -- separator, and benign short clusters stay as they are.
func TestMakeOptionEnvFormsTable(t *testing.T) {
	cases := []struct {
		name     string
		val      string
		wantExec bool
		wantAsk  bool
	}{
		{name: "dashless file", val: "f evil.mk", wantExec: true},
		{name: "dashless bundled file", val: "nf evil.mk", wantExec: true},
		{name: "dashless attached file", val: "fevil.mk", wantExec: true},
		{name: "dashless chdir", val: "C /tmp", wantAsk: true},
		{name: "dashless chdir attached", val: "C/tmp", wantAsk: true},
		{name: "dashed file", val: "-f evil.mk", wantExec: true},
		{name: "long eval", val: "--eval=$(shell id)", wantExec: true},
		{name: "dashless benign jobs", val: "j4"},
		{name: "assignment is not a flag", val: "C=1"},
		{name: "dash separator", val: "-- f evil.mk"},
		{name: "empty", val: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotExec, gotAsk := false, false
			for _, form := range makeOptionEnvForms(tc.val) {
				if makeOptionExecForm(form) {
					gotExec = true
				}
				if makeOptionAskForm("MAKEFLAGS", form) != "" {
					gotAsk = true
				}
			}
			if gotExec != tc.wantExec || gotAsk != tc.wantAsk {
				t.Errorf("makeOptionEnvForms(%q) exec=%v ask=%v, want exec=%v ask=%v", tc.val, gotExec, gotAsk, tc.wantExec, tc.wantAsk)
			}
		})
	}
}

// TestMakeOptionCaseFold pins the platform split for tool-option keys:
// byte-exact off Windows, folded on Windows (environment names are
// case-insensitive there).
func TestMakeOptionCaseFold(t *testing.T) {
	caseInsensitive := runtime.GOOS == "windows"
	for _, tc := range []struct {
		key, want string
	}{
		{"MAKEFLAGS", "MAKEFLAGS"},
		{"GNUMAKEFLAGS", "GNUMAKEFLAGS"},
		{"MAKEFILES", "MAKEFILES"},
		{"TAR_OPTIONS", "TAR_OPTIONS"},
	} {
		if !toolOptionEnvKey(tc.key, tc.want) {
			t.Errorf("toolOptionEnvKey(%q, %q) = false, want true", tc.key, tc.want)
		}
	}
	for _, key := range []string{"makeflags", "Makefiles", "tar_options"} {
		if got, want := toolOptionEnvKey(key, strings.ToUpper(key)), caseInsensitive; got != want {
			t.Errorf("toolOptionEnvKey(%q, folded) = %v, want %v (windows=%v)", key, got, want, caseInsensitive)
		}
	}
}

// TestEnvLongOptionsParity pins the single-source long-option table: the
// gate resolves exactly the sandbox-owned list, no name is duplicated, and
// gate/sandbox agree on every exact name and every unique prefix (including
// ambiguous prefixes resolving unknown on both layers).
func TestEnvLongOptionsParity(t *testing.T) {
	names := sandbox.EnvLongOptionNames()
	if !reflect.DeepEqual(names, envLongOptions) {
		t.Fatalf("gate envLongOptions = %q, want sandbox canonical %q", envLongOptions, names)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Errorf("duplicate env long option %q", name)
		}
		seen[name] = true
		if kind := sandbox.EnvLongOptionKind(name); kind == "" {
			t.Errorf("sandbox EnvLongOptionKind(%q) = unknown", name)
		}
		if got, want := gateEnvLongKindString(resolveEnvLong(name)), sandbox.EnvLongOptionKind(name); got != want {
			t.Errorf("exact %q: gate kind %q, sandbox kind %q", name, got, want)
		}
		for i := 1; i < len(name); i++ {
			prefix := name[:i]
			got := gateEnvLongKindString(resolveEnvLong(prefix))
			want := sandbox.EnvLongOptionKind(prefix)
			if got != want {
				t.Errorf("prefix %q: gate kind %q, sandbox kind %q", prefix, got, want)
			}
		}
	}
	for _, unknown := range []string{"zznope", "d", ""} {
		if got := gateEnvLongKindString(resolveEnvLong(unknown)); got != "" {
			t.Errorf("gate resolveEnvLong(%q) = %q, want unknown", unknown, got)
		}
		if got := sandbox.EnvLongOptionKind(unknown); got != "" {
			t.Errorf("sandbox EnvLongOptionKind(%q) = %q, want unknown", unknown, got)
		}
	}
	// The accessor returns a copy: mutating it cannot change resolution.
	mutated := sandbox.EnvLongOptionNames()
	mutated[0] = "zznope"
	if got := sandbox.EnvLongOptionKind("ignore-environment"); got == "" {
		t.Errorf("mutating EnvLongOptionNames() result changed the canonical resolver")
	}
}

// TestEnvSplitStringCrossLayerParity pins the shared -S lexer (Tier1 F2):
// the gate expansion and the sandbox splice tokenizer consume the same
// corpus the same way, including escapes, empty quotes, and unparseable
// shapes (both fail closed).
func TestEnvSplitStringCrossLayerParity(t *testing.T) {
	corpus := []string{
		"ls",
		"echo hi",
		"-C /hook ls",
		"FOO=bar git status",
		`a\ b`,
		`"a b" c`,
		`'a b' c`,
		`a"b c"d`,
		`\$HOME`,
		`a\\b`,
		`''`,
		`"a\"b"`,
		`'a'\"'\"'b'`,
		`"unterminated`,
		`trailing\`,
	}
	for _, val := range corpus {
		t.Run(val, func(t *testing.T) {
			want, ok := sandbox.SplitEnvSplitString(val)
			got, bad := expandEnvSplitString([]string{"-S", val})
			if ok == bad {
				t.Fatalf("SplitEnvSplitString(%q) ok=%v but gate bad=%v", val, ok, bad)
			}
			if ok && !reflect.DeepEqual(got, want) {
				t.Errorf("gate expansion(%q) = %q, sandbox lexer = %q", val, got, want)
			}
		})
	}
}

// TestExpandSpliceOverApprox pins the deliberate any-position divergence
// (#6): past the utility position the gate splices -S anywhere before
// "--" (fail-closed over-approximation), while the sandbox spliceEnvLayer
// stops at the utility and leaves operands untouched. `env ls -S foo`
// runs ls with operand "-S foo"; the gate splices it to ["ls" "foo"] and
// the sandbox keeps ["ls" "-S" "foo"]. The gate direction is safe (it can
// only over-Ask/over-Deny, never Allow a hidden payload), so the pin
// documents the difference instead of "fixing" one layer.
func TestExpandSpliceOverApprox(t *testing.T) {
	got, bad := expandEnvSplitString([]string{"ls", "-S", "foo"})
	if bad {
		t.Fatalf("expandEnvSplitString(ls -S foo) = unknown, want over-approx splice")
	}
	if !reflect.DeepEqual(got, []string{"ls", "foo"}) {
		t.Errorf("expandEnvSplitString(ls -S foo) = %q, want [ls foo] (any-position over-approx)", got)
	}
}
