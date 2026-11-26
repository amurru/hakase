package agent

import (
	"strings"
	"testing"
	"time"
)

// interleavedEnvArgv builds [env x] x pairs followed by tail, the shape that
// regressed: x is the first plain operand after the leading env, so every
// later env token is x's operand, not a command-position trampoline. Before
// the command-position stop each env position re-expanded the whole suffix
// (0.9s/3.6s/14.3s at 400/800/1600 pairs).
func interleavedEnvArgv(pairs int, tail ...string) []string {
	argv := make([]string, 0, pairs*2+len(tail))
	for i := 0; i < pairs; i++ {
		argv = append(argv, "env", "x")
	}
	return append(argv, tail...)
}

// alternatingEnvArgv builds [env sudo] x pairs followed by tail: the wrapper
// keeps env in command position, so the chain never settles and exercises
// the work budget instead of the settled-tail stop.
func alternatingEnvArgv(pairs int, tail ...string) []string {
	argv := make([]string, 0, pairs*2+len(tail))
	for i := 0; i < pairs; i++ {
		argv = append(argv, "env", "sudo")
	}
	return append(argv, tail...)
}

// perfBudget replaces absolute wall-clock ceilings (which flake on slow CI)
// with a scaling bound. A larger run is allowed factor times the measured
// baseline, plus generous headroom; a linear scan stays near the baseline,
// while the quadratic/cubic re-expansion this pins exceeds it by orders of
// magnitude. A floor keeps microsecond baselines from making the bound
// meaningless.
func perfBudget(baseline time.Duration, factor int) time.Duration {
	allowed := baseline * time.Duration(factor)
	if allowed < 100*time.Millisecond {
		allowed = 100 * time.Millisecond
	}
	return allowed
}

// TestCheckEnvPositionHardDenyDeepNestingFast pins the chain-walk
// linearization: 500 nested env layers settle in one unwrap and still deny a
// smuggled payload, well inside the baseline-relative budget.
func TestCheckEnvPositionHardDenyDeepNestingFast(t *testing.T) {
	const depth = 500
	build := func(d int, tail ...string) []string {
		argv := make([]string, 0, d+len(tail))
		for i := 0; i < d; i++ {
			argv = append(argv, "env")
		}
		return append(argv, tail...)
	}
	baselineStart := time.Now()
	_ = checkEnvPositionHardDeny(build(64, "git", "status"))
	baseline := time.Since(baselineStart)

	hostile := build(depth, "rm", "-rf", "/")
	start := time.Now()
	if reason := checkEnvPositionHardDeny(hostile); reason == "" {
		t.Fatalf("%d-layer env nesting must deny, got empty", depth)
	}
	if elapsed := time.Since(start); elapsed >= perfBudget(baseline, 64) {
		t.Fatalf("%d-layer env scan took %v, baseline %v", depth, elapsed, baseline)
	}

	// Benign deep nesting stays silent and fast.
	benign := build(depth, "ls")
	start = time.Now()
	if reason := checkEnvPositionHardDeny(benign); reason != "" {
		t.Fatalf("benign %d-layer env nesting must not deny, got %q", depth, reason)
	}
	if elapsed := time.Since(start); elapsed >= perfBudget(baseline, 64) {
		t.Fatalf("benign %d-layer env scan took %v, baseline %v", depth, elapsed, baseline)
	}
}

// TestGateEnvInterleavedScanLinear pins the env-x stop in both gate env
// legs: after the leading env unwraps to the plain operand x, later env
// tokens are x's operands, so the every-position scans must stop instead of
// re-expanding the suffix per env token. The smuggled rm is still denied via
// the primary unwrap, and a benign chain stays silent, both within a
// baseline-relative budget.
func TestGateEnvInterleavedScanLinear(t *testing.T) {
	const pairs = 1600
	baseStart := time.Now()
	_ = checkEnvPositionHardDeny(interleavedEnvArgv(200, "git", "status"))
	_ = envChdirAskReason(interleavedEnvArgv(200, "git", "status"))
	baseline := time.Since(baseStart)

	hostile := interleavedEnvArgv(pairs, "rm", "-rf", "/")
	start := time.Now()
	if reason := checkEnvPositionHardDeny(hostile); reason == "" {
		t.Fatalf("%d-pair env x chain with smuggled rm must deny, got empty", pairs)
	}
	if elapsed := time.Since(start); elapsed >= perfBudget(baseline, 64) {
		t.Fatalf("checkEnvPositionHardDeny(%d env x pairs) took %v, baseline %v", pairs, elapsed, baseline)
	}

	benign := interleavedEnvArgv(pairs, "git", "status")
	start = time.Now()
	if reason := checkEnvPositionHardDeny(benign); reason != "" {
		t.Fatalf("benign %d-pair env x chain must not deny, got %q", pairs, reason)
	}
	if reason := envChdirAskReason(benign); reason != "" {
		t.Fatalf("benign %d-pair env x chain must not ask, got %q", pairs, reason)
	}
	if elapsed := time.Since(start); elapsed >= perfBudget(baseline, 64) {
		t.Fatalf("gate env x scans (%d pairs) took %v, baseline %v", pairs, elapsed, baseline)
	}
}

// TestEvaluateCommandDeepEnvNestingFast pins the end-to-end budget: a deep
// bare env chain through the full EvaluateCommand path stays inside a
// baseline-relative bound and still Allows, while a smuggled payload in the
// same shape still Denies. The gate legs that re-expanded every suffix
// (interpreterTails, codeToolTails) made this shape cubic before their
// command-position/chain-walk bounds: 500 layers took ~1.5s, 2000 ~84s.
func TestEvaluateCommandDeepEnvNestingFast(t *testing.T) {
	const depth = 500
	sb := round2AllowSB()
	baseStart := time.Now()
	_ = EvaluateCommand(sb, strings.Repeat("env ", 64)+"git status", nil)
	baseline := time.Since(baseStart)

	benign := strings.Repeat("env ", depth) + "git status"
	start := time.Now()
	d := EvaluateCommand(sb, benign, nil)
	elapsed := time.Since(start)
	if d.Action != ActionAllow {
		t.Fatalf("EvaluateCommand(%d bare env layers) = %s, want allow (%s)", depth, d.Action, d.Reason)
	}
	if elapsed >= perfBudget(baseline, 64) {
		t.Fatalf("EvaluateCommand(%d bare env layers) took %v, baseline %v", depth, elapsed, baseline)
	}

	hostile := strings.Repeat("env ", depth) + "rm -rf /"
	start = time.Now()
	if d := EvaluateCommand(sb, hostile, nil); d.Action != ActionDeny {
		t.Fatalf("EvaluateCommand(%d bare env layers + rm) = %s, want deny (%s)", depth, d.Action, d.Reason)
	}
	if elapsed := time.Since(start); elapsed >= perfBudget(baseline, 64) {
		t.Fatalf("hostile %d bare env layers took %v, baseline %v", depth, elapsed, baseline)
	}
}

// TestGateEnvPositionBudgetFailClosed pins the work budget: an alternating
// wrapper/env chain never settles on a utility, so the scans stop at the
// bound and fail closed (hard-deny here; envChdirAskReason asks with the
// nesting reason) instead of scanning quadratically forever.
func TestGateEnvPositionBudgetFailClosed(t *testing.T) {
	const pairs = maxEnvPositionScans + 64
	argv := alternatingEnvArgv(pairs, "git", "status")

	baseStart := time.Now()
	_ = checkEnvPositionHardDeny(alternatingEnvArgv(8, "git", "status"))
	baseline := time.Since(baseStart)

	start := time.Now()
	if reason := checkEnvPositionHardDeny(argv); reason == "" {
		t.Fatalf("over-budget env/wrapper chain must fail closed, got empty")
	}
	// envChdirAskReason uses the same budget and asks (never allows).
	seg := append([]string{"sudo"}, argv...)
	if reason := envChdirAskReason(seg); reason == "" {
		t.Fatalf("over-budget env/wrapper chain must ask, got empty")
	}
	if elapsed := time.Since(start); elapsed >= perfBudget(baseline, 64) {
		t.Fatalf("over-budget scans took %v, baseline %v", elapsed, baseline)
	}
}

// sudoShellArgv builds [sudo -s] x pairs followed by tail, the flat
// shell-carrier chain shape.
func sudoShellArgv(pairs int, tail ...string) []string {
	argv := make([]string, 0, pairs*2+len(tail))
	for i := 0; i < pairs; i++ {
		argv = append(argv, "sudo", "-s")
	}
	return append(argv, tail...)
}

// TestSudoShellPayloadChainLinear pins the single-pass sudo payload walk: a
// flat chain of sudo -s carriers used to re-join and re-parse the whole
// suffix per carrier (super-quadratic: 2400 tokens ran for minutes). The
// first reachable carrier's payload covers the rest, so the walk stays
// linear within the baseline-relative budget. The depth cap fails closed for
// a carrier that survives in a packed payload.
func TestSudoShellPayloadChainLinear(t *testing.T) {
	const pairs = 1200
	baseStart := time.Now()
	_ = checkSudoShellPayload(sudoShellArgv(8, "git", "status"))
	baseline := time.Since(baseStart)

	for _, tc := range []struct {
		tail       []string
		wantReason bool
	}{
		{[]string{"git", "status"}, false},
		// The packed quoted payload is the one the payload walk owns:
		// split "rm -rf /" words after a sudo -s chain are consumed as
		// the carrier's own option stream, so the outer token scan owns
		// those. Asserting the verdict keeps a regression from silently
		// turning the hostile chain green.
		{[]string{"rm -rf /"}, true},
		{[]string{"bash", "-c", "rm -rf /"}, true},
	} {
		argv := sudoShellArgv(pairs, tc.tail...)
		start := time.Now()
		reason := checkSudoShellPayload(argv)
		if tc.wantReason && reason == "" {
			t.Fatalf("checkSudoShellPayload(%d sudo -s pairs, %q) = empty, want deny", pairs, tc.tail)
		}
		if !tc.wantReason && reason != "" {
			t.Fatalf("checkSudoShellPayload(%d sudo -s pairs, %q) = %q, want clean", pairs, tc.tail, reason)
		}
		if elapsed := time.Since(start); elapsed >= perfBudget(baseline, 64) {
			t.Fatalf("checkSudoShellPayload(%d sudo -s pairs, %q) took %v, baseline %v", pairs, tc.tail, elapsed, baseline)
		}
	}

	// The depth cap fails closed instead of recursing forever.
	carrier := []string{"sudo", "-s", `sudo -s git status`}
	if reason := checkSudoShellPayloadDepth(carrier, maxCarrierPayloadDepth-1); !strings.Contains(reason, "nests too deeply") {
		t.Errorf("depth-capped carrier = %q, want nesting reason", reason)
	}

	// A nested packed payload still denies end to end.
	if d := EvaluateCommand(round2AllowSB(), `sudo -s 'sudo -s "rm -rf /"'`, nil); d.Action != ActionDeny {
		t.Errorf("nested sudo payload = %s, want deny (%s)", d.Action, d.Reason)
	}
}

// TestGateEnvPositionBudgetNeverAllows pins the truncate invariant: a chain
// longer than maxEnvPositionScans must never be silently truncated into an
// Allow. The hard-deny leg fails closed before per-position work, and the
// ask leg reports the split-string nesting reason, so EvaluateCommand can
// only Deny or Ask (and an Ask carries the nesting reason, never a benign
// one).
func TestGateEnvPositionBudgetNeverAllows(t *testing.T) {
	const pairs = maxEnvPositionScans + 64
	sb := round2AllowSB()
	for _, tail := range [][]string{
		{"git", "status"},
		{"rm", "-rf", "/"},
		{"python3", "-c", "print(1)"},
	} {
		argv := alternatingEnvArgv(pairs, tail...)
		d := EvaluateCommand(sb, "", argv)
		if d.Action == ActionAllow {
			t.Fatalf("over-budget chain ending %q = allow, want deny/ask", tail)
		}
		if d.Action == ActionAsk && !strings.Contains(d.Reason, "split-string") {
			t.Errorf("over-budget chain ending %q asked %q, want nesting reason", tail, d.Reason)
		}
	}
}
