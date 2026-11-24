package agent

// Fail-closed floor pins: execution-capable flags deny even under a
// blanket allow (hard-deny authoritative, never widened), approval-
// clearable shapes (ext:: operands, config/env influence, unresolvable
// expansions, interpreter code programs, device-adjacent chmod) ask
// exactly so approval can clear a benign case, and quoted-echo greens
// stay out. Backslash-escaped (`\git`) and ANSI-C ($'...') spellings pin
// the same verdicts as their plain forms.

import (
	"os"
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
)

// TestGateFailClosedBackslashGit pins backslash-escaped git/env spellings
// seeing through the escape: `\git -c core.pager=id status` denies like
// plain git, `\env -S 'GIT_PAGER=x git status'` denies like env, and
// clean `\git status` stays allowed exactly like `git status`.
func TestGateFailClosedBackslashGit(t *testing.T) {
	allow := round2AllowSB()
	for _, cmd := range []string{
		`\git -c core.pager=id status`,
		`\git --config-env core.pager=EVIL status`,
		`\env GIT_PAGER=x git status`,
		`\env -S 'GIT_PAGER=x git status'`,
		`sudo \git -c core.pager=id status`,
	} {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want deny (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
	for _, cmd := range []string{
		`\git status`,
		`\env git status`,
		`echo "\git -c core.pager=id status"`,
	} {
		t.Run("allow "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAllow {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want allow (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	// Explicit argv form sees through the escape the same way: a
	// backslash-escaped binary still denies on exec-hook influence and
	// stays allowed when clean.
	for _, argv := range [][]string{
		{`\git`, "-c", "core.pager=id", "status"},
		{`\env`, "GIT_PAGER=x", "git", "status"},
	} {
		t.Run("deny explicit "+strings.Join(argv, " "), func(t *testing.T) {
			if d := EvaluateCommand(allow, argv[0], argv[1:]); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want deny (%s)", argv, d.Action, d.Reason)
			}
		})
	}
	if d := EvaluateCommand(allow, `\git`, []string{"status"}); d.Action != ActionAllow {
		t.Errorf("explicit \\git status under allow = %s, want allow (%s)", d.Action, d.Reason)
	}
}

// TestGateFailClosedExecFlagBeatsAllow pins plain (non-ANSI-C) sort/tar
// exec flags denying even under a blanket policy allow (hard-deny
// authoritative). ANSI-C $'...' / $"..." forms are pinned by
// TestGateFailClosedANSICExecFlagBeatsAllow below (G1 ACE-path fix).
func TestGateFailClosedExecFlagBeatsAllow(t *testing.T) {
	installTestPolicy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAllow},
	}})
	for _, cmd := range []string{
		`sort --co=gzip file`,
		`sort --compress-program=gzip file`,
		`sudo sort --co=gzip file`,
		`tar -xf archive.tar --checkpoint-action=exec=id`,
		`sudo tar -xf archive.tar --checkpoint-action=exec=id`,
		`find . -exec sort --co=gzip {} \;`,
	} {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(nil, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) = %s, want deny (hard-deny beats allow, reason=%q)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// TestGateFailClosedQuotedSortGreen pins quoted sort-flag text staying
// green (data, not execution). Unquoted `echo sort --co=gzip` currently
// denies via the quote-blind string leg (safe-direction false positive)
// and is NOT pinned green: changing it needs gate.go work.
func TestGateFailClosedQuotedSortGreen(t *testing.T) {
	for _, cmd := range []string{
		`echo "sort --co=gzip"`,
		`echo 'sort --co=gzip'`,
		`echo "sort --compress-program=gzip"`,
		`grep -r "sort --co" .`,
	} {
		t.Run("green "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(nil, cmd, nil); d.Action == ActionDeny {
				t.Errorf("EvaluateCommand(%q) = deny, want not-deny (reason=%q)", cmd, d.Reason)
			}
		})
	}
}

// TestGateFailClosedANSICExecFlagDeny pins the G1 ACE-path: bash ANSI-C
// $'...' and locale $"..." forms of tar/sort execution-capable flags deny
// exactly like the plain forms (argv $'...' misses the --co prefix and the
// string leg strips '...' as quoted). Covers direct, piped, sudo, find
// -exec, and escape-encoded ($'\x3d' for '=') shapes.
func TestGateFailClosedANSICExecFlagDeny(t *testing.T) {
	for _, cmd := range []string{
		`sort $'--co=gzip' file`,
		`sort $'--compress-program=gzip' file`,
		`sort $"--co=gzip" file`,
		`sort $"--compress-program=gzip" file`,
		`sudo sort $'--co=gzip' file`,
		`sudo sort $"--co=gzip" file`,
		`echo hi | sort $'--co=gzip'`,
		`echo hi | sort $"--co=gzip"`,
		`find . -exec sort $'--co=gzip' {} \;`,
		`sort $'--co\x3dgzip' file`,
		`tar -xf archive.tar $'--checkpoint-action=exec=id'`,
		`tar -xf archive.tar $"--checkpoint-action=exec=id"`,
		`tar -xf archive.tar $'--checkpoint-a=exec=id'`,
		`sudo tar -xf archive.tar $'--checkpoint-action=exec=id'`,
		`sudo tar -xf archive.tar $"--checkpoint-action=exec=id"`,
		`printf x | tar -xf archive.tar $'--checkpoint-action=exec=id'`,
		`find . -exec tar -xf archive.tar $'--checkpoint-action=exec=id' {} \;`,
	} {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(nil, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) = %s, want deny (reason=%q)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// TestGateFailClosedANSICExecFlagBeatsAllow pins ANSI-C exec flags staying
// deny under a blanket policy allow (hard-deny authoritative, never
// widened to allow).
func TestGateFailClosedANSICExecFlagBeatsAllow(t *testing.T) {
	installTestPolicy(t, permissions.Policy{Rules: []permissions.Rule{
		{Action: "shell", Resource: "*", Effect: permissions.EffectAllow},
	}})
	for _, cmd := range []string{
		`sort $'--co=gzip' file`,
		`sort $"--co=gzip" file`,
		`sudo sort $'--co=gzip' file`,
		`echo hi | sort $'--co=gzip'`,
		`tar -xf archive.tar $'--checkpoint-action=exec=id'`,
		`tar -xf archive.tar $"--checkpoint-action=exec=id"`,
		`sudo tar -xf archive.tar $'--checkpoint-action=exec=id'`,
		`sudo env -S $'sort --co=gzip'`,
		`sudo env -S $'rm -rf /'`,
		`sudo env --split-string=$'rm -rf /'`,
	} {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(nil, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) = %s, want deny (hard-deny beats allow, reason=%q)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// TestGateFailClosedANSICUnbalancedFailClosed pins unbalanced $' / $"
// openers failing closed (never Allow). Balanced decoding misses are Deny
// above; unbalanced input leaves the flag text visible (Deny) or fails
// shlex parsing (Ask) - either is fail-closed.
func TestGateFailClosedANSICUnbalancedFailClosed(t *testing.T) {
	for _, cmd := range []string{
		`sort $'--co=gzip file`,
		`tar -xf archive.tar $'--checkpoint-action=exec=id`,
		`sort $"--co=gzip file`,
	} {
		t.Run("failclosed "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(nil, cmd, nil); d.Action == ActionAllow {
				t.Errorf("EvaluateCommand(%q) = allow, want deny or ask (fail-closed, reason=%q)", cmd, d.Reason)
			}
		})
	}
}

// TestGateFailClosedANSICPreserve pins no-regression greens and carriers
// alongside the ANSI-C fix: quoted echo "rm -rf /" stays green, su -c and
// find -exec carriers still deny, bundled clusters and -- separators keep
// working.
func TestGateFailClosedANSICPreserve(t *testing.T) {
	for _, cmd := range []string{
		`echo "rm -rf /"`,
		`echo "sort --co=gzip"`,
		`echo "tar --checkpoint-action=exec=id"`,
	} {
		t.Run("green "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(nil, cmd, nil); d.Action == ActionDeny {
				t.Errorf("EvaluateCommand(%q) = deny, want not-deny (reason=%q)", cmd, d.Reason)
			}
		})
	}
	for _, cmd := range []string{
		`su -c 'rm -rf /'`,
		`find . -exec rm -rf / {} \;`,
		`bash -c -- 'rm -rf /'`,
		`bash -ec 'rm -rf /'`,
	} {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(nil, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) = %s, want deny (reason=%q)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// TestGateFailClosedGitExtOperand pins the HIGH-2 ext:: Ask leg: a bare
// ext:: transport operand past the subcommand (clone, fetch, ls-remote,
// remote set-url/add) asks even under allow - git executes the remainder
// as a command. Benign transports, local paths, and quoted-echo text stay
// out; the explicit argv form behaves the same.
func TestGateFailClosedGitExtOperand(t *testing.T) {
	allow := round2AllowSB()
	ask := []string{
		`git clone ext::sh -c id`,
		`git clone 'ext::sh -c id'`,
		`git clone "ext::ssh host repo"`,
		`git clone EXT::id`,
		`git clone -- ext::id`,
		`git fetch ext::sh -c id`,
		`git ls-remote ext::sh -c id`,
		`git remote add origin ext::id`,
		`git remote set-url origin ext::id`,
		`sudo git clone ext::id`,
		`env PATH=/bin git clone ext::id`,
	}
	for _, cmd := range ask {
		t.Run("ask "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want ask (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	// Benign transports, local paths, and plain subcommands stay out of
	// the ext:: leg.
	for _, cmd := range []string{
		`git clone https://example.com/r.git`,
		`git clone /tmp/local/path`,
		`git fetch origin`,
		`git remote add origin https://example.com/r.git`,
		`git status`,
		`git branch -c old new`,
	} {
		t.Run("allow "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAllow {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want allow (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	// Quoted ext:: text stays green.
	if d := EvaluateCommand(allow, `echo "git clone ext::x"`, nil); d.Action == ActionDeny {
		t.Errorf("quoted ext:: echo should not deny, got deny (%s)", d.Reason)
	}

	// Explicit argv form behaves the same.
	if d := EvaluateCommand(allow, "git", []string{"clone", "ext::sh -c id"}); d.Action != ActionAsk {
		t.Errorf("explicit git clone ext:: = %s, want ask (%s)", d.Action, d.Reason)
	}
	if d := EvaluateCommand(allow, "git", []string{"clone", "https://example.com/r.git"}); d.Action != ActionAllow {
		t.Errorf("explicit git clone https = %s, want allow (%s)", d.Action, d.Reason)
	}
}

// round2AllowSB is a system_exec=allow sandbox: the pins below assert the
// fail-closed floor (Deny/Ask, never Allow) where the risk table alone
// would allow.
func round2AllowSB() *sandbox.SandboxConfig {
	return &sandbox.SandboxConfig{
		Mode:        sandbox.SandboxModePaths,
		Permissions: map[string]string{"system_exec": "allow"},
	}
}

// TestGateFailClosedEnvAbbrev pins getopt_long abbreviation resolution
// for env --split-string (--s/--sp/--spl... splice like --split-string,
// with =val or a separate value) and mid-cluster -S (-iS takes the next
// token, -iSVALUE takes the attached remainder). Unknown/ambiguous longs
// fail closed to Ask, never Allow.
func TestGateFailClosedEnvAbbrev(t *testing.T) {
	sb := &sandbox.SandboxConfig{
		Mode:        sandbox.SandboxModePaths,
		Permissions: map[string]string{"system_exec": "ask"},
	}
	deny := []struct {
		name string
		cmd  string
		args []string
	}{
		{name: "abbrev --s shell", cmd: `env --s "rm -rf /"`},
		{name: "abbrev --sp shell", cmd: `env --sp "rm -rf /"`},
		{name: "abbrev --spl shell", cmd: `env --spl "rm -rf /"`},
		{name: "abbrev --split shell", cmd: `env --split "rm -rf /"`},
		{name: "abbrev --s= shell", cmd: `env --s="rm -rf /"`},
		{name: "abbrev --sp= shell", cmd: `env --sp="rm -rf /"`},
		{name: "mid-cluster -iS shell", cmd: `env -iS "rm -rf /"`},
		{name: "mid-cluster -viS shell", cmd: `env -viS "rm -rf /"`},
		{name: "attached mid-cluster shell", cmd: `env -iS"rm -rf /"`},
		{name: "sudo abbrev shell", cmd: `sudo env --s "rm -rf /"`},
		{name: "sudo mid-cluster shell", cmd: `sudo env -iS "rm -rf /"`},
		{name: "abbrev explicit", cmd: "env", args: ss("--s", "rm -rf /")},
		{name: "mid-cluster explicit", cmd: "env", args: ss("-iS", "rm -rf /")},
		{name: "value-taking abbrev still finds utility", cmd: "env", args: ss("--u", "FOO", "rm", "-rf", "/")},
	}
	for _, tt := range deny {
		t.Run("deny "+tt.name, func(t *testing.T) {
			if dec := EvaluateCommand(sb, tt.cmd, tt.args); dec.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q, %v) = %s, want deny (%s)", tt.cmd, tt.args, dec.Action, dec.Reason)
			}
		})
	}

	// Unknown/ambiguous longs fail closed to Ask, never Allow.
	for _, cmd := range []string{`env --zznope ls`, `env --d ls`} {
		t.Run("ask "+cmd, func(t *testing.T) {
			if dec := EvaluateCommand(round2AllowSB(), cmd, nil); dec.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) = %s, want ask (%s)", cmd, dec.Action, dec.Reason)
			}
		})
	}

	// Benign abbreviated value-takers still resolve (no over-ask).
	if dec := EvaluateCommand(sb, "env", ss("--ch", "/tmp", "ls")); dec.Action == ActionDeny {
		t.Errorf("env --ch /tmp ls: want not-deny, got deny (%s)", dec.Reason)
	}
}

// TestGateFailClosedTarExecFlags pins tar execution-capable flags denying
// even under allow: --use-compress-program (-I), --to-command,
// --info-script, --new-volume-script, in full and abbreviated, plus short
// -I (separate, attached, bundled). The bare --checkpoint=N counter stays
// out.
func TestGateFailClosedTarExecFlags(t *testing.T) {
	allow := round2AllowSB()
	deny := []string{
		`tar -xf archive.tar -I prog`,
		`tar -xf archive.tar -Iprog`,
		`tar -xIf prog archive.tar`,
		`tar -xf archive.tar --use-compress-program=prog`,
		`tar -xf archive.tar --use-compr=prog`,
		`tar -xf archive.tar --to-command=prog`,
		`tar -xf archive.tar --to-c=prog`,
		`tar -xf archive.tar --info-script=prog`,
		`tar -xf archive.tar --info=prog`,
		`tar -xf archive.tar --new-volume-script=prog`,
		`tar -xf archive.tar --new-v=prog`,
		`tar -xf archive.tar --checkpoint-a=exec=id`,
		`sudo tar -xf archive.tar --use-compress-program=prog`,
		`sudo tar -xf archive.tar -I prog`,
		`printf x | tar -xf archive.tar --to-command=prog`,
		`find . -exec tar -xf archive.tar --to-command=prog {} \;`,
		// GNU tar old option style: the dashless first word is the flag
		// cluster, so these are the -I/-F forms without the dash.
		`tar cI prog -f archive.tar`,
		`tar cF script -f archive.tar`,
		`tar xI prog -f archive.tar`,
		`sudo tar cI prog -f archive.tar`,
		`printf x | tar cI prog -f archive.tar`,
	}
	for _, cmd := range deny {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want deny (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	// Bare counter, plain list, and directory/archive names that merely
	// contain I/F stay out of hard-deny. -C still Asks via the directory
	// override leg; the point is that it is not mistaken for -I/-F. The
	// dashless old-style forms with f as the value-taker stay out too, and
	// a later dashless word is an operand (old style is first-word only).
	for _, cmd := range []string{
		`tar --checkpoint=1 -xf archive.tar`,
		`tar -tf archive.tar`,
		`tar -C/home/Foo -xf archive.tar`,
		`tar -xf archive.tar -C/Flutter`,
		`tar -xf archive.tar -C/opt/I`,
		`tar -xvf archive.tar`,
		`tar cf archive.tar file`,
		`tar cvf archive.tar file`,
		`tar tvf archive.tar`,
		`tar xf archive.tar`,
		`tar -f archive.tar cI`,
	} {
		t.Run("nodeny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action == ActionDeny {
				t.Errorf("EvaluateCommand(%q) under allow = deny, want not-deny (%s)", cmd, d.Reason)
			}
		})
	}

	// The old-style dashless directory flag still Asks (never Allowed): it
	// steers the extraction directory exactly like -C.
	for _, cmd := range []string{
		`tar cC /tmp -xf archive.tar`,
		`sudo tar cC /tmp -xf archive.tar`,
	} {
		t.Run("ask "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want ask (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	// Quoted text stays green.
	for _, cmd := range []string{
		`echo "tar -I prog"`,
		`echo "tar --to-command=prog"`,
	} {
		t.Run("green "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action == ActionDeny {
				t.Errorf("EvaluateCommand(%q) = deny, want not-deny (%s)", cmd, d.Reason)
			}
		})
	}

	// The deny reason names both value-taking exec flags: a -F info-script
	// run must not be reported as -I only.
	for _, cmd := range []string{`tar -F script -f archive.tar`, `tar cF script -f archive.tar`} {
		d := EvaluateCommand(allow, cmd, nil)
		if d.Action != ActionDeny || !strings.Contains(d.Reason, "-I/-F") {
			t.Errorf("EvaluateCommand(%q) = %s (%q), want deny naming -I/-F", cmd, d.Action, d.Reason)
		}
	}
}

// TestGateFailClosedGitConfigEnv pins git config/env influence failing
// closed (never Allow low): exec -c keys and exec-hook env overlays deny;
// benign -c keys, directory overrides, and other GIT_* overlays ask; plain
// git status and subcommand -c (branch copy) stay out.
func TestGateFailClosedGitConfigEnv(t *testing.T) {
	allow := round2AllowSB()
	deny := []string{
		`git -c core.pager=id status`,
		`git -c diff.external=prog status`,
		`git -c alias.x=!evil status`,
		`git -c alias.x='"!evil"' status`,
		`git --config-env core.pager=EVIL status`,
		`sudo git -c core.pager=id status`,
		`env GIT_EXTERNAL_DIFF=x git status`,
		`env GIT_PAGER=x git status`,
		`env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.pager GIT_CONFIG_VALUE_0=id git status`,
		`env GIT_CONFIG_PARAMETERS='core.pager=id' git status`,
		`env GIT_EDITOR=evil git status`,
		`env -S 'GIT_EXTERNAL_DIFF=id git status'`,
		`env --split-string='GIT_PAGER=x git status'`,
		`env -S 'GIT_CONFIG_PARAMETERS=core.pager=id git status'`,
		`sudo env -S 'GIT_PAGER=x git status'`,
		// Unresolvable value in a known exec-capable key denies: the
		// key is known to execute, so an unknown value cannot be
		// cleared by approval the way an unknown-capability shape
		// (user.name, GIT_TRACE) can.
		`git -c core.pager=${HK_GIT_UNSET_VAR} status`,
	}
	for _, cmd := range deny {
		t.Run("deny "+cmd, func(t *testing.T) {
			os.Unsetenv("HK_GIT_UNSET_VAR")
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want deny (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	ask := []string{
		`git -c user.name=x status`,
		`git -c alias.st=status status`,
		`git -C /tmp status`,
		`git --git-dir=/tmp/x status`,
		`env GIT_TRACE=1 git status`,
		`env GIT_DIR=/tmp/x git status`,
		`env -S 'GIT_TRACE=1 git status'`,
	}
	for _, cmd := range ask {
		t.Run("ask "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want ask (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	// Plain status stays allowed; a subcommand's own -c (branch copy) is
	// not a config override. The engine's own non-interactive
	// GIT_EDITOR=true sentinel stays allowed too.
	for _, cmd := range []string{
		`git status`,
		`git branch -c old new`,
		`env GIT_EDITOR=true git status`,
		`env GIT_TERMINAL_PROMPT=0 git status`,
		`echo "git -c core.pager=id status"`,
	} {
		t.Run("allow "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAllow {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want allow (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// TestGateFailClosedParamExpansion pins ${} handling: known values deny
// via expansion (using the ambient env map), unresolvable expansions in
// execution-capable flags or destructive targets ask, never Allow low.
// Plain quoted text stays green.
func TestGateFailClosedParamExpansion(t *testing.T) {
	allow := round2AllowSB()
	t.Setenv("HK_GATE_T_H", "o")
	t.Setenv("HK_GATE_T_X", "/")
	t.Setenv("HK_GATE_T_OK", "/tmp/hk-ok")

	deny := []string{
		`sort --c${HK_GATE_T_H}ompress file`,
		`rm -rf ${HK_GATE_T_X}`,
		`rm -rf $HK_GATE_T_X`,
		`tar ${1:-cI} prog -f a.tar`,
		// A leading word the gate cannot resolve does not hide the tar
		// behind it: once expanded, the dashless cluster is tar's option
		// stream, so the old-style hard-deny leg still reads it in
		// command position. Wrapper prefixes stay transparent too.
		`sudo -s '$CMD tar ${1:-cI}'`,
		`su -c '$CMD tar ${1:-cI}'`,
		`timeout 5 sudo -s '$CMD tar ${1:-cI}'`,
		`env sudo -s '$CMD tar ${1:-cI}'`,
		`FOO=bar sudo -s '$CMD tar ${1:-cI}'`,
		`nice sudo -s '$CMD tar ${1:-cI}'`,
	}
	for _, cmd := range deny {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want deny (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	ask := []string{
		`rm -rf ${HK_GATE_T_OK}`,
		`rm -rf $HK_GATE_T_OK`,
		`rm -rf $HK_GATE_T_UNSET`,
		`rm -rf $1`,
		`rm -rf $9`,
		`rm -rf $0`,
		`rm -rf $@`,
		`rm -rf $*`,
		`rm -rf $$`,
		`rm -rf $?`,
		`rm -rf $#`,
		`rm -rf $!`,
		`rm -rf $-`,
		`tar $1`,
		`tar $@`,
		`tar $!`,
		`tar -xf archive.tar --to-${HK_GATE_T_UNSET}md prog`,
		`dd if=x of=${HK_GATE_T_OK}`,
		`dd if=x of=$HK_GATE_T_OK`,
		`dd if=x of=$1`,
		`dd if=x of=$@`,
		`sort --c$HK_GATE_T_UNSET file`,
		`chmod -R 777 ${HK_GATE_T_OK}`,
		`chmod -R 777 $HK_GATE_T_OK`,
		`chmod -R 777 ${}`,
		`chown -R root $HK_GATE_T_OK`,
		// tar/make-family tails fail closed in command position: an
		// unresolvable expansion can name an option, target, or
		// steering assignment, and gtar/bsdtar/gmake alias the same
		// utilities.
		`make $1`,
		`make ${HK_GATE_T_UNSET}`,
		`gmake $@`,
		`make -C /tmp $1`,
		`make all ${HK_GATE_T_UNSET}`,
		`gtar $1`,
		`bsdtar ${HK_GATE_T_UNSET}`,
		`sudo gtar ${HK_GATE_T_UNSET}`,
		`gtar -xf ${HK_GATE_T_UNSET}`,
		`tar -t $1`,
		`tar -xf a.tar $1`,
		`tar -xvf $@`,
		// Packed shell-carrier payloads re-lex with the same
		// command-position semantics: the outer token is one quoted
		// operand, so the tar/make tail Ask must be evaluated on the
		// re-lexed payload, not the outer argv alone.
		`sudo -s 'tar $1'`,
		`su -c 'tar $1'`,
		`sh -c 'tar $1'`,
		`sudo -s 'make $1'`,
		`su -c 'make $1'`,
		`sudo -i 'tar $1'`,
		`sudo -s 'gtar ${HK_GATE_T_UNSET}'`,
		`su -c 'make all ${HK_GATE_T_UNSET}'`,
		// A nested carrier packed into the payload text is followed with
		// the shared depth budget.
		`su -c "sudo -s 'tar \$1'"`,
		// A leading word the gate cannot resolve may be the command
		// itself, so the payload fails closed to Ask instead of letting
		// the unresolvable lead hide what follows.
		`sudo -s '$CMD $1'`,
		`su -c '$CMD $1'`,
		`sudo -s '$CMD chown'`,
		`su -c '$CMD chown'`,
		`sudo -s '$CMD make $1'`,
		// Wrapper prefixes stay transparent: the carrier behind
		// timeout/env/FOO=/nice is still reachable and asks.
		`timeout 5 sudo -s 'tar $1'`,
		`env sudo -s 'tar $1'`,
		`FOO=bar sudo -s 'tar $1'`,
		`nice sudo -s 'tar $1'`,
	}
	for _, cmd := range ask {
		t.Run("ask "+cmd, func(t *testing.T) {
			os.Unsetenv("HK_GATE_T_UNSET")
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want ask (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	if d := EvaluateCommand(allow, `echo "${HK_GATE_T_X}"`, nil); d.Action != ActionAllow {
		t.Errorf("quoted echo expansion should stay allowed, got %s (%s)", d.Action, d.Reason)
	}
	// A prose mention of a tar/make utility is data, not the utility in
	// command position, so the fail-closed tail walk stays out.
	for _, cmd := range []string{
		`echo tar ${HK_GATE_T_UNSET}`,
		`echo make ${HK_GATE_T_UNSET}`,
		// An unreachable carrier is an argument, not a shell: the packed
		// payload stays data.
		`echo sudo -s 'tar $1'`,
		`echo su -c 'make $1'`,
		// Inside a real payload, prose stays prose: command position is
		// resolved per re-lexed segment.
		`sudo -s 'echo tar ${HK_GATE_T_UNSET}'`,
		`su -c 'echo make ${HK_GATE_T_UNSET}'`,
		`sudo -s 'echo hi'`,
	} {
		t.Run("allow prose "+cmd, func(t *testing.T) {
			os.Unsetenv("HK_GATE_T_UNSET")
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAllow {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want allow (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// TestGateFailClosedTarDashlessParamExpansion pins GNU tar's old option
// style against shell parameter-expansion defaults: the shell substitutes
// the default word before tar runs, so `tar ${UNSET:-cI} prog` is
// `tar -cI prog` and must Deny even under allow. An expansion the gate
// cannot resolve anywhere in TAR_OPTIONS/MAKEFLAGS is pre-parsed by the
// utility, so it Asks; prose that merely contains a tar-like word stays
// green (the dashless cluster is only read in command position).
func TestGateFailClosedTarDashlessParamExpansion(t *testing.T) {
	allow := round2AllowSB()
	deny := []string{
		`tar ${X:-cI} prog -f a.tar`,
		`tar ${UNSET:-cI} prog -f a.tar`,
		`tar ${X:-cF} prog -f a.tar`,
		`tar ${X:-xI} prog -f a.tar`,
		// A dashed default is a flag in its own right: the long option
		// resolves like any argv long instead of being rejected by the
		// dashless short-cluster walk.
		`tar ${1:---to-command=id} -f a.tar`,
		// The same candidate walk applies to TAR_OPTIONS: tar parses the
		// substituted default as pre-options, so -I is an exec flag.
		`TAR_OPTIONS=${1:--I id} tar -xf a.tar`,
		`sudo tar ${X:-cI} prog -f a.tar`,
		`printf x | tar ${X:-cI} prog -f a.tar`,
		`echo $(tar ${X:-cI} prog -f a.tar)`,
		`command tar ${X:-cI} prog -f a.tar`,
		`! tar ${X:-cI} prog -f a.tar`,
		`if true; then tar ${X:-cI} prog -f a.tar; fi`,
	}
	for _, cmd := range deny {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want deny (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	ask := []string{
		`env TAR_OPTIONS='${X}' tar -xf a.tar`,
		`env TAR_OPTIONS="${X:-cI}" tar -xf a.tar`,
		`env TAR_OPTIONS='${X:-C /hook}' tar -xf a.tar`,
		`TAR_OPTIONS=$1 tar -xf a.tar`,
		`TAR_OPTIONS=$@ tar -xf a.tar`,
		`TAR_OPTIONS=$9 tar -xf a.tar`,
		`env MAKEFLAGS='${X}' make`,
		`MAKEFLAGS=$1 make`,
		`tar ${X}`,
	}
	for _, cmd := range ask {
		t.Run("ask "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want ask (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	green := []string{
		`echo tar cI`,
		`echo tar cF prog`,
		`echo command tar cI`,
		`echo tar ${X}`,
		`echo "tar cI prog"`,
		`tar cf a.tar file`,
		`tar -f a.tar cI`,
	}
	for _, cmd := range green {
		t.Run("allow "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAllow {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want allow (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// TestParamExpansionAskReasonTable pins the Ask leg's carrier positions
// directly (no caller currently exercises it in isolation): tar/sort flag
// tokens, GNU tar's dashless first word, dd's of=, and every rm operand
// Ask, while unrelated utilities and tar operands stay out.
func TestParamExpansionAskReasonTable(t *testing.T) {
	cases := []struct {
		name    string
		seg     []string
		wantAsk bool
	}{
		{name: "empty", seg: nil},
		{name: "unrelated binary", seg: ss("echo", "${X}")},
		{name: "tar dashed flag", seg: ss("tar", "-f${X}"), wantAsk: true},
		{name: "tar dashless flag position", seg: ss("tar", "${X}"), wantAsk: true},
		{name: "tar bare positional", seg: ss("tar", "$1"), wantAsk: true},
		{name: "sudo tar dashless flag position", seg: ss("sudo", "tar", "${X}"), wantAsk: true},
		{name: "tar argument in prose", seg: ss("echo", "tar", "${X}")},
		{name: "tar following operand", seg: ss("tar", "-xf", "a.tar", "${X}"), wantAsk: true},
		{name: "tar list trailing positional", seg: ss("tar", "-t", "$1"), wantAsk: true},
		{name: "tar bundle trailing at", seg: ss("tar", "-xvf", "$@"), wantAsk: true},
		{name: "gtar bare positional", seg: ss("gtar", "$1"), wantAsk: true},
		{name: "gtar extract expansion", seg: ss("gtar", "-xf", "${X}"), wantAsk: true},
		{name: "bsdtar dashed expansion", seg: ss("bsdtar", "${X}"), wantAsk: true},
		{name: "sudo gtar expansion", seg: ss("sudo", "gtar", "${X}"), wantAsk: true},
		{name: "make bare positional", seg: ss("make", "$1"), wantAsk: true},
		{name: "make bare expansion", seg: ss("make", "${X}"), wantAsk: true},
		{name: "gmake at flag position", seg: ss("gmake", "$@"), wantAsk: true},
		{name: "make directory then positional", seg: ss("make", "-C", "/tmp", "$1"), wantAsk: true},
		{name: "make target then expansion", seg: ss("make", "all", "${X}"), wantAsk: true},
		{name: "make argument in prose", seg: ss("echo", "make", "${X}")},
		{name: "sort flag", seg: ss("sort", "--c${_H}ompress", "file"), wantAsk: true},
		{name: "dd assignment", seg: ss("dd", "if=x", "of=${DEV}"), wantAsk: true},
		{name: "dd bare positional assignment", seg: ss("dd", "if=x", "of=$1"), wantAsk: true},
		{name: "rm target", seg: ss("rm", "-rf", "${X}"), wantAsk: true},
		{name: "rm bare positional target", seg: ss("rm", "-rf", "$1"), wantAsk: true},
		{name: "rm script name target", seg: ss("rm", "-rf", "$0"), wantAsk: true},
		{name: "rm positional nine target", seg: ss("rm", "-rf", "$9"), wantAsk: true},
		{name: "rm at target", seg: ss("rm", "-rf", "$@"), wantAsk: true},
		{name: "rm star target", seg: ss("rm", "-rf", "$*"), wantAsk: true},
		{name: "rm pid target", seg: ss("rm", "-rf", "$$"), wantAsk: true},
		{name: "rm status target", seg: ss("rm", "-rf", "$?"), wantAsk: true},
		{name: "rm argv count target", seg: ss("rm", "-rf", "$#"), wantAsk: true},
		{name: "rm last background pid target", seg: ss("rm", "-rf", "$!"), wantAsk: true},
		{name: "rm shell flags target", seg: ss("rm", "-rf", "$-"), wantAsk: true},
		{name: "tar at flag position", seg: ss("tar", "$@"), wantAsk: true},
		{name: "tar shell flags flag position", seg: ss("tar", "$-"), wantAsk: true},
		{name: "dd star assignment", seg: ss("dd", "if=x", "of=$*"), wantAsk: true},
		{name: "dd last background pid assignment", seg: ss("dd", "if=x", "of=$!"), wantAsk: true},
		{name: "rm env assignment skipped", seg: ss("X=${V}", "rm", "-rf", "${X}"), wantAsk: true},
		{name: "chmod target", seg: ss("chmod", "-R", "777", "${X}"), wantAsk: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := paramExpansionAskReason(tc.seg)
			if (got != "") != tc.wantAsk {
				t.Errorf("paramExpansionAskReason(%q) = %q, wantAsk=%v", tc.seg, got, tc.wantAsk)
			}
		})
	}
}

// TestShellCommandPayloads pins the shared shell -c candidate extraction
// used by both the hard-deny and Ask payload walks: standalone -c skips
// "--", short clusters contribute the attached suffix plus the following
// token (and the o/O-shifted token), value-taking options skip their
// argument, and the walk stops at "--" or the first plain operand. A
// divergence here would let one walk see a payload the other never checks.
func TestShellCommandPayloads(t *testing.T) {
	cases := []struct {
		name string
		rest []string
		want []string
	}{
		{name: "empty", rest: nil},
		{name: "no payload", rest: ss("script.sh", "arg")},
		{name: "standalone", rest: ss("-c", "tar $1"), want: ss("tar $1")},
		{name: "standalone after flags", rest: ss("-e", "-x", "-c", "tar $1"), want: ss("tar $1")},
		{name: "standalone skips separator", rest: ss("-c", "--", "tar $1"), want: ss("tar $1")},
		{name: "cluster attached", rest: ss("-crm -rf /"), want: ss("rm -rf /")},
		{name: "cluster trailing c", rest: ss("-ec", "tar $1"), want: ss("tar $1")},
		{name: "cluster suffix and next", rest: ss("-ce", "tar $1"), want: ss("e", "tar $1")},
		{name: "cluster o shift", rest: ss("-co", "opt", "tar $1"), want: ss("o", "opt", "tar $1")},
		{name: "option value skipped", rest: ss("-o", "extglob", "-c", "tar $1"), want: ss("tar $1")},
		{name: "rcfile value skipped", rest: ss("--rcfile", "/tmp/rc", "-c", "tar $1"), want: ss("tar $1")},
		{name: "rcfile attached skipped", rest: ss("--rcfile=/tmp/rc", "-c", "tar $1"), want: ss("tar $1")},
		{name: "separator before c", rest: ss("--", "-c", "tar $1")},
		{name: "plain operand stops", rest: ss("script.sh", "-c", "tar $1")},
		{name: "empty payload", rest: ss("-c", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shellCommandPayloads(tc.rest)
			if len(got) != len(tc.want) {
				t.Fatalf("shellCommandPayloads(%q) = %q, want %q", tc.rest, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("shellCommandPayloads(%q) = %q, want %q", tc.rest, got, tc.want)
				}
			}
		})
	}
}

// TestCarrierPayloadAskEscalation pins the Ask half of the shell-carrier
// payload walk directly: tar/make-family parameter-expansion tails and the
// other Ask-only leaves fire on a re-lexed payload, prose inside the payload
// stays green (commandPosition is per payload segment), and a nested
// carrier packed into the payload text is followed. The helper must never
// invent an Ask for a plain command.
func TestCarrierPayloadAskEscalation(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantAsk bool
	}{
		{name: "empty", payload: ""},
		{name: "plain command", payload: "echo hi"},
		{name: "tar positional", payload: "tar $1", wantAsk: true},
		{name: "make positional", payload: "make $1", wantAsk: true},
		{name: "tar trailing expansion", payload: "tar -xf a.tar ${X}", wantAsk: true},
		{name: "tar prose in payload", payload: "echo tar ${X}"},
		{name: "make prose in payload", payload: "echo make ${X}"},
		{name: "git chdir", payload: "git -C /hook status", wantAsk: true},
		{name: "make chdir", payload: "make -C /tmp", wantAsk: true},
		{name: "nested sudo carrier", payload: "sudo -s 'tar $1'", wantAsk: true},
		{name: "nested su carrier", payload: "su -c 'make $1'", wantAsk: true},
		{name: "nested shell carrier", payload: "sh -c 'tar $1'", wantAsk: true},
		{name: "nested prose carrier", payload: "sh -c 'echo tar ${X}'"},
		{name: "unreachable carrier", payload: "echo sudo -s 'tar $1'"},
		// A leading word the gate cannot resolve may be the command
		// itself; the effective command fails closed to Ask, and the
		// dashless tar cluster behind it is still read in command
		// position (a known exec-capable form would Deny in hard-deny
		// before this Ask leg runs).
		{name: "leading unresolvable command", payload: "$CMD $1", wantAsk: true},
		{name: "leading unresolvable then chown", payload: "$CMD chown", wantAsk: true},
		{name: "leading unresolvable tar dashless", payload: "$CMD tar ${1:-cI}", wantAsk: true},
		{name: "leading unresolvable benign tail", payload: "$CMD echo hi", wantAsk: true},
		{name: "known command stays out", payload: "chown file"},
		// Wrapper prefixes are transparent to the carrier walk.
		{name: "timeout wrapped carrier", payload: "timeout 5 sudo -s 'tar $1'", wantAsk: true},
		{name: "env wrapped carrier", payload: "env sudo -s 'tar $1'", wantAsk: true},
		{name: "assignment wrapped carrier", payload: "FOO=bar sudo -s 'tar $1'", wantAsk: true},
		{name: "nice wrapped carrier", payload: "nice sudo -s 'tar $1'", wantAsk: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := carrierPayloadAskEscalation(tc.payload, 0)
			if (d != nil) != tc.wantAsk {
				t.Errorf("carrierPayloadAskEscalation(%q) = %+v, wantAsk=%v", tc.payload, d, tc.wantAsk)
			}
		})
	}

	// F4: the shared Ask fold keeps the chmod subpath leg's RiskHigh
	// instead of downgrading it to the generic carrier RiskUnknown.
	if d := carrierPayloadAskEscalation("chmod -R 777 /etc/ssh", 0); d == nil || d.Action != ActionAsk || d.Risk != RiskHigh {
		t.Fatalf("carrier chmod subpath = %+v, want Ask with RiskHigh", d)
	}

	// Depth budget: a chain of nested carriers past maxCarrierPayloadDepth
	// fails closed to Ask rather than recursing forever, with the shared
	// depth reason (the Deny-only walk reports the same string).
	deep := "tar $1"
	for i := 0; i < maxCarrierPayloadDepth+2; i++ {
		deep = "sudo -s '" + strings.ReplaceAll(deep, "'", `'\''`) + "'"
	}
	if d := carrierPayloadAskEscalation(deep, 0); d == nil || d.Action != ActionAsk || !strings.Contains(d.Reason, "nests too deeply to audit") {
		t.Fatalf("deep carrier chain = %+v, want Ask with depth reason", d)
	}
}

// TestParamExpansionMatchers pins the two fail-closed matchers directly.
// hasParamExpansion must see every spelling the shell expands -- bare named,
// positional ($0-$9), special ($@, $*, $#, $?, $$, $!, $-), and braced --
// because a missed spelling silently Allows "rm -rf $1". A lone "$" and a
// dollar followed by an ordinary non-name byte are not expansions.
// paramDefaultWords must return the word ${NAME<op>word} substitutes for the
// same name classes, so GNU tar's dashless cluster audits ${1:-cI}.
func TestParamExpansionMatchers(t *testing.T) {
	expansions := []string{
		"$HOME", "$_x1", "${HOME}", "${HOME:-/tmp}", "pre$HOME", "$HOME/post",
		"$0", "$1", "$9", "$12",
		"$@", "$*", "$#", "$?", "$$", "$!", "$-", "of=$1", "-f$1",
		"${1:-cI}", "${9:-cI}", "${@:-cI}", "${*:--cI}", "${1:=w}", "${1:+w}",
	}
	for _, tok := range expansions {
		if !hasParamExpansion(tok) {
			t.Errorf("hasParamExpansion(%q) = false, want true", tok)
		}
	}
	literals := []string{"", "$", "$ ", "price$", "$%", "$.", "a$", "-f", "100"}
	for _, tok := range literals {
		if hasParamExpansion(tok) {
			t.Errorf("hasParamExpansion(%q) = true, want false", tok)
		}
	}

	defaults := []struct {
		in   string
		want []string
	}{
		{in: "${1:-cI}", want: []string{"cI"}},
		{in: "${9:-cI}", want: []string{"cI"}},
		{in: "${@:-cI}", want: []string{"cI"}},
		{in: "${*:--cI}", want: []string{"-cI"}},
		{in: "${?:-cF prog}", want: []string{"cF prog"}},
		{in: "${UNSET:-cI}", want: []string{"cI"}},
		{in: "${X-cI}", want: []string{"cI"}},
		{in: "${X:=cI}", want: []string{"cI"}},
		{in: "${X:?cI}", want: nil},
		{in: "${X:+cI}", want: []string{"cI"}},
		{in: "${1}", want: nil},
		{in: "$1", want: nil},
		{in: "${X#pat}", want: nil},
		{in: "${X/pat/rep}", want: nil},
		{in: "plain", want: nil},
	}
	for _, tc := range defaults {
		got := paramDefaultWords(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("paramDefaultWords(%q) = %q, want %q", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("paramDefaultWords(%q) = %q, want %q", tc.in, got, tc.want)
				break
			}
		}
	}
}

// TestCandidateFormsAgreement pins the shared candidate walk: the argv
// hard-deny loop, the string window scan, GNU tar's old-style cluster, the
// TAR_OPTIONS/MAKEFLAGS option stream, and the sort/make option legs must
// all reach the same verdict for a token's raw, environment-expanded, and
// parameter-default spellings. A payload hidden behind any of those must
// deny everywhere; a benign operand must stay out everywhere.
func TestCandidateFormsAgreement(t *testing.T) {
	t.Setenv("HK_CAND_X", "--to-command=id")
	t.Setenv("HK_CAND_SORT_X", "--compress-program=id")
	cases := []struct {
		name      string
		tok       string
		want      bool
		optWant   bool
		sortWant  bool
		makeCheck bool
		makeWant  bool
	}{
		{name: "dashless short exec", tok: "cI", want: true},
		{name: "dashless short info script", tok: "cF", want: true},
		{name: "dashless archive operand", tok: "cf", want: false},
		{name: "param short default", tok: "${1:-cI}", want: true},
		{name: "param long default", tok: "${1:---to-command=id}", want: true, optWant: true},
		{name: "param checkpoint default", tok: "${1:---checkpoint-action=exec=id}", want: true, optWant: true},
		{name: "param short with operand", tok: "${1:--I id}", want: true, optWant: true},
		{name: "param benign directory default", tok: "${1:--C /tmp}", want: false},
		{name: "param benign archive default", tok: "${X:-cf}", want: false},
		{name: "env expanded long", tok: "${HK_CAND_X}", want: true, optWant: true},
		{name: "plain operand", tok: "plain", want: false},
		{name: "sort compress param default", tok: "${1:---compress-program=id}", sortWant: true},
		{name: "sort compress param attached", tok: "${1:---co=gzip}", sortWant: true},
		{name: "sort compress env expanded", tok: "${HK_CAND_SORT_X}", sortWant: true},
		{name: "sort benign key default", tok: "${1:--k 2}"},
		{name: "make eval param default", tok: "${1:---eval=id}", makeCheck: true, makeWant: true},
		{name: "make file param default", tok: "${1:--f evil.mk}", makeCheck: true, makeWant: true},
		{name: "make benign directory default", tok: "${1:--C /tmp}", makeCheck: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tarOldStyleExecFlag(tc.tok); got != tc.want {
				t.Errorf("tarOldStyleExecFlag(%q) = %v, want %v", tc.tok, got, tc.want)
			}
			if got := tarWindowHasExecFlag("tar "+tc.tok, true); got != tc.want {
				t.Errorf("tarWindowHasExecFlag(%q) = %v, want %v", "tar "+tc.tok, got, tc.want)
			}
			if got := checkExecFlagHardDeny([]string{"tar", tc.tok}, true) != ""; got != tc.want {
				t.Errorf("checkExecFlagHardDeny(tar %q) = %v, want %v", tc.tok, got, tc.want)
			}
			opt := false
			for _, form := range expandOptionForms([]string{tc.tok}) {
				if tarTokenExecFlag(form) {
					opt = true
					break
				}
			}
			if opt != tc.optWant {
				t.Errorf("expandOptionForms(%q) exec = %v, want %v (%q)", tc.tok, opt, tc.optWant, expandOptionForms([]string{tc.tok}))
			}
			if got := checkExecFlagHardDeny([]string{"sort", tc.tok}, false) != ""; got != tc.sortWant {
				t.Errorf("checkExecFlagHardDeny(sort %q) = %v, want %v", tc.tok, got, tc.sortWant)
			}
			if got := sortWindowHasExecFlag("sort " + tc.tok); got != tc.sortWant {
				t.Errorf("sortWindowHasExecFlag(sort %q) = %v, want %v", tc.tok, got, tc.sortWant)
			}
			if !tc.makeCheck {
				return
			}
			makeOpt := false
			for _, form := range makeOptionEnvForms(tc.tok) {
				if makeOptionExecForm(form) {
					makeOpt = true
					break
				}
			}
			if makeOpt != tc.makeWant {
				t.Errorf("makeOptionEnvForms(%q) exec = %v, want %v", tc.tok, makeOpt, tc.makeWant)
			}
			// The argv token scan must agree with the option-stream walk:
			// a parameter default the stream denies cannot stay Allow when
			// it sits after a real make token.
			if got := checkMakeTokenScan([]string{"make", tc.tok}) != ""; got != tc.makeWant {
				t.Errorf("checkMakeTokenScan(make %q) deny = %v, want %v", tc.tok, got, tc.makeWant)
			}
		})
	}

	// EvaluateCommand-level pins: the same payloads must hard-deny through
	// every carrier the argv legs reach (wrapper shell mode, su -c, and a
	// quoted substitution body). Before the per-view + string-window make
	// legs these carriers stayed Allow under an allow policy.
	allow := round2AllowSB()
	for _, cmd := range []string{
		`make ${1:---eval=id}`,
		`sudo -s 'make -f evil.mk'`,
		`sudo --shell 'make -f evil.mk'`,
		`sudo -i 'make -f evil.mk'`,
		`su -c 'make -f evil.mk'`,
		`echo "$(make -f evil.mk)"`,
		`make$IFS-f evil.mk`,
	} {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) = %s, want deny (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
	// An unresolvable expansion make never sees as a flag stays out: with
	// ZZ_EVAL unset the shell substitutes nothing, so make runs with no
	// makefile/eval argument.
	if d := EvaluateCommand(allow, `make ${ZZ_EVAL}`, nil); d.Action == ActionDeny {
		t.Errorf("EvaluateCommand(make ${ZZ_EVAL}) denied an empty expansion: %s", d.Reason)
	}
}

// TestToolOptionParamOpenerFailClosed pins the raw-view scan beside the
// joined expansion view: a literal (quoted) unterminated `${` must not
// swallow the rest of the line and hide a following exec flag, makefile,
// or option assignment. The joined view still covers a terminated
// expansion the shared shlex split on an inner space.
func TestToolOptionParamOpenerFailClosed(t *testing.T) {
	if reason := checkMakeTokenScan(ss("make", "${", "-f", "evil.mk")); reason == "" {
		t.Error("checkMakeTokenScan literal ${ opener hid -f evil.mk")
	}
	if reason := toolOptionEnvHardDeny(ss("env", "X='${'", "TAR_OPTIONS='-I evil.sh'", "tar", "-xf", "a.tar")); reason == "" {
		t.Error("toolOptionEnvHardDeny literal ${ opener hid TAR_OPTIONS -I")
	}
	if reason := checkMakeTokenScan(ss("make", "MAKEFLAGS=${1:--f", "evil.mk}")); reason == "" {
		t.Error("checkMakeTokenScan joined view missed a split param default")
	}

	allow := round2AllowSB()
	for _, cmd := range []string{
		`make '${' -f evil.mk`,
		`env X='${' TAR_OPTIONS='-I evil.sh' tar -xf a.tar`,
		`env X='${' MAKEFLAGS='-f evil.mk' make`,
		`env X='${' sort --compress-program=id file`,
		`sort ${1:---compress-program=id} file`,
	} {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) = %s, want deny (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// TestGateFailClosedCodeToolPrograms pins awk system() and sed e/s///e
// asking even under allow (treated as opaque interpreter code), while
// plain print/substitute programs stay out.
func TestGateFailClosedCodeToolPrograms(t *testing.T) {
	allow := round2AllowSB()
	ask := []string{
		`awk 'BEGIN{system("id")}' file`,
		`awk '{system("id")}' file`,
		`sudo awk 'BEGIN{system("id")}' file`,
		`find . -exec awk 'BEGIN{system("id")}' {} \;`,
		`sed '1e id' file`,
		`sed -e 's/a/b/e' file`,
		`sed 's/a/b/e' file`,
		`sudo sed 's/a/b/e' file`,
	}
	for _, cmd := range ask {
		t.Run("ask "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want ask (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	for _, cmd := range []string{
		`awk '{print $1}' file`,
		`sed 's/a/b/' file`,
		`sed -e 's/a/b/' file`,
		`sed -n 's/a/b/p' file`,
		`echo "awk system()"`,
		`echo 'sed 1e id'`,
	} {
		t.Run("allow "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAllow {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want allow (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// TestGateFailClosedPhpBeginRun pins php -B/-R as inline-code flags
// (including ANSI-C octal forms) asking even under allow.
func TestGateFailClosedPhpBeginRun(t *testing.T) {
	allow := round2AllowSB()
	ask := []string{
		`php -B 'echo 1;'`,
		`php -R 'echo $line;'`,
		`sudo php -R 'echo hi;'`,
		`php $'-B' 'echo hi;'`,
	}
	for _, cmd := range ask {
		t.Run("ask "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want ask (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// TestGateFailClosedRmNoForceAndDevices pins rm -r without -f denying on
// exact system roots, the widened device set (vd/hd/xvd/ubd/loop/dm-/
// mapper), and the chmod 777 subpath Ask floor.
func TestGateFailClosedRmNoForceAndDevices(t *testing.T) {
	askSB := &sandbox.SandboxConfig{
		Mode:        sandbox.SandboxModePaths,
		Permissions: map[string]string{"system_exec": "ask"},
	}
	allow := round2AllowSB()

	deny := []string{
		`rm -r /`,
		`rm -R /etc/`,
		`rm --recursive /`,
		`dd if=x of=/dev/vda`,
		`dd if=x of=/dev/loop0`,
		`dd if=x of=/dev/dm-0`,
		`dd if=x of=/dev/mapper/vg-lv`,
		`rm -rf /dev/xvda`,
		`mkfs.ext4 /dev/ubd0`,
		`chmod -R 777 /etc`,
		`chmod -R 0777 /etc`,
		`sudo chmod -R 0777 /`,
	}
	for _, cmd := range deny {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(askSB, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) = %s, want deny (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	// chmod 777 on system-root subpaths asks even under allow (exact
	// roots deny in hard-deny), in both 777 and 0777 spellings.
	for _, cmd := range []string{
		`chmod -R 777 /etc/ssh`,
		`chmod -R 0777 /etc/ssh`,
		`sudo chmod -R 0777 /etc/ssh`,
	} {
		t.Run("ask "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want ask (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	// Quoted echo stays green.
	if d := EvaluateCommand(askSB, `echo "chmod -R 777 /etc/ssh"`, nil); d.Action == ActionDeny {
		t.Errorf("quoted chmod echo should not deny, got deny (%s)", d.Reason)
	}
}

// TestGateFailClosedSubstGitAsk pins substitution-led git asking: a
// `$(git -C ...)` substitution still executes git in the named directory,
// so the directory-override Ask fires exactly like bare `git -C`.
func TestGateFailClosedSubstGitAsk(t *testing.T) {
	allow := round2AllowSB()
	for _, cmd := range []string{
		`$(git -C /evil status)`,
		`$(git -C /tmp status)`,
	} {
		t.Run("ask "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want ask (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
}

// TestGateShellPrefixWordsTransparent pins the shell command-opening word
// reachability rule: a carrier or interpreter behind a builtin/keyword
// ("command", "exec", "builtin", "eval", "!", "if", "then", "else", "while",
// "do"), a subshell/group opener ("(", "{", attached "(sudo"/"{sudo"/"(bash"/
// "{bash"/"(su"/"{su"/"(chmod"/"(eval"), or a find -exec prefix is in command
// position, so its hard-deny payload
// still Denies under allow instead of degrading to Ask. eval re-lexes its
// quoted operand like a shell -c payload, so "eval 'rm -rf /'" denies too.
// A prose mention ("echo command sudo -s ...", "echo eval 'rm -rf /'") stays
// green.
func TestGateShellPrefixWordsTransparent(t *testing.T) {
	allow := round2AllowSB()
	deny := []string{
		`command sudo -s 'rm -rf /'`,
		`command sudo -s 'rm -rf /etc'`,
		`command bash -c 'rm -rf /'`,
		`exec bash -c 'rm -rf /'`,
		`exec sudo -s 'rm -rf /'`,
		`builtin bash -c 'rm -rf /'`,
		`builtin sudo -s 'rm -rf /'`,
		`! sudo -s 'rm -rf /'`,
		`! bash -c 'rm -rf /'`,
		`if sudo -s 'rm -rf /'`,
		`then sudo -s 'rm -rf /'`,
		`else bash -c 'rm -rf /'`,
		`elif sudo -s 'rm -rf /'`,
		`while sudo -s 'rm -rf /'`,
		`until bash -c 'rm -rf /'`,
		`do sudo -s 'rm -rf /'`,
		`{ sudo -s 'rm -rf /' ; }`,
		`( sudo -s 'rm -rf /' )`,
		`( bash -c 'rm -rf /' )`,
		`(sudo bash -c 'rm -rf /')`,
		`(sudo -s 'rm -rf /')`,
		`(bash -c 'rm -rf /')`,
		`(su -c 'rm -rf /')`,
		`{sudo -s 'rm -rf /' ; }`,
		`{chmod -R 777 / ; }`,
		`(chmod -R 777 /)`,
		`{bash -c 'rm -rf /' ; }`,
		`{su -c 'rm -rf /' ; }`,
		`(eval 'rm -rf /')`,
		`{eval 'rm -rf /' ; }`,
		`((sudo -s 'rm -rf /'))`,
		`sudo -s 'rm -rf /'`,
		`bash -c 'rm -rf /'`,
		`su -c 'rm -rf /'`,
		`eval sudo -s 'rm -rf /'`,
		`eval 'rm -rf /'`,
		`eval bash -c 'rm -rf /'`,
		`find . -exec command sudo -s 'rm -rf /' \;`,
		`find . -exec ! bash -c 'rm -rf /' \;`,
	}
	for _, cmd := range deny {
		t.Run("deny "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionDeny {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want deny (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	// Opaque interpreter code behind the same openers still fails closed to
	// Ask (not Allow), matching the bare interpreter.
	ask := []string{
		`command sudo -s 'python3 evil.py'`,
		`command bash -c 'python3 evil.py'`,
		`( sudo -s 'python3 evil.py' )`,
		`( bash -c 'python3 evil.py' )`,
		`(sudo -s 'python3 evil.py')`,
		`(bash -c 'python3 evil.py')`,
		`{sudo -s 'python3 evil.py' ; }`,
		`{bash -c 'python3 evil.py' ; }`,
		`{su -c 'python3 evil.py' ; }`,
		`{eval 'python3 evil.py' ; }`,
		`eval sudo -s 'python3 evil.py'`,
		`eval 'python3 evil.py'`,
		`eval 'tar $1'`,
	}
	for _, cmd := range ask {
		t.Run("ask "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAsk {
				t.Errorf("EvaluateCommand(%q) under allow = %s, want ask (%s)", cmd, d.Action, d.Reason)
			}
		})
	}

	// Prose mentions stay green: the openers are arguments of echo, not
	// commands, so the carrier is not reachable.
	for _, cmd := range []string{
		`echo command sudo -s 'rm -rf /'`,
		`echo sudo -s 'rm -rf /'`,
		`echo command sudo -s 'python3 evil.py'`,
		`echo (sudo -s 'rm -rf /')`,
		`echo eval 'rm -rf /'`,
		`echo eval sudo -s 'rm -rf /'`,
	} {
		t.Run("green "+cmd, func(t *testing.T) {
			if d := EvaluateCommand(allow, cmd, nil); d.Action != ActionAllow {
				t.Errorf("EvaluateCommand(%q) = %s, want allow (%s)", cmd, d.Action, d.Reason)
			}
		})
	}
}
