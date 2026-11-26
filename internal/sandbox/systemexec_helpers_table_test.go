package sandbox

import (
	"reflect"
	"strings"
	"testing"
)

// TestSplitShellSegmentsLiteOps pins the pre-gate lexer's deliberate
// subset contract (see splitShellSegmentsWithSeps): no &>/>& redirect
// handling and no escaped-operator tracking, because the pre-gate only
// needs boundaries and cd operands. The gate-side splitShellSegments
// (internal/agent) keeps `2>&1` and `&>` in one segment; this lexer
// splits them. Do not "fix" one lexer without updating the other and
// its pin test (TestSplitShellSegmentsLexerParity in internal/agent).
func TestSplitShellSegmentsLiteOps(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string
	}{
		{name: "and chain", cmd: "a && b", want: []string{"a", "b"}},
		{name: "or chain", cmd: "a || b", want: []string{"a", "b"}},
		{name: "semicolon chain", cmd: "a; b", want: []string{"a", "b"}},
		{name: "quoted chain stays one", cmd: `echo "a && b"`, want: []string{`echo "a && b"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitShellSegmentsLite(tc.cmd); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitShellSegmentsLite(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}

	// Divergence pins: the lite lexer splits FD-duplication and &>
	// redirects that the gate lexer keeps whole.
	divergent := []struct {
		name     string
		cmd      string
		wantSegs int
	}{
		{name: "fd duplication splits", cmd: "a 2>&1 b", wantSegs: 2},
		{name: "amp redirect splits", cmd: "echo hi &> file", wantSegs: 2},
	}
	for _, tc := range divergent {
		t.Run(tc.name, func(t *testing.T) {
			got := splitShellSegmentsLite(tc.cmd)
			if len(got) != tc.wantSegs {
				t.Errorf("splitShellSegmentsLite(%q) = %q (%d segs), want %d segs (gate keeps 1: redirect-aware)", tc.cmd, got, len(got), tc.wantSegs)
			}
		})
	}
}

// TestSpliceEnvLayerTable pins one env option layer: assignments pass
// through, -S splices inline, -C/--chdir operands are collected (never
// silently consumed), value-takers consume their operand, and missing
// operands or unknown/ambiguous longs fail closed with unknown=true.
func TestSpliceEnvLayerTable(t *testing.T) {
	cases := []struct {
		name       string
		rest       []string
		wantOut    []string
		wantChdirs []string
		wantUnk    bool
	}{
		{name: "assigns pass through", rest: []string{"FOO=bar", "ls"}, wantOut: []string{"FOO=bar", "ls"}},
		{name: "short chdir separate", rest: []string{"-C", "/hook", "ls"}, wantOut: []string{"ls"}, wantChdirs: []string{"/hook"}},
		{name: "short chdir attached", rest: []string{"-C/hook", "ls"}, wantOut: []string{"ls"}, wantChdirs: []string{"/hook"}},
		{name: "short chdir attached S value", rest: []string{"-CS", "ls"}, wantOut: []string{"ls"}, wantChdirs: []string{"S"}},
		{name: "short chdir attached Sh value", rest: []string{"-CSh", "ls"}, wantOut: []string{"ls"}, wantChdirs: []string{"Sh"}},
		{name: "cluster iC with S value", rest: []string{"-iCS", "ls"}, wantOut: []string{"ls"}, wantChdirs: []string{"S"}},
		{name: "unset C attached no chdir", rest: []string{"-uC", "ls"}, wantOut: []string{"ls"}},
		{name: "unset S attached no chdir", rest: []string{"-uS", "ls"}, wantOut: []string{"ls"}},
		{name: "archive S attached no chdir", rest: []string{"-aS", "ls"}, wantOut: []string{"ls"}},
		{name: "long chdir separate", rest: []string{"--chdir", "/hook", "ls"}, wantOut: []string{"ls"}, wantChdirs: []string{"/hook"}},
		{name: "long chdir equals", rest: []string{"--chdir=/hook", "ls"}, wantOut: []string{"ls"}, wantChdirs: []string{"/hook"}},
		{name: "long abbrev chdir", rest: []string{"--ch", "/hook", "ls"}, wantOut: []string{"ls"}, wantChdirs: []string{"/hook"}},
		{name: "unset consumes operand", rest: []string{"-u", "FOO", "ls"}, wantOut: []string{"ls"}},
		{name: "split-string splices", rest: []string{"-S", "FOO=bar ls"}, wantOut: []string{"FOO=bar", "ls"}},
		{name: "dashes stops options", rest: []string{"--", "-C"}, wantOut: []string{"-C"}},
		{name: "missing chdir operand", rest: []string{"-C"}, wantUnk: true},
		{name: "missing unset operand", rest: []string{"-u"}, wantUnk: true},
		{name: "missing long operand", rest: []string{"--chdir"}, wantUnk: true},
		{name: "unknown long", rest: []string{"--zznope", "ls"}, wantUnk: true},
		{name: "ambiguous long", rest: []string{"--d", "ls"}, wantUnk: true},
		{name: "unparseable split-string", rest: []string{"-S", "'unterminated"}, wantUnk: true},
		{name: "SE-1 assign before nested env unwraps chdir", rest: []string{"X=1", "env", "-C", "/evil", "git", "status"}, wantOut: []string{"X=1", "git", "status"}, wantChdirs: []string{"/evil"}},
		{name: "SE-1 nested env after dashes unwraps", rest: []string{"--", "env", "-C", "/evil", "ls"}, wantOut: []string{"ls"}, wantChdirs: []string{"/evil"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, chdirs, unk := spliceEnvLayer(tc.rest)
			if unk != tc.wantUnk {
				t.Fatalf("spliceEnvLayer(%q) unknown = %v, want %v (out=%q chdirs=%q)", tc.rest, unk, tc.wantUnk, out, chdirs)
			}
			if unk {
				return
			}
			if !reflect.DeepEqual(out, tc.wantOut) {
				t.Errorf("spliceEnvLayer(%q) out = %q, want %q", tc.rest, out, tc.wantOut)
			}
			if !reflect.DeepEqual(chdirs, tc.wantChdirs) {
				t.Errorf("spliceEnvLayer(%q) chdirs = %q, want %q", tc.rest, chdirs, tc.wantChdirs)
			}
		})
	}
}

// TestUnwrapEnvLayersTable pins nested env unwrapping: assignments are
// preserved ahead of the utility, every layer's -C operands accumulate in
// order, and any unparseable layer fails closed.
func TestUnwrapEnvLayersTable(t *testing.T) {
	cases := []struct {
		name       string
		argv       []string
		wantEff    []string
		wantChdirs []string
		wantUnk    bool
	}{
		{name: "bare env", argv: []string{"env"}, wantUnk: false},
		{name: "plain utility", argv: []string{"env", "git", "status"}, wantEff: []string{"git", "status"}},
		{name: "assign preserved", argv: []string{"env", "FOO=bar", "git", "status"}, wantEff: []string{"FOO=bar", "git", "status"}},
		{name: "chdir collected", argv: []string{"env", "-C", "/hook", "git", "status"}, wantEff: []string{"git", "status"}, wantChdirs: []string{"/hook"}},
		{name: "nested chdirs accumulate", argv: []string{"env", "-C", "/a", "env", "-C", "sub", "git", "status"}, wantEff: []string{"git", "status"}, wantChdirs: []string{"/a", "sub"}},
		{name: "split splice", argv: []string{"env", "-S", "FOO=bar git status"}, wantEff: []string{"FOO=bar", "git", "status"}},
		{name: "not env", argv: []string{"git", "status"}, wantEff: []string{"git", "status"}},
		{name: "trailing chdir unknown", argv: []string{"env", "-C"}, wantUnk: true},
		{name: "SE-1 split splice with assign before nested env", argv: []string{"env", "-S", "X=1 env -C /evil git status"}, wantEff: []string{"X=1", "git", "status"}, wantChdirs: []string{"/evil"}},
		{name: "SE-1 assign before nested env", argv: []string{"env", "X=1", "env", "-C", "/evil", "git", "status"}, wantEff: []string{"X=1", "git", "status"}, wantChdirs: []string{"/evil"}},
		{name: "unknown long unknown", argv: []string{"env", "--zznope", "ls"}, wantUnk: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eff, chdirs, unk := unwrapEnvLayers(tc.argv)
			if unk != tc.wantUnk {
				t.Fatalf("unwrapEnvLayers(%q) unknown = %v, want %v (eff=%q chdirs=%q)", tc.argv, unk, tc.wantUnk, eff, chdirs)
			}
			if unk {
				return
			}
			if !reflect.DeepEqual(eff, tc.wantEff) {
				t.Errorf("unwrapEnvLayers(%q) eff = %q, want %q", tc.argv, eff, tc.wantEff)
			}
			if !reflect.DeepEqual(chdirs, tc.wantChdirs) {
				t.Errorf("unwrapEnvLayers(%q) chdirs = %q, want %q", tc.argv, chdirs, tc.wantChdirs)
			}
		})
	}
}

// TestScanGitInToksTable pins git discovery: leading, wrapper-prefixed,
// and env-trampolined positions are found with assignments and chdirs;
// quoted payloads never match; unparseable layers report unknown.
func TestScanGitInToksTable(t *testing.T) {
	cases := []struct {
		name       string
		toks       []string
		wantOk     bool
		wantAssign []string
		wantChdirs []string
		wantUnk    bool
	}{
		{name: "direct git", toks: []string{"git", "status"}, wantOk: true},
		{name: "sudo git", toks: []string{"sudo", "git", "status"}, wantOk: true},
		{name: "assign overlay", toks: []string{"GIT_TRACE=1", "git", "status"}, wantOk: true, wantAssign: []string{"GIT_TRACE=1"}},
		{name: "env trampoline", toks: []string{"env", "GIT_TRACE=1", "git", "status"}, wantOk: true, wantAssign: []string{"GIT_TRACE=1"}},
		{name: "env chdir", toks: []string{"env", "-C", "/hook", "git", "status"}, wantOk: true, wantChdirs: []string{"/hook"}},
		{name: "sudo env chdir", toks: []string{"sudo", "env", "-C", "/hook", "git", "status"}, wantOk: true, wantChdirs: []string{"/hook"}},
		{name: "time env chdir", toks: []string{"env", "time", "env", "-C", "/hook", "git", "status"}, wantOk: true, wantChdirs: []string{"/hook"}},
		{name: "settled word trailing env chdir", toks: []string{"env", "ls", "env", "-C", "/hook", "git", "status"}, wantOk: true, wantChdirs: []string{"/hook"}},
		{name: "no git", toks: []string{"ls", "-la"}, wantOk: false},
		{name: "quoted git stays green", toks: []string{"echo", "git status"}, wantOk: false},
		{name: "unparseable env reports unknown", toks: []string{"env", "--zznope", "ls"}, wantOk: false, wantUnk: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assigns, chdirs, ok, unk := scanGitInToks(tc.toks)
			if ok != tc.wantOk {
				t.Fatalf("scanGitInToks(%q) ok = %v, want %v", tc.toks, ok, tc.wantOk)
			}
			if unk != tc.wantUnk {
				t.Errorf("scanGitInToks(%q) unknown = %v, want %v", tc.toks, unk, tc.wantUnk)
			}
			if !reflect.DeepEqual(assigns, tc.wantAssign) {
				t.Errorf("scanGitInToks(%q) assigns = %q, want %q", tc.toks, assigns, tc.wantAssign)
			}
			if !reflect.DeepEqual(chdirs, tc.wantChdirs) {
				t.Errorf("scanGitInToks(%q) chdirs = %q, want %q", tc.toks, chdirs, tc.wantChdirs)
			}
		})
	}
}

// TestEnvChdirInToksTable pins non-git-led -C detection: `env -C /hook
// ls` reports found even without git, sudo-prefixed layers count, and
// unparseable layers report unknown.
func TestEnvChdirInToksTable(t *testing.T) {
	cases := []struct {
		name    string
		toks    []string
		want    bool
		wantUnk bool
	}{
		{name: "plain chdir", toks: []string{"env", "-C", "/hook", "ls"}, want: true},
		{name: "sudo chdir", toks: []string{"sudo", "env", "-C", "/hook", "ls"}, want: true},
		{name: "long chdir", toks: []string{"env", "--chdir=/hook", "ls"}, want: true},
		{name: "time nested chdir", toks: []string{"env", "time", "env", "-C", "/hook", "ls"}, want: true},
		{name: "settled word trailing env chdir", toks: []string{"env", "ls", "env", "-C", "/hook", "git"}, want: true},
		{name: "no chdir", toks: []string{"env", "ls"}, want: false},
		{name: "flag operand not option", toks: []string{"ls", "-C"}, want: false},
		{name: "unparseable", toks: []string{"env", "--zznope", "ls"}, wantUnk: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found, unk := envChdirInToks(tc.toks)
			if found != tc.want {
				t.Errorf("envChdirInToks(%q) = %v, want %v", tc.toks, found, tc.want)
			}
			if unk != tc.wantUnk {
				t.Errorf("envChdirInToks(%q) unknown = %v, want %v", tc.toks, unk, tc.wantUnk)
			}
		})
	}
}

// TestCountSubshellParensTable pins quote-aware paren counting: quoted
// parens never open a subshell, escapes are honored, and $(...) counts
// as a balanced subshell pair.
func TestCountSubshellParensTable(t *testing.T) {
	cases := []struct {
		name       string
		seg        string
		wantOpens  int
		wantCloses int
	}{
		{name: "bare open", seg: "(cd /hook", wantOpens: 1, wantCloses: 0},
		{name: "balanced", seg: "(cd /hook)", wantOpens: 1, wantCloses: 1},
		{name: "no parens", seg: "cd /hook", wantOpens: 0, wantCloses: 0},
		{name: "single-quoted parens", seg: "echo '('", wantOpens: 0, wantCloses: 0},
		{name: "double-quoted parens", seg: `echo "("`, wantOpens: 0, wantCloses: 0},
		{name: "substitution balanced", seg: "echo $(whoami)", wantOpens: 1, wantCloses: 1},
		{name: "close only", seg: "git status)", wantOpens: 0, wantCloses: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opens, closes := countSubshellParens(tc.seg)
			if opens != tc.wantOpens || closes != tc.wantCloses {
				t.Errorf("countSubshellParens(%q) = (%d,%d), want (%d,%d)", tc.seg, opens, closes, tc.wantOpens, tc.wantCloses)
			}
		})
	}
}

// TestStripCdGroupOpenersTable pins opener stripping: leading (,{ and
// attached (cd shapes strip (brace cds persist like bare cd), the
// trailing closer trims, and ungrouped operands keep their bytes.
func TestStripCdGroupOpenersTable(t *testing.T) {
	cases := []struct {
		name        string
		toks        []string
		want        []string
		wantGrouped bool
	}{
		{name: "bare cd", toks: []string{"cd", "/hook"}, want: []string{"cd", "/hook"}, wantGrouped: false},
		{name: "attached open", toks: []string{"(cd", "/hook"}, want: []string{"cd", "/hook"}, wantGrouped: true},
		{name: "standalone open", toks: []string{"(", "cd", "/hook"}, want: []string{"cd", "/hook"}, wantGrouped: true},
		{name: "brace open", toks: []string{"{", "cd", "/hook"}, want: []string{"cd", "/hook"}, wantGrouped: true},
		{name: "trailing closer trims", toks: []string{"(cd", "/x)"}, want: []string{"cd", "/x"}, wantGrouped: true},
		{name: "unguarded paren operand keeps bytes", toks: []string{"cd", "(weird"}, want: []string{"cd", "(weird"}, wantGrouped: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, grouped := stripCdGroupOpeners(tc.toks)
			if grouped != tc.wantGrouped {
				t.Errorf("stripCdGroupOpeners(%q) grouped = %v, want %v", tc.toks, grouped, tc.wantGrouped)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("stripCdGroupOpeners(%q) = %q, want %q", tc.toks, got, tc.want)
			}
		})
	}
}

// TestResolveCdTargetTable pins cd replay: absolute replaces, relative
// joins, flags/-- are skipped, and unresolvable shapes (bare pushd,
// popd, dash, unknown vars, extra operands) report ok=false.
func TestResolveCdTargetTable(t *testing.T) {
	lookup := func(k string) (string, bool) {
		if k == "HOME" {
			return "/home/u", true
		}
		if k == "HK_OK" {
			return "/ok", true
		}
		return "", false
	}
	cases := []struct {
		name   string
		cur    string
		toks   []string
		want   string
		wantOk bool
	}{
		{name: "absolute replaces", cur: "/base", toks: []string{"cd", "/hook"}, want: "/hook", wantOk: true},
		{name: "relative joins", cur: "/base", toks: []string{"cd", "sub"}, want: "/base/sub", wantOk: true},
		{name: "flags skipped", cur: "/base", toks: []string{"cd", "-L", "/hook"}, want: "/hook", wantOk: true},
		{name: "dashes literal", cur: "/base", toks: []string{"cd", "--", "-weird"}, want: "/base/-weird", wantOk: true},
		{name: "bare cd homes", cur: "/base", toks: []string{"cd"}, want: "/home/u", wantOk: true},
		{name: "var expands", cur: "/base", toks: []string{"cd", "$HK_OK"}, want: "/ok", wantOk: true},
		{name: "bare pushd unknown", cur: "/base", toks: []string{"pushd"}, wantOk: false},
		{name: "popd unknown", cur: "/base", toks: []string{"popd"}, wantOk: false},
		{name: "dash unknown", cur: "/base", toks: []string{"cd", "-"}, wantOk: false},
		{name: "unknown var", cur: "/base", toks: []string{"cd", "$HK_MISSING"}, wantOk: false},
		{name: "extra operand", cur: "/base", toks: []string{"cd", "/x", "extra"}, wantOk: false},
		{name: "tilde user unknown", cur: "/base", toks: []string{"cd", "~other"}, wantOk: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveCdTarget(tc.cur, tc.toks, lookup)
			if ok != tc.wantOk {
				t.Fatalf("resolveCdTarget(%q, %q) ok = %v, want %v (got %q)", tc.cur, tc.toks, ok, tc.wantOk, got)
			}
			if ok && got != tc.want {
				t.Errorf("resolveCdTarget(%q, %q) = %q, want %q", tc.cur, tc.toks, got, tc.want)
			}
		})
	}
}

// TestResolveEnvChdirsTable pins -C resolution: absolute replaces,
// relative joins onto the running directory (or chains), unresolvable
// operands fail closed, and Ws variables expand like cd operands.
func TestResolveEnvChdirsTable(t *testing.T) {
	lookup := func(k string) (string, bool) {
		if k == "HK_OK" {
			return "/ok", true
		}
		return "", false
	}
	cases := []struct {
		name   string
		cur    string
		dirs   []string
		want   []string
		wantOk bool
	}{
		{name: "absolute", cur: "/base", dirs: []string{"/hook"}, want: []string{"/hook"}, wantOk: true},
		{name: "relative joins", cur: "/base", dirs: []string{"sub"}, want: []string{"/base/sub"}, wantOk: true},
		{name: "chain", cur: "/base", dirs: []string{"/a", "sub"}, want: []string{"/a", "/a/sub"}, wantOk: true},
		{name: "var expands", cur: "/base", dirs: []string{"$HK_OK"}, want: []string{"/ok"}, wantOk: true},
		{name: "unknown var fails closed", cur: "/base", dirs: []string{"$HK_MISSING"}, wantOk: false},
		{name: "empty fails closed", cur: "/base", dirs: []string{""}, wantOk: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveEnvChdirs(tc.cur, tc.dirs, lookup)
			if ok != tc.wantOk {
				t.Fatalf("resolveEnvChdirs(%q, %q) ok = %v, want %v (got %q)", tc.cur, tc.dirs, ok, tc.wantOk, got)
			}
			if ok && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("resolveEnvChdirs(%q, %q) = %q, want %q", tc.cur, tc.dirs, got, tc.want)
			}
		})
	}
}

// TestSkipCdPrefixesTable pins wrapper/assign skipping: VAR=, command,
// builtin, and exec prefixes are skipped to the cd operand, while other
// wrappers (sudo, time) are left in place so the beyond-first net fails
// them closed.
func TestSkipCdPrefixesTable(t *testing.T) {
	cases := []struct {
		name string
		toks []string
		want []string
	}{
		{name: "bare cd", toks: []string{"cd", "/hook"}, want: []string{"cd", "/hook"}},
		{name: "assign prefix", toks: []string{"VAR=1", "cd", "/hook"}, want: []string{"cd", "/hook"}},
		{name: "command prefix", toks: []string{"command", "cd", "/hook"}, want: []string{"cd", "/hook"}},
		{name: "builtin prefix", toks: []string{"builtin", "cd", "/hook"}, want: []string{"cd", "/hook"}},
		{name: "exec prefix", toks: []string{"exec", "cd", "/hook"}, want: []string{"cd", "/hook"}},
		{name: "assign then wrapper", toks: []string{"VAR=1", "command", "cd", "/hook"}, want: []string{"cd", "/hook"}},
		{name: "sudo stays", toks: []string{"sudo", "cd", "/hook"}, want: []string{"sudo", "cd", "/hook"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipCdPrefixes(tc.toks); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("skipCdPrefixes(%q) = %q, want %q", tc.toks, got, tc.want)
			}
		})
	}

	beyond := []struct {
		name string
		eff  []string
		want bool
	}{
		{name: "sudo cd beyond first", eff: []string{"sudo", "cd", "/hook"}, want: true},
		{name: "bare cd first", eff: []string{"cd", "/hook"}, want: false},
		{name: "path operand ignored", eff: []string{"/bin/cd", "/hook"}, want: false},
	}
	for _, tc := range beyond {
		t.Run(tc.name, func(t *testing.T) {
			if got := cdTokenBeyondFirst(tc.eff); got != tc.want {
				t.Errorf("cdTokenBeyondFirst(%q) = %v, want %v", tc.eff, got, tc.want)
			}
		})
	}
}

// TestEnvChainCoveredTable pins the skip span in input coordinates. It
// delegates to spliceEnvLayerDepth, so a -S value's spliced words add no
// coverage: a chain that ends at a spliced wrapper covers only the flag and
// its value, and the following input tokens (which may sit behind that
// wrapper) stay scannable. This is the regression fix: the old mirror
// followed the following input tokens as if they were spliced options.
func TestEnvChainCoveredTable(t *testing.T) {
	cases := []struct {
		name string
		rest []string
		want int
	}{
		{name: "plain utility", rest: []string{"ls", "-la"}, want: 0},
		{name: "chdir operand", rest: []string{"-C", "/hook", "ls"}, want: 2},
		{name: "attached chdir", rest: []string{"-C/hook", "ls"}, want: 1},
		{name: "nested env chdir", rest: []string{"env", "-C", "/hook", "git"}, want: 3},
		{name: "dashes nested env", rest: []string{"--", "env", "-C", "/hook", "ls"}, want: 4},
		{name: "split wrapper stops at value", rest: []string{"-S", "sudo", "env", "-C", "/hook", "git", "status"}, want: 2},
		{name: "split nested env continues into tail", rest: []string{"-S", "env", "-C", "/hook", "git"}, want: 4},
		{name: "split chdir value takes tail operand", rest: []string{"-S", "-C", "/hook", "git"}, want: 3},
		{name: "unknown fails closed to zero", rest: []string{"--zznope", "ls"}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envChainCovered(tc.rest); got != tc.want {
				t.Errorf("envChainCovered(%q) = %d, want %d", tc.rest, got, tc.want)
			}
		})
	}
}

// TestEnvSplitWrapperInterleavingAsk pins the coveredUntil regression end to
// end: `env -S '<wrapper>' env -C <dir> git status` runs the trailing env
// behind the spliced wrapper, so the -C steers git and the pre-gate must
// Ask (never silently allow). The shell and explicit-argv forms behave the
// same; the plain -C control pins the expected reason.
func TestEnvSplitWrapperInterleavingAsk(t *testing.T) {
	workspace := t.TempDir()
	hook := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{workspace},
		ReadRoots:      []string{workspace},
	}
	cases := []struct {
		name string
		cmd  string
		args []string
	}{
		{name: "control chdir", cmd: "env -C " + hook + " git status"},
		{name: "shell wrapper", cmd: "env -S 'sudo' env -C " + hook + " git status"},
		{name: "explicit wrapper", cmd: "env", args: []string{"-S", "sudo", "env", "-C", hook, "git", "status"}},
		{name: "shell wrapper interleaved", cmd: "env -S 'sudo env' env -C " + hook + " git status"},
		{name: "explicit wrapper interleaved", cmd: "env", args: []string{"-S", "sudo env", "env", "-C", hook, "git", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deny, ask := auditGitPreGate(tc.cmd, tc.args, workspace, nil, &execOptions{}, sb)
			if deny != "" {
				t.Fatalf("auditGitPreGate(%q, %q) deny = %q, want none", tc.cmd, tc.args, deny)
			}
			if !strings.Contains(ask, "env -C directory override requires user approval") {
				t.Errorf("auditGitPreGate(%q, %q) ask = %q, want env -C override reason", tc.cmd, tc.args, ask)
			}
		})
	}
}

// TestEnvTrampolineAndLongOptionAccessors pins the frozen read APIs: the
// prefix table is readable only through IsEnvTrampolinePrefix, and
// EnvLongOptionNames returns a copy whose mutation cannot change either
// layer's resolution.
func TestEnvTrampolineAndLongOptionAccessors(t *testing.T) {
	for _, base := range []string{"time", "sudo", "env", "xargs"} {
		if !IsEnvTrampolinePrefix(base) {
			t.Errorf("IsEnvTrampolinePrefix(%q) = false, want true", base)
		}
	}
	for _, base := range []string{"ls", "make", "tar", ""} {
		if IsEnvTrampolinePrefix(base) {
			t.Errorf("IsEnvTrampolinePrefix(%q) = true, want false", base)
		}
	}
	names := EnvLongOptionNames()
	if len(names) == 0 {
		t.Fatal("EnvLongOptionNames() = empty")
	}
	names[0] = "zznope"
	if got := EnvLongOptionKind("ignore-environment"); got == "" {
		t.Errorf("mutating EnvLongOptionNames() result changed the canonical resolver")
	}
	if got := resolveEnvLongName("ignore-environment"); got == envLongUnknown {
		t.Errorf("mutating EnvLongOptionNames() result changed resolveEnvLongName")
	}
}

// TestSettledWordTrailingEnvChdirAsk pins F-C end to end at the pre-gate:
// the chdir Ask fires for a trailing env -C after a settled word (and after
// the newly added time delegator), in shell and explicit-argv forms.
func TestSettledWordTrailingEnvChdirAsk(t *testing.T) {
	workspace := t.TempDir()
	hook := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{workspace},
		ReadRoots:      []string{workspace},
	}
	cases := []struct {
		name string
		cmd  string
		args []string
	}{
		{name: "settled word trailing env", cmd: "env ls env -C " + hook + " git status"},
		{name: "time delegator nested env", cmd: "env time env -C " + hook + " git status"},
		{name: "explicit settled word trailing env", cmd: "env", args: []string{"ls", "env", "-C", hook, "git", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deny, ask := auditGitPreGate(tc.cmd, tc.args, workspace, nil, &execOptions{}, sb)
			if deny != "" {
				t.Fatalf("auditGitPreGate(%q, %q) deny = %q, want none", tc.cmd, tc.args, deny)
			}
			if !strings.Contains(ask, "env -C directory override requires user approval") {
				t.Errorf("auditGitPreGate(%q, %q) ask = %q, want env -C override reason", tc.cmd, tc.args, ask)
			}
		})
	}
}

// TestJoinGitAskWithDirTable pins reason rendering: keys sort, the env
// -C override appends deterministically, and empty stays empty.
func TestJoinGitAskWithDirTable(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		dir  bool
		want string
	}{
		{name: "empty", keys: nil, dir: false, want: ""},
		{name: "keys sort", keys: []string{"GIT_B", "GIT_A"}, dir: false, want: "git environment overrides require user approval: GIT_A, GIT_B"},
		{name: "dir only", keys: nil, dir: true, want: "env -C directory override requires user approval"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := joinGitAskWithDir(tc.keys, tc.dir)
			if tc.dir && len(tc.keys) > 0 {
				if !strings.Contains(got, "env -C directory override") || !strings.Contains(got, "GIT_A") {
					t.Errorf("joinGitAskWithDir(%q, %v) = %q, want keys plus dir override", tc.keys, tc.dir, got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("joinGitAskWithDir(%q, %v) = %q, want %q", tc.keys, tc.dir, got, tc.want)
			}
		})
	}
}
