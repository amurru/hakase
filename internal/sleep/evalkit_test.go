// evalkit_test.go - SL-034 acceptance: McNemar exactness, deterministic
// bootstrap CIs, pairing integrity, and the noise-band claim rule.
package sleep

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"amurru/hakase/internal/skill"
)

func TestMcNemarExactKnownValues(t *testing.T) {
	cases := []struct {
		b, c int
		want float64
	}{
		{0, 0, 1},
		{0, 10, 2 * math.Pow(0.5, 10)}, // all candidate wins: 2*(1/2^10)
		{1, 5, 2 * (7.0 / 64.0)},       // n=6, k=1: 2*(C(6,0)+C(6,1))/2^6
		{5, 5, 1},                      // symmetric: p = 1
		{3, 3, 1},
	}
	for _, tc := range cases {
		got := McNemarExact(tc.b, tc.c)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("McNemarExact(%d,%d) = %g, want %g", tc.b, tc.c, got, tc.want)
		}
	}
	if p := McNemarExact(0, 8); p >= 0.05 {
		t.Errorf("8:0 discordance should be significant, p=%g", p)
	}
}

func TestBootstrapCIDeterministicAndSane(t *testing.T) {
	deltas := []float64{0.4, 0.2, -0.1, 0.5, 0.3, 0.25}
	lo1, hi1 := BootstrapCI(deltas, 500, 42)
	lo2, hi2 := BootstrapCI(deltas, 500, 42)
	if lo1 != lo2 || hi1 != hi2 {
		t.Fatalf("bootstrap must be seed-deterministic: [%v,%v] vs [%v,%v]", lo1, hi1, lo2, hi2)
	}
	mean := 0.0
	for _, d := range deltas {
		mean += d
	}
	mean /= float64(len(deltas))
	if lo1 > hi1 {
		t.Errorf("CI inverted: [%v, %v]", lo1, hi1)
	}
	if hi1 < mean-0.4 || lo1 > mean+0.4 {
		t.Errorf("CI implausibly far from the mean %v: [%v, %v]", mean, lo1, hi1)
	}
}

func evalkitStubRunner(scores func(skillBody, taskID string) float64) TargetRunner {
	return func(_ context.Context, skillBody string, task skill.MarkdownTask) (string, []string, error) {
		s := scores(skillBody, task.ID)
		resp := "fail"
		if s >= 0.5 {
			resp = "pass"
		}
		return resp, nil, nil
	}
}

func evalkitTasks(n int) []skill.MarkdownTask {
	tasks := make([]skill.MarkdownTask, 0, n)
	for i := 0; i < n; i++ {
		tasks = append(tasks, skill.MarkdownTask{
			ID: "t" + string(rune('a'+i)), Intent: "do thing " + string(rune('a'+i)),
			ReferenceKind: "rule",
			Judge:         skill.TaskJudge{Checks: []skill.JudgeCheck{{Op: "contains", Text: "pass"}}},
		})
	}
	return tasks
}

func TestRunEvalkitCandidateWins(t *testing.T) {
	base := evalkitStubRunner(func(skillBody, _ string) float64 { return 0 })
	cand := evalkitStubRunner(func(skillBody, _ string) float64 {
		if strings.Contains(skillBody, "candidate") {
			return 1
		}
		return 0
	})
	calls := 0
	run := func(ctx context.Context, body string, task skill.MarkdownTask) (string, []string, error) {
		if strings.Contains(body, "candidate") {
			return cand(ctx, body, task)
		}
		calls++
		return base(ctx, body, task)
	}
	res, err := RunEvalkit(context.Background(), run, nil, EvalkitOpts{
		Tasks: evalkitTasks(6), BaselineBody: "baseline skill", CandidateBody: "candidate skill",
		Bootstrap: 200, Seed: 7,
	})
	if err != nil {
		t.Fatalf("evalkit: %v", err)
	}
	if res.CandidateWins != 6 || res.BaselineWins != 0 || res.Tasks != 6 {
		t.Errorf("discordance = %+v", res)
	}
	if res.Delta <= 0 || res.McNemarP >= 0.05 {
		t.Errorf("a clean 6:0 sweep must be significant: p=%g delta=%g", res.McNemarP, res.Delta)
	}
	if !strings.Contains(res.Claim, "candidate wins") {
		t.Errorf("claim = %q", res.Claim)
	}
	if res.BootstrapReps != 200 {
		t.Errorf("bootstrap reps = %d", res.BootstrapReps)
	}
}

func TestRunEvalkitNoiseBandOverrulesSignificance(t *testing.T) {
	// With pass/fail dichotomy the smallest nonzero delta is 1/n; at n=68
	// one extra pass is 0.0147 < 1.5 points, so the noise rule must hold
	// even though the delta is nonzero.
	const n = 68
	tasks := make([]skill.MarkdownTask, 0, n)
	for i := 0; i < n; i++ {
		id := "t" + fmt.Sprintf("%02d", i)
		tasks = append(tasks, skill.MarkdownTask{
			ID: id, Intent: "do thing " + id,
			ReferenceKind: "rule",
			Judge:         skill.TaskJudge{Checks: []skill.JudgeCheck{{Op: "contains", Text: "pass"}}},
		})
	}
	run := evalkitStubRunner(func(skillBody, taskID string) float64 {
		score := 0.0
		if strings.Contains(skillBody, "candidate") && (taskID == "t00" || taskID == "t01" || taskID == "t02") {
			score = 1
		}
		if strings.Contains(skillBody, "baseline") && (taskID == "t00" || taskID == "t01") {
			score = 1
		}
		return score
	})
	res, err := RunEvalkit(context.Background(), run, nil, EvalkitOpts{
		Tasks: tasks, BaselineBody: "baseline skill", CandidateBody: "candidate skill",
		Bootstrap: 100,
	})
	if err != nil {
		t.Fatalf("evalkit: %v", err)
	}
	if !res.NoiseRange {
		t.Errorf("sub-1.5pt delta (%.4f) must be noise-range: %+v", res.Delta, res)
	}
	if !strings.Contains(res.Claim, "no claim") {
		t.Errorf("claim = %q", res.Claim)
	}
}

func TestRunEvalkitDeadBackendScoresHonestZero(t *testing.T) {
	// A dead runner scores 0 on both sides: evalkit must report a flat,
	// no-claim comparison rather than fabricating a win.
	run := func(_ context.Context, _ string, _ skill.MarkdownTask) (string, []string, error) {
		return "", nil, context.Canceled
	}
	res, err := RunEvalkit(context.Background(), run, nil, EvalkitOpts{
		Tasks: evalkitTasks(3), BaselineBody: "b", CandidateBody: "c",
	})
	if err != nil {
		t.Fatalf("evalkit: %v", err)
	}
	if res.BaselineRate != 0 || res.CandidateRate != 0 || res.Delta != 0 || res.BothFail != 3 {
		t.Errorf("dead backend result = %+v, want honest zeros", res)
	}
	if !res.NoiseRange || !strings.Contains(res.Claim, "no claim") {
		t.Errorf("claim = %q noise=%v", res.Claim, res.NoiseRange)
	}
}

func TestEvalkitClaimReport(t *testing.T) {
	res := EvalkitResult{Delta: 0.4, McNemarP: 0.01, CILow: 0.2, CIHigh: 0.6, BootstrapReps: 10}
	if !strings.Contains(EvalkitClaim(res), "candidate wins") {
		t.Errorf("claim = %s", EvalkitClaim(res))
	}
	report := RenderEvalkitReport(res)
	for _, want := range []string{"Evalkit A/B", "McNemar", "CI of delta: [", "Claim"} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}
