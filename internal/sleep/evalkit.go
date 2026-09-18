// evalkit.go - paired A/B evaluation for markdown skills (plan Phase 3,
// SL-034): the same task manifest replayed against a baseline and a
// candidate skill body, scored per task, and compared with McNemar's exact
// test on the discordant pass/fail pairs plus a seeded bootstrap CI on the
// mean delta. Any "B beats A" claim must carry these numbers; single-seed
// deltas under 1.5 points are noise by rule.
package sleep

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"amurru/hakase/internal/skill"
)

// Evalkit defaults: bootstrap repetitions and the split seed.
const (
	DefaultBootstrapReps = 2000
	// EvalkitNoiseDelta mirrors consolidate.NoiseDeltaThreshold: deltas
	// under 1.5 points (0.015 on the 0..1 gate scale) are noise.
	EvalkitNoiseDelta = 0.015
)

// EvalkitOpts tunes one paired A/B run.
type EvalkitOpts struct {
	Tasks         []skill.MarkdownTask
	BaselineBody  string
	CandidateBody string
	// Metric projects (hard, soft) per task: hard | soft | mixed.
	Metric      string
	MixedWeight float64
	// Bootstrap reps (0 = 2000) and the seeded RNG (0 = 42).
	Bootstrap int
	Seed      int64
	// Timeout bounds one task replay on either side.
	Timeout time.Duration
}

// EvalkitResult is the full comparison: raw discordance, McNemar's exact
// p-value, and the bootstrap CI for the mean per-task delta
// (candidate - baseline; positive favors the candidate).
type EvalkitResult struct {
	Tasks         int     `json:"tasks"`
	BaselineWins  int     `json:"baseline_wins"`
	CandidateWins int     `json:"candidate_wins"`
	BothPass      int     `json:"both_pass"`
	BothFail      int     `json:"both_fail"`
	BaselineRate  float64 `json:"baseline_rate"`
	CandidateRate float64 `json:"candidate_rate"`
	Delta         float64 `json:"delta"`
	McNemarP      float64 `json:"mcnemar_p"`
	CILow         float64 `json:"ci_low"`
	CIHigh        float64 `json:"ci_high"`
	BootstrapReps int     `json:"bootstrap_reps"`
	// NoiseRange flags deltas inside the 1.5-point noise band (report rule:
	// such deltas are unproven regardless of p, plan SL-034).
	NoiseRange bool   `json:"noise_range"`
	Claim      string `json:"claim"`
}

// RunEvalkit replays the manifest against both skill bodies and compares.
// Replay order is deterministic (ReplayBatch); pairing is by task ID, and a
// missing counterpart aborts rather than pairing by position silently.
func RunEvalkit(ctx context.Context, run TargetRunner, rubric RubricJudge, opts EvalkitOpts) (EvalkitResult, error) {
	var res EvalkitResult
	if run == nil {
		return res, fmt.Errorf("evalkit requires a model seam")
	}
	if len(opts.Tasks) == 0 {
		return res, fmt.Errorf("evalkit requires at least one task")
	}
	metric := opts.Metric
	if metric == "" {
		metric = "hard"
	}
	w := opts.MixedWeight
	if w <= 0 {
		w = DefaultMixedWeight
	}
	bootstrap := opts.Bootstrap
	if bootstrap <= 0 {
		bootstrap = DefaultBootstrapReps
	}
	seed := opts.Seed
	if seed == 0 {
		seed = int64(DefaultSplitSeed)
	}

	basePairs := ReplayBatch(ctx, run, opts.BaselineBody, opts.Tasks, rubric, ReplayOpts{Timeout: opts.Timeout})
	candPairs := ReplayBatch(ctx, run, opts.CandidateBody, opts.Tasks, rubric, ReplayOpts{Timeout: opts.Timeout})
	baseScore := make(map[string]float64, len(basePairs))
	for _, p := range basePairs {
		baseScore[p.Task.ID] = skill.SelectGateScore(p.Hard, p.Soft, metric, w)
	}
	candScore := make(map[string]float64, len(candPairs))
	for _, p := range candPairs {
		candScore[p.Task.ID] = skill.SelectGateScore(p.Hard, p.Soft, metric, w)
	}

	deltas := make([]float64, 0, len(opts.Tasks))
	var sumBase, sumCand float64
	for _, t := range opts.Tasks {
		b, okB := baseScore[t.ID]
		c, okC := candScore[t.ID]
		if !okB || !okC {
			return res, fmt.Errorf("evalkit: task %s missing from a replay side", t.ID)
		}
		bPass, cPass := b >= 0.5, c >= 0.5
		switch {
		case bPass && cPass:
			res.BothPass++
		case !bPass && !cPass:
			res.BothFail++
		case bPass:
			res.BaselineWins++
		default:
			res.CandidateWins++
		}
		sumBase += b
		sumCand += c
		deltas = append(deltas, c-b)
	}
	res.Tasks = len(deltas)
	res.BaselineRate = sumBase / float64(res.Tasks)
	res.CandidateRate = sumCand / float64(res.Tasks)
	res.Delta = res.CandidateRate - res.BaselineRate
	res.BootstrapReps = bootstrap
	res.McNemarP = McNemarExact(res.BaselineWins, res.CandidateWins)
	res.CILow, res.CIHigh = BootstrapCI(deltas, bootstrap, seed)
	res.NoiseRange = math.Abs(res.Delta) < EvalkitNoiseDelta
	res.Claim = EvalkitClaim(res)
	return res, nil
}

// McNemarExact returns the two-sided exact McNemar p-value for b vs c
// discordant pairs: p = 2 * P(X <= min(b,c)), X ~ Bin((b+c), 0.5), capped
// at 1. Zero discordance is p=1 (no evidence either way).
func McNemarExact(b, c int) float64 {
	n := b + c
	if n == 0 {
		return 1
	}
	k := b
	if c < k {
		k = c
	}
	// Sum the binomial tail in log space to stay exact for large n.
	logTerm := math.Log(0.5) * float64(n)
	sum := 0.0
	for i := 0; i <= k; i++ {
		sum += math.Exp(logTerm + logChoose(n, i))
	}
	p := 2 * sum
	if p > 1 {
		p = 1
	}
	return p
}

// logChoose returns log(C(n, k)) via lgamma.
func logChoose(n, k int) float64 {
	return logFact(n) - logFact(k) - logFact(n-k)
}

func logFact(n int) float64 {
	// This toolchain's math.Lgamma returns (lgamma, sign); the sign is
	// irrelevant for positive arguments.
	lg, _ := math.Lgamma(float64(n) + 1)
	return lg
}

// BootstrapCI returns the 95% percentile CI of the mean over seeded
// resampling of the paired deltas. Deterministic for a given seed.
func BootstrapCI(deltas []float64, reps int, seed int64) (lo, hi float64) {
	if len(deltas) == 0 {
		return 0, 0
	}
	if reps <= 0 {
		reps = DefaultBootstrapReps
	}
	rng := rand.New(rand.NewSource(seed))
	means := make([]float64, reps)
	buf := make([]float64, len(deltas))
	for r := 0; r < reps; r++ {
		sum := 0.0
		for i := range buf {
			buf[i] = deltas[rng.Intn(len(deltas))]
			sum += buf[i]
		}
		means[r] = sum / float64(len(deltas))
	}
	sort.Float64s(means)
	loIdx := int(0.025 * float64(reps-1))
	hiIdx := int(0.975 * float64(reps-1))
	return means[loIdx], means[hiIdx]
}

// EvalkitClaim renders the verdict line under the report rule: sub-1.5-point
// single-seed deltas are noise regardless of significance; significant wins
// must have a CI excluding zero.
func EvalkitClaim(res EvalkitResult) string {
	significant := res.McNemarP < 0.05
	ciExcludesZero := res.CILow > 0 || res.CIHigh < 0
	switch {
	case res.NoiseRange:
		return fmt.Sprintf("delta %.3f is inside the noise band (<1.5 points): no claim", res.Delta)
	case significant && res.Delta > 0 && res.CILow > 0:
		return fmt.Sprintf("candidate wins: p=%.4g, 95%% CI [%.3f, %.3f]", res.McNemarP, res.CILow, res.CIHigh)
	case significant && res.Delta < 0 && res.CIHigh < 0:
		return fmt.Sprintf("baseline wins: p=%.4g, 95%% CI [%.3f, %.3f]", res.McNemarP, res.CILow, res.CIHigh)
	case significant && !ciExcludesZero:
		return fmt.Sprintf("McNemar significant (p=%.4g) but the bootstrap CI straddles zero: no claim", res.McNemarP)
	default:
		return fmt.Sprintf("no significant difference (p=%.4g, 95%% CI [%.3f, %.3f]): within noise", res.McNemarP, res.CILow, res.CIHigh)
	}
}

// RenderEvalkitReport renders the human-readable A/B report.
func RenderEvalkitReport(res EvalkitResult) string {
	var b strings.Builder
	b.WriteString("# Evalkit A/B\n\n")
	b.WriteString(fmt.Sprintf("- tasks: %d\n- baseline pass rate: %.3f\n- candidate pass rate: %.3f\n- delta (candidate - baseline): %+.3f\n",
		res.Tasks, res.BaselineRate, res.CandidateRate, res.Delta))
	b.WriteString(fmt.Sprintf("- discordant pairs: %d candidate-wins vs %d baseline-wins\n", res.CandidateWins, res.BaselineWins))
	b.WriteString(fmt.Sprintf("- McNemar exact p: %.4g\n- bootstrap 95%% CI of delta: [%+.3f, %+.3f] (%d reps)\n",
		res.McNemarP, res.CILow, res.CIHigh, res.BootstrapReps))
	b.WriteString(fmt.Sprintf("\n**Claim:** %s\n", res.Claim))
	return b.String()
}
