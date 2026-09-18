// cycle.go - one SkillOpt-Sleep night (plan Phase 2, SL-022): harvest ->
// mine (or a reviewed seed task file) -> per-skill consolidation behind the
// held-out gate -> stage unless dry-run -> optional managed-only auto-adopt
// -> state save with spend guards and the model-identity pause.
//
// Spend guards (M2) enforced here: per-night timeout (context), per-task
// timeout (ReplayOpts), token ledger over every seam call, and the
// per-task tool-call budget threaded into the agentic runner. Exceeding a
// guard aborts the night and records it in the night diagnostics.
package sleep

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"amurru/hakase/internal/skill"
)

// Spend-guard defaults (plan M2). MaxTokensPerNight counts estimated tokens
// (chars/4) over every seam call: baseline+candidate replays, judge,
// miner, and optimizer.
const (
	DefaultPerNightTimeout = time.Hour
	DefaultMaxTokensNight  = 2_000_000
)

// CycleOpts tunes one night. Model seams are function-injected so the cycle
// never hard-depends on a live model (tests run fully offline).
type CycleOpts struct {
	// SeedTasksPath runs the night from an existing (reviewed) task file
	// instead of harvest+mine. M4 is enforced: machine-generated files must
	// carry a valid review sidecar.
	SeedTasksPath string
	// Harvest/mine tuning (used when SeedTasksPath is empty).
	Harvest HarvestOpts
	Mine    MineOpts

	// Call is the base model seam; every call the cycle makes (runner,
	// judge, reflector) is routed through the token ledger over it.
	Call ModelCaller
	// JudgeCall, when set, is a SEPARATE model seam for rubric judging
	// (plan H2, completed with Phase 3): judge independence is refused at
	// the CLI when it resolves to the target model unless
	// AllowSharedJudge. Nil shares the Call seam (recorded in the report
	// as shared_backend=true).
	JudgeCall ModelCaller
	JudgeModelKey      string
	AllowSharedJudge   bool
	// Run overrides the replay runner (agentic mode). Nil builds a
	// single-shot runner over Call. Agentic runners cannot report token
	// usage at this seam; the diagnostics note the unaccounted spend.
	Run TargetRunner
	// ReflectorFor builds the per-skill reflector over the counted caller.
	// Nil uses DefaultReflectorFor.
	ReflectorFor func(call ModelCaller, skillName string, budget int, rc ReflectContext) Reflector

	// Gate knobs (identical semantics to ConsolidateOpts).
	EditBudget        int
	GateMetric        string
	MixedWeight       float64
	GateNoRegression  bool
	Greedy            bool

	// Learning-rate schedule (Phase 3, SL-030): LRScheduler
	// constant|linear|cosine decays the per-night edit budget from
	// EditBudget toward LRFloor across LRHorizon nights. The epoch index is
	// the number of nights already recorded in the sleep state, so the
	// schedule is reproducible from the state file alone. Defaults:
	// constant (no decay); LRFloor 0; LRHorizon 0 = never decay.
	LRScheduler string
	LRFloor     int
	LRHorizon   int

	// RankerFor builds the per-night rank_and_select seam over the counted
	// caller (plan SL-030). Nil uses documented truncation fallback.
	RankerFor func(call ModelCaller) Ranker

	// Spend guards.
	PerTaskTimeout    time.Duration
	PerNightTimeout   time.Duration
	MaxTokensPerNight int

	// Skill selection: SkillName evolves one hint (fail-closed when it
	// does not resolve); FanOut evolves every resolvable group; neither
	// evolves the single largest group.
	SkillName string
	SkillRoot string
	FanOut    bool
	// AutoAdopt installs accepted proposals for managed skills only (M5):
	// a skill doc carrying a sleep learned block. Hand-written skills
	// always stage for explicit adopt.
	AutoAdopt bool

	// ModelKey is the provider/model identity for the pause rule; a change
	// vs the state hard-pauses until AcknowledgeModelChange.
	ModelKey               string
	AcknowledgeModelChange bool

	// Paths. Empty selects the documented defaults.
	StatePath string // .hakase/sleep-state.json
	OutputDir string // outputs/sleep
	WorkDir   string // skill discovery root ("" = process cwd)

	// Phase 3 plumbing (SL-033): RecallK recalls top-k knowledge notes
	// (BM25 over KnowledgeDir) into the reflector context; DreamRollouts
	// with DreamFactor > 0 synthesizes contrastive dream tasks (factor ×
	// train tasks, capped) quarantined to the train slice. All default off.
	RecallK       int
	KnowledgeDir  string
	DreamRollouts bool
	DreamFactor   float64
	// DreamMiner overrides the dream synthesis seam (tests). Nil uses
	// DefaultDreamMiner over the counted caller.
	DreamMiner DreamMiner
	// SkillAware enables skill-aware reflection routing (SL-032); default
	// off. Patch-only is structural (the applier has no rewrite op).
	SkillAware bool
	// DreamConsolidate is reserved (memory-trial surface, not implemented);
	// recorded in diagnostics when set.
	DreamConsolidate bool
	// Progress, when set, receives one line per night milestone (group
	// selection, per-group completion, truncation) for long-running
	// nights. Nil stays silent (SL-041).
	Progress func(line string)

	// DryRun makes the night read-only: harvest + mine + counts, with no
	// model call, no staging, no adopt, and no state advance.
	DryRun bool

	// Now pins wall-clock time for tests.
	Now func() time.Time
}

// GroupResult is one skill group's night outcome.
type GroupResult struct {
	SkillName     string               `json:"skill_name"`
	Tasks         int                  `json:"tasks"`
	Consolidation *ConsolidationResult `json:"consolidation,omitempty"`
	StagingDir    string               `json:"staging_dir,omitempty"`
	Adopted       bool                 `json:"adopted,omitempty"`
	// Consolidated reports whether real replay work ran (feeds the
	// state file's slow-update history; dead groups never count).
	Consolidated bool `json:"consolidated,omitempty"`
	// DreamTasks counts synthesized dream tasks folded into the group
	// (SL-033 cost accounting).
	DreamTasks int `json:"dream_tasks,omitempty"`
	// Notes carry per-group night notes (slow-update trend, recall, dream).
	Notes []string `json:"notes,omitempty"`
	// Skipped records why nothing ran for this group; skips are logged in
	// the night report, never silent.
	Skipped string `json:"skipped,omitempty"`
}

// CycleResult summarizes the night for the CLI and cron output.
type CycleResult struct {
	DryRun            bool          `json:"dry_run"`
	HarvestedSessions int           `json:"harvested_sessions"`
	MinedTasks        int           `json:"mined_tasks"`
	TokensUsed        int           `json:"tokens_used"`
	Truncated         bool          `json:"truncated"`
	StagingDir        string        `json:"staging_dir,omitempty"`
	Groups            []GroupResult `json:"groups"`
	Log               []string      `json:"log,omitempty"`
	// Model identities for the report (plan H2: the report always emits
	// target/judge/optimizer and the shared-backend flag). The optimizer
	// shares the target caller today; JudgeModel names the judge seam.
	TargetModel   string `json:"target_model,omitempty"`
	JudgeModel    string `json:"judge_model,omitempty"`
	SharedBackend bool   `json:"shared_backend,omitempty"`
}

// tokenLedger estimates and caps night spend across every seam call.
// Tokens := chars/4 (provider-reported counts are not visible at this
// seam); the estimate is documented in the night diagnostics.
type tokenLedger struct {
	mu    sync.Mutex
	tokens int
	max   int
}

// wrap returns a ModelCaller that accounts and enforces the budget.
func (l *tokenLedger) wrap(call ModelCaller) ModelCaller {
	return func(ctx context.Context, prompt string) (string, error) {
		resp, err := call(ctx, prompt)
		l.mu.Lock()
		l.tokens += (len(prompt) + len(resp)) / 4
		over := l.max > 0 && l.tokens > l.max
		l.mu.Unlock()
		if over {
			return "", fmt.Errorf("night token budget exceeded (%d est. tokens)", l.tokens)
		}
		return resp, err
	}
}

func (l *tokenLedger) used() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tokens
}

func (l *tokenLedger) exceeded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.max > 0 && l.tokens > l.max
}

// RunCycle executes one night.
func RunCycle(ctx context.Context, opts CycleOpts) (CycleResult, error) {
	started := time.Now().UTC()
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	res := CycleResult{}

	statePath := opts.StatePath
	if statePath == "" {
		statePath = DefaultStatePath
	}
	outputDir := opts.OutputDir
	if outputDir == "" {
		outputDir = filepath.Join("outputs", "sleep")
	}
	workDir := opts.WorkDir
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return res, err
		}
		workDir = wd
	}

	state, note, err := LoadSleepState(statePath)
	if err != nil {
		return res, err
	}
	if note != "" {
		res.Log = append(res.Log, note)
	}

	// Sources: reviewed seed file or harvest+mine.
	var tasks []skill.MarkdownTask
	harvested := 0
	if opts.SeedTasksPath != "" {
		if err := RequireReviewed(opts.SeedTasksPath); err != nil {
			return res, err
		}
		tasks, err = LoadMarkdownTasks(opts.SeedTasksPath)
		if err != nil {
			return res, err
		}
	} else {
		file, err := Harvest(opts.Harvest)
		if err != nil {
			return res, err
		}
		harvested = len(file.Sessions)
		mineRes := Mine(file.Sessions, opts.Mine)
		tasks = mineRes.Tasks
		res.Log = append(res.Log, mineRes.Log...)
	}
	res.HarvestedSessions = harvested
	res.MinedTasks = len(tasks)

	// Dry-run: read-only counts. No model, no writes, no state advance.
	if opts.DryRunMode() {
		res.DryRun = true
		res.Groups = dryRunGroups(tasks)
		return res, nil
	}

	// Model identity pause: a changed provider/model is a HARD stop until
	// acknowledged (plan SL-022), never a warning.
	if err := CheckModelKey(state, opts.ModelKey, opts.AcknowledgeModelChange); err != nil {
		return res, err
	}
	if opts.Call == nil {
		return res, fmt.Errorf("sleep run requires a model seam (Call); use dry-run for read-only counts")
	}

	// Spend guards.
	perNight := opts.PerNightTimeout
	if perNight <= 0 {
		perNight = DefaultPerNightTimeout
	}
	maxTokens := opts.MaxTokensPerNight
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokensNight
	}
	ledger := &tokenLedger{max: maxTokens}
	nightCtx, cancel := context.WithTimeout(ctx, perNight)
	defer cancel()

	counted := ledger.wrap(opts.Call)
	// Judge separation (plan H2, completed with Phase 3): a dedicated judge
	// caller keeps rubric scoring independent of the optimizer/target model.
	// Sharing is recorded in the report, never silent.
	judgeCall := opts.JudgeCall
	if judgeCall == nil {
		res.Log = append(res.Log, "judge shares the optimizer/target caller (shared_backend=true)")
		judgeCall = opts.Call
	}
	judgeCounted := ledger.wrap(judgeCall)
	run := opts.Run
	if run == nil {
		run = SingleShotTarget(counted)
	}
	rubric := DefaultRubricJudge(judgeCounted)
	reflectorFor := opts.ReflectorFor
	if reflectorFor == nil {
		reflectorFor = DefaultReflectorFor
	}
	var ranker Ranker
	if opts.RankerFor != nil {
		ranker = opts.RankerFor(counted)
	}
	var dreamMiner DreamMiner
	if opts.DreamRollouts && opts.DreamFactor > 0 {
		dreamMiner = opts.DreamMiner
		if dreamMiner == nil {
			dreamMiner = DefaultDreamMiner(counted)
		}
	}
	res.TargetModel = opts.ModelKey
	if opts.JudgeCall != nil {
		res.JudgeModel = opts.JudgeModelKey
		if res.JudgeModel == "" {
			res.JudgeModel = "judge"
		}
	} else {
		res.JudgeModel = opts.ModelKey
		res.SharedBackend = true
	}

	// Per-skill epoch history for the slow-update trend (SL-031), oldest
	// first as recorded.
	history := make(map[string][]NightGroupRecord)
	for _, n := range state.Nights {
		for _, g := range n.Groups {
			key := strings.ToLower(g.SkillName)
			history[key] = append(history[key], g)
		}
	}

	grouped := groupTasks(tasks)
	selected, selLog := selectGroups(grouped, opts)
	res.Log = append(res.Log, selLog...)
	progressf := func(format string, args ...any) {
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf(format, args...))
		}
	}
	progressf("%d skill group(s) selected for tonight", len(selected))

	// Learning-rate epoch: nights already recorded. The state save below
	// appends tonight, so tonight's epoch index is exactly this.
	epoch := len(state.Nights)

	nightDir := filepath.Join(outputDir, now().UTC().Format("20060102-150405"))
	anyStaged := false
	for _, g := range selected {
		if ledger.exceeded() || nightCtx.Err() != nil {
			res.Truncated = true
			break
		}
		gr := runGroup(nightCtx, g, opts, runOpts{
			call: counted, run: run, rubric: rubric, reflectorFor: reflectorFor,
			perTask: opts.PerTaskTimeout, workDir: workDir, skillRoot: opts.SkillRoot,
			nightDir: nightDir, autoAdopt: opts.AutoAdopt, ranker: ranker, epoch: epoch,
			history: history, dreamMiner: dreamMiner, knowledgeDir: opts.KnowledgeDir,
			recallK: opts.RecallK, dreamFactor: opts.DreamFactor, skillAware: opts.SkillAware,
		})
		res.Groups = append(res.Groups, gr)
		if gr.StagingDir != "" {
			anyStaged = true
		}
		progressf("group %s: %s", gr.SkillName, groupVerdict(gr))
	}
	res.TokensUsed = ledger.used()
	if ledger.exceeded() || nightCtx.Err() != nil {
		res.Truncated = true
		res.Log = append(res.Log, fmt.Sprintf("night truncated: tokens=%d (est.) deadline_left=%v",
			res.TokensUsed, perNight))
		progressf("night truncated by a spend guard")
	}

	// Stage the night container (report + diagnostics) when anything ran.
	if anyStaged {
		res.StagingDir = nightDir
		if err := writeNightFiles(nightDir, res); err != nil {
			return res, err
		}
	}

	// State advance: the checkpoint moves even on an empty night.
	newState := SleepState{
		LastHarvest:  now().UTC(),
		LastModelKey: opts.ModelKey,
	}
	rec := NightRecord{
		StartedAt:  started,
		Outcome:    nightOutcome(res),
		StagingDir: res.StagingDir,
		Sessions:   harvested,
		Tasks:      res.MinedTasks,
		Tokens:     res.TokensUsed,
		AbortReason: abortReason(res),
		Groups:      nightGroups(res, started),
	}
	newState.Nights = append(state.Nights, rec)
	if err := SaveSleepState(statePath, newState); err != nil {
		return res, fmt.Errorf("save sleep state: %w", err)
	}

	// Retention: prune staging dirs older than 30d (best-effort).
	pruneStagingDirs(outputDir, StatePruneAge, now)
	return res, nil
}

// ReflectContext carries the reflector's ambient context for one group:
// the optimizer memory (meta-skill sidecar + recalled lessons, never part
// of the skill document) and the skill-aware routing switch.
type ReflectContext struct {
	OptimizerMemory string
	SkillAware      bool
}

// DefaultReflectorFor builds the standard optimizer reflector over a
// caller: failures in, bounded TextEdit JSON out. Successes are omitted
// from the prompt (failures carry the signal); a non-parseable reply
// returns no edits with the raw text preserved for diagnostics.
func DefaultReflectorFor(call ModelCaller, skillName string, budget int, rc ReflectContext) Reflector {
	return func(ctx context.Context, failures, _ []ScoredTask, skillBody string, b int) ([]skill.TextEdit, string, error) {
		if b <= 0 {
			b = DefaultEditBudget
		}
		var mfails []skill.MarkdownFailure
		for _, f := range failures {
			mfails = append(mfails, skill.MarkdownFailure{Task: f.Task, Actual: f.Response, Error: f.FailReason})
		}
		var prompt string
		if rc.SkillAware {
			prompt = skill.BuildSkillAwareMutationPrompt(skillName, skillBody, mfails, b, rc.OptimizerMemory)
		} else {
			prompt = skill.BuildMarkdownMutationPrompt(skillName, skillBody, mfails, b, rc.OptimizerMemory)
		}
		raw, err := call(ctx, prompt)
		if err != nil {
			return nil, "", err
		}
		edits, ok := skill.ParseMarkdownEdits(raw)
		if !ok {
			return nil, raw, nil
		}
		return edits, raw, nil
	}
}

// nightGroups extracts the per-skill epoch records for the state file:
// only groups where real replay work ran feed the slow-update history.
func nightGroups(res CycleResult, started time.Time) []NightGroupRecord {
	var out []NightGroupRecord
	for _, g := range res.Groups {
		if g.Consolidation == nil || !g.Consolidated {
			continue
		}
		out = append(out, NightGroupRecord{
			SkillName:      g.SkillName,
			StartedAt:      started,
			BaselineScore:  g.Consolidation.BaselineScore,
			CandidateScore: g.Consolidation.CandidateScore,
			Accepted:       g.Consolidation.Accepted,
			Consolidated:   true,
		})
	}
	return out
}

// DryRunMode reports whether opts selects read-only dry-run behavior.
func (o CycleOpts) DryRunMode() bool { return o.DryRun }

// groupVerdict renders one progress line for a finished group.
func groupVerdict(gr GroupResult) string {
	if gr.Skipped != "" {
		return "skipped: " + gr.Skipped
	}
	if gr.Adopted {
		return "accepted + auto-adopted (managed skill)"
	}
	if gr.StagingDir != "" {
		return fmt.Sprintf("gate %s, staged for review", gr.Consolidation.GateAction)
	}
	if gr.Consolidation != nil {
		return fmt.Sprintf("gate %s, no proposal", gr.Consolidation.GateAction)
	}
	return "no work"
}

// runOpts bundles the counted seams for runGroup.
type runOpts struct {
	call         ModelCaller
	run          TargetRunner
	rubric       RubricJudge
	reflectorFor func(call ModelCaller, skillName string, budget int, rc ReflectContext) Reflector
	perTask      time.Duration
	workDir      string
	skillRoot    string
	nightDir     string
	autoAdopt    bool
	ranker       Ranker
	epoch        int
	history      map[string][]NightGroupRecord
	dreamMiner   DreamMiner
	knowledgeDir string
	recallK      int
	dreamFactor  float64
	skillAware   bool
}

// runGroup resolves, consolidates, stages, and (optionally) auto-adopts one
// skill group. Unresolvable hints skip with a logged reason; unreadable
// live files abort the night.
func runGroup(ctx context.Context, g taskGroup, opts CycleOpts, r runOpts) GroupResult {
	gr := GroupResult{SkillName: g.hint, Tasks: len(g.tasks)}

	matched, err := resolveSkillMatches(r.workDir, r.skillRoot, g.hint)
	if err != nil {
		gr.Skipped = err.Error()
		return gr
	}
	if len(matched) == 0 {
		gr.Skipped = fmt.Sprintf("no markdown skill matches hint %q", g.hint)
		return gr
	}
	if len(matched) > 1 {
		// Fail-closed duplicate detection (plan SL-013): never pick a winner
		// silently; disambiguate with --skill-root.
		gr.Skipped = fmt.Sprintf("ambiguous hint %q matches %d skills across roots: %s",
			g.hint, len(matched), strings.Join(matched, ", "))
		return gr
	}
	livePath := matched[0]
	liveBytes, err := os.ReadFile(livePath)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		liveBytes = nil // missing baseline is an empty, valid one
	default:
		gr.Skipped = fmt.Sprintf("read live skill: %v", err)
		return gr
	}
	if !utf8.Valid(liveBytes) {
		gr.Skipped = "live skill is not valid UTF-8"
		return gr
	}
	baseline := string(liveBytes)

	// Slow update (SL-031): tonight's baseline carries the epoch-trend
	// guidance as a protected tail block. Both sides of the gate replay
	// the same block, so the comparison stays fair; step edits can never
	// touch it.
	history := r.history[strings.ToLower(g.hint)]
	trend := ClassifyEpochTrend(history)
	if guidance := TrendGuidance(trend); len(guidance) > 0 {
		baseline = skill.SetSlowUpdate(baseline, guidance)
		gr.Notes = append(gr.Notes, fmt.Sprintf("slow update: trend=%s over %d night(s)", trend, len(history)))
	}

	// Optimizer context (SL-031 sidecar + SL-033 recall): prepended to the
	// reflector prompt, never deployed with the skill.
	rc := ReflectContext{SkillAware: r.skillAware}
	rc.OptimizerMemory = ReadOptimizerMemory(livePath)
	if r.recallK > 0 && r.knowledgeDir != "" {
		intents := make([]string, 0, len(g.tasks))
		for _, t := range g.tasks {
			intents = append(intents, t.Intent)
		}
		if notes := RecallNotes(r.knowledgeDir, intents, r.recallK); len(notes) > 0 {
			rc.OptimizerMemory = strings.TrimSpace(rc.OptimizerMemory + "\n\n" + RenderRecallBlock(notes))
			gr.Notes = append(gr.Notes, fmt.Sprintf("recall: %d knowledge note(s) into reflector context", len(notes)))
		}
	}

	budget := opts.EditBudget
	if budget <= 0 {
		budget = DefaultEditBudget
	}
	// Learning-rate schedule (SL-030): tonight's budget decays across the
	// configured horizon; the report shows the scheduled budget.
	budget = skill.ScheduledEditBudget(budget, opts.LRScheduler, r.epoch, opts.LRHorizon, opts.LRFloor)

	// Dream rollouts (SL-033): contrastive synthetics distilled from the
	// group's train tasks, quarantined to train by origin. Off by default.
	groupTasks := g.tasks
	if r.dreamMiner != nil {
		trainish := make([]skill.MarkdownTask, 0, len(groupTasks))
		for _, t := range groupTasks {
			s := normalizeSplit(t.Split)
			if s == "" || s == "train" {
				trainish = append(trainish, t)
			}
		}
		if len(trainish) > 0 {
			dreams, err := r.dreamMiner(ctx, g.hint, trainish, r.dreamFactor, DefaultMaxDreamTasks)
			if err != nil {
				gr.Notes = append(gr.Notes, "dream rollouts failed: "+err.Error())
			} else if len(dreams) > 0 {
				groupTasks = append(append([]skill.MarkdownTask(nil), groupTasks...), dreams...)
				gr.DreamTasks = len(dreams)
				gr.Notes = append(gr.Notes, fmt.Sprintf("dream: %d synthetic train task(s)", len(dreams)))
			}
		}
	}

	// Deny-by-default audit (SL-040): agentic replay records off-allowlist
	// tool attempts and sandbox path refusals onto this per-group audit;
	// single-shot replay has no tools and records nothing.
	var audit ReplayAudit
	result := Consolidate(withReplayAudit(ctx, &audit), r.run, r.rubric, groupTasks, baseline,
		ReplayOpts{Timeout: r.perTask},
		ConsolidateOpts{
			EditBudget: budget, GateMetric: opts.GateMetric,
			MixedWeight: opts.MixedWeight, GateNoRegression: opts.GateNoRegression,
			Greedy: opts.Greedy, Ranker: r.ranker, SkillAware: r.skillAware,
			Reflect: r.reflectorFor(r.call, g.hint, budget, rc),
		})
	if snap := audit.snapshot(); len(snap.DeniedToolAttempts) > 0 || snap.SandboxPathDenials > 0 {
		result.ReplayDenials = &snap
	}
	gr.Consolidation = &result
	gr.Consolidated = consolidationRan(result)

	// Optimizer memory sidecar (SL-031): tonight's row joins the history
	// and the sidecar is rewritten with the post-night trend. Never part
	// of the skill document; written only for skills that exist on disk.
	if liveBytes != nil {
		tonight := NightGroupRecord{
			SkillName: g.hint, BaselineScore: result.BaselineScore,
			CandidateScore: result.CandidateScore, Accepted: result.Accepted,
			Consolidated: gr.Consolidated,
		}
		full := append(append([]NightGroupRecord(nil), history...), tonight)
		if err := WriteOptimizerMemory(livePath, full, ClassifyEpochTrend(full)); err != nil {
			gr.Notes = append(gr.Notes, "optimizer memory write failed: "+err.Error())
		}
	}

	// Validation only (dry-run handled upstream): stage unless nothing ran.
	if result.GateAction == "noop" && len(result.Deltas) == 0 && result.NoEditsReason != "" &&
		result.BaselineScore == 0 && result.CandidateScore == 0 {
		// A fully dead group (backend down, no tasks): report, no staging.
		return gr
	}
	skillDir := filepath.Join(r.nightDir, sanitizeDirName(g.hint))
	if err := StageConsolidationInto(skillDir, g.hint, livePath, result); err != nil {
		gr.Skipped = fmt.Sprintf("stage: %v", err)
		return gr
	}
	gr.StagingDir = skillDir

	// Managed-only auto-adopt (M5): hand-written skills stage and wait.
	if r.autoAdopt && result.Accepted && skill.IsSleepManagedDoc(baseline) {
		if _, _, err := AdoptStaging(skillDir); err != nil {
			gr.Skipped = fmt.Sprintf("auto-adopt refused: %v", err)
			return gr
		}
		gr.Adopted = true
	}
	return gr
}

// resolveSkillMatches returns every discovered markdown skill whose
// frontmatter name equals hint (case-insensitive), as live paths.
func resolveSkillMatches(workDir, skillRoot, hint string) ([]string, error) {
	var extra []string
	if skillRoot != "" {
		extra = append(extra, skillRoot)
	}
	all := skill.DiscoverMarkdownSkills(workDir, extra, func(string) {})
	var paths []string
	for _, s := range all {
		if strings.EqualFold(s.Frontmatter.Name, hint) {
			paths = append(paths, s.Path)
		}
	}
	return paths, nil
}

// taskGroup is the mined tasks of one skill hint.
type taskGroup struct {
	hint  string
	tasks []skill.MarkdownTask
}

// groupTasks buckets tasks by skill hint (deterministic: sorted keys via
// the caller).
func groupTasks(tasks []skill.MarkdownTask) map[string]taskGroup {
	m := map[string]taskGroup{}
	for _, t := range tasks {
		hint := strings.TrimSpace(t.SkillHint)
		if hint == "" {
			hint = ManagedCatchAll
		}
		g, ok := m[hint]
		if !ok {
			g = taskGroup{hint: hint}
		}
		g.tasks = append(g.tasks, t)
		m[hint] = g
	}
	return m
}

// selectGroups picks which groups run tonight. Deterministic order.
func selectGroups(grouped map[string]taskGroup, opts CycleOpts) ([]taskGroup, []string) {
	var hints []string
	for h := range grouped {
		hints = append(hints, h)
	}
	sort.Strings(hints)
	var log []string

	var chosen []taskGroup
	switch {
	case opts.SkillName != "":
		found := false
		for _, h := range hints {
			if strings.EqualFold(h, opts.SkillName) {
				chosen = append(chosen, grouped[h])
				found = true
			}
		}
		if !found {
			log = append(log, fmt.Sprintf("requested skill %q has no mined tasks tonight", opts.SkillName))
		}
	case opts.FanOut:
		for _, h := range hints {
			chosen = append(chosen, grouped[h])
		}
	default:
		// Single-group night: the largest group wins (tie: lexicographic,
		// which the sorted iteration already guarantees).
		var best string
		for _, h := range hints {
			if best == "" || len(grouped[h].tasks) > len(grouped[best].tasks) {
				best = h
			}
		}
		if best != "" {
			chosen = append(chosen, grouped[best])
			for _, h := range hints {
				if h != best {
					log = append(log, fmt.Sprintf("skipped group %q (fan-out disabled; use --fan-out or --skill)", h))
				}
			}
		}
	}
	return chosen, log
}

// dryRunGroups renders read-only per-hint counts for dry-run output.
func dryRunGroups(tasks []skill.MarkdownTask) []GroupResult {
	grouped := groupTasks(tasks)
	hints := make([]string, 0, len(grouped))
	for h := range grouped {
		hints = append(hints, h)
	}
	sort.Strings(hints)
	var out []GroupResult
	for _, h := range hints {
		out = append(out, GroupResult{SkillName: h, Tasks: len(grouped[h].tasks)})
	}
	return out
}

// sanitizeDirName keeps hint names inside one path segment.
func sanitizeDirName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "group"
	}
	return out
}

// nightOutcome classifies the night for the state record.
func nightOutcome(res CycleResult) string {
	if res.Truncated {
		return "aborted"
	}
	if res.StagingDir != "" {
		return "staged"
	}
	return "empty"
}

// abortReason extracts the human reason for an aborted night.
func abortReason(res CycleResult) string {
	if !res.Truncated {
		return ""
	}
	return "spend guard tripped (token budget or night deadline)"
}

// writeNightFiles drops the night-level report and diagnostics into the
// night dir (per-skill files already live in subdirectories).
func writeNightFiles(nightDir string, res CycleResult) error {
	if err := os.MkdirAll(nightDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(nightDir, 0o700)
	report := RenderNightReport(res)
	if err := os.WriteFile(filepath.Join(nightDir, "night.md"), []byte(report), 0o600); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Join(nightDir, "night.md"), 0o600)
	blob, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(nightDir, "night.json"), blob, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Join(nightDir, "night.json"), 0o600)
	return nil
}

// RenderNightReport renders the human-readable night summary.
func RenderNightReport(res CycleResult) string {
	var b strings.Builder
	if res.DryRun {
		b.WriteString("# Sleep dry-run (read-only)\n\n")
	} else {
		b.WriteString("# Sleep night report\n\n")
	}
	b.WriteString(fmt.Sprintf("- sessions harvested: %d\n- tasks mined: %d\n- tokens (est.): %d\n",
		res.HarvestedSessions, res.MinedTasks, res.TokensUsed))
	if res.Truncated {
		b.WriteString("- **truncated**: a spend guard fired; the night aborted early\n")
	}
	if res.TargetModel != "" || res.JudgeModel != "" {
		b.WriteString(fmt.Sprintf("- models: target=%s judge=%s optimizer=%s shared_backend=%v\n",
			res.TargetModel, res.JudgeModel, res.TargetModel, res.SharedBackend))
	}
	if res.StagingDir != "" {
		b.WriteString(fmt.Sprintf("- staged under `%s`\n", res.StagingDir))
	}
	b.WriteString("\n| Skill | Tasks | Baseline → Candidate | Gate | Result |\n|---|---:|---:|---|---|\n")
	for _, g := range res.Groups {
		gate, score := "-", "-"
		verdict := "no work"
		if g.Skipped != "" {
			verdict = "skipped: " + g.Skipped
		}
		if g.Consolidation != nil {
			gate = g.Consolidation.GateAction
			score = fmt.Sprintf("%.3f → %.3f", g.Consolidation.BaselineScore, g.Consolidation.CandidateScore)
			switch {
			case g.Adopted:
				verdict = "auto-adopted (managed skill)"
			case g.StagingDir != "":
				verdict = "staged for review"
			default:
				verdict = "no proposal"
			}
			if g.Consolidation.NoiseRange && g.Consolidation.Accepted {
				verdict += " [noise-range]"
			}
		}
		if g.DreamTasks > 0 {
			verdict += fmt.Sprintf(" (+%d dream)", g.DreamTasks)
		}
		b.WriteString(fmt.Sprintf("| %s | %d | %s | %s | %s |\n",
			g.SkillName, g.Tasks, score, gate, verdict))
	}
	var notes []string
	for _, g := range res.Groups {
		for _, n := range g.Notes {
			notes = append(notes, g.SkillName+": "+n)
		}
	}
	if len(notes) > 0 {
		b.WriteString("\n## Group notes\n\n")
		for _, ln := range notes {
			b.WriteString("- " + ln + "\n")
		}
	}
	if len(res.Log) > 0 {
		b.WriteString("\n## Notes\n\n")
		for _, ln := range res.Log {
			b.WriteString("- " + ln + "\n")
		}
	}
	return b.String()
}

// pruneStagingDirs removes timestamped staging dirs older than age
// (best-effort; only sleep's own outputs/<base>/ children).
func pruneStagingDirs(baseDir string, age time.Duration, now func() time.Time) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return
	}
	cutoff := now().UTC().Add(-age)
	for _, e := range entries {
		if !e.IsDir() || len(e.Name()) < 8 {
			continue
		}
		if ts, err := time.Parse("20060102-150405", e.Name()[:15]); err == nil {
			if ts.Before(cutoff) {
				_ = os.RemoveAll(filepath.Join(baseDir, e.Name()))
			}
		}
	}
}
