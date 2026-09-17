// mine.go - task miner for the SkillOpt-Sleep loop (plan Phase 2, SL-021):
// turn harvested session digests into replayable MarkdownTasks with a
// deterministic train/val/test split.
//
// The heuristic miner (default) clusters turns by skills_used into
// skill_hint buckets (managed catch-all otherwise), classifies outcomes
// from feedback/error/retry/lessons-learned co-occurrence, derives exact
// checks only where derivable (everything else is rubric + needs_review),
// and applies the nightly caps. The LLM miner is a function-injected hook
// (default off) that receives the same already-redacted digests; its
// output passes through the identical split/caps/quarantine pipeline.
package sleep

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strings"

	"amurru/hakase/internal/skill"
)

// Mining caps and defaults (plan SL-021).
const (
	DefaultMaxTasksPerNight = 40
	DefaultTrainFraction    = 0.6
	DefaultValFraction      = 0.2
	DefaultTestFraction     = 0.0 // legacy behavior: no test slice
	DefaultSplitSeed        = 42
	// ManagedCatchAll is the skill_hint for turns with no load_markdown_skill
	// affinity: the cycle groups these under the managed-skills pass.
	ManagedCatchAll = "managed"
)

// MineOpts tunes one mining pass.
type MineOpts struct {
	// MaxTasksPerNight caps emitted tasks (0 = 40).
	MaxTasksPerNight int
	// Split fractions must sum to 1 (±epsilon). TestFraction 0 keeps the
	// legacy two-slice behavior. Defaults 0.6/0.2/0.
	TrainFraction, ValFraction, TestFraction float64
	// Seed drives the deterministic split shuffle (0 = 42).
	Seed int64
	// LLMMiner, when set, replaces the heuristic extractor. It receives the
	// harvested digests (already redacted and truncated at harvest time).
	// Its tasks still flow through split/caps/quarantine below.
	LLMMiner func(digests []SessionDigest) ([]skill.MarkdownTask, error)
}

// MineResult carries the emitted tasks plus the audit trail the report
// surfaces (quarantine and split decisions must be logged, not silent).
type MineResult struct {
	Tasks []skill.MarkdownTask
	// Log lines: origin quarantine, split shape, cap hits.
	Log []string
}

// outcome classes recorded as tags.
const (
	outcomeFail    = "outcome:fail"
	outcomeSuccess = "outcome:success"
	outcomeMixed   = "outcome:mixed"
	needsReviewTag = "needs_review"
)

// minedTurn is one candidate task before split assignment.
type minedTurn struct {
	task    skill.MarkdownTask
	outcome string // fail | success | mixed | ""
	weight  int    // ordering priority (failures first)
}

var (
	// finalErrorRe marks error-shaped agent finals.
	finalErrorRe = regexp.MustCompile(`(?i)\b(error|failed|failure|unable to|cannot|traceback|exception)\b`)
	// retryRe marks a follow-up prompt as a retry of the previous turn.
	retryRe = regexp.MustCompile(`(?i)\b(try again|retry|still|again,?|redo|start over|didn'?t work|nope|no,)\b`)
	// lessonsRe marks lessons-learned co-occurrence (reflexion hook output).
	lessonsRe = regexp.MustCompile(`(?i)lessons?-learned`)
	// yesNoPromptRe detects checkable yes/no prompts for exact-reference
	// derivation; yesNoAnswerRe reads the verdict off the final.
	yesNoPromptRe  = regexp.MustCompile(`(?i)\b(did|does|is|are|was|can|could)\b[\s\S]{0,200}\?`)
	yesNoAnswerRe  = regexp.MustCompile(`(?i)^\W*(yes|no)\b`)
	positiveSignal = map[string]bool{"perfect": true, "works": true, "thanks": true, "praise": true}
	negativeSignal = map[string]bool{"wrong": true, "incorrect": true, "not-what-i-asked": true, "thats-not": true,
		"didnt-work": true, "still-failing": true, "revert": true, "try-again": true, "not-right": true, "broken": true}
)

// Mine turns digests into capped, split-assigned tasks. Deterministic: the
// same digests and opts always produce the same output (split shuffle is
// seeded; ordering is total).
func Mine(digests []SessionDigest, opts MineOpts) MineResult {
	maxTasks := opts.MaxTasksPerNight
	if maxTasks <= 0 {
		maxTasks = DefaultMaxTasksPerNight
	}
	seed := opts.Seed
	if seed == 0 {
		seed = DefaultSplitSeed
	}

	var turns []minedTurn
	if opts.LLMMiner != nil {
		llmTasks, err := opts.LLMMiner(digests)
		res := MineResult{}
		if err != nil {
			res.Log = append(res.Log, "llm miner failed: "+err.Error())
			return res
		}
		for _, t := range llmTasks {
			// LLM-proposed tasks carry no trustworthy outcome class; keep
			// recency-neutral ordering and let the split pipeline do the rest.
			turns = append(turns, minedTurn{task: t, weight: 1})
		}
	} else {
		turns = heuristicTurns(digests)
	}
	return finalizeTasks(turns, maxTasks, opts.TrainFraction, opts.ValFraction, opts.TestFraction, seed)
}

// heuristicTurns extracts one candidate task per harvested turn.
func heuristicTurns(digests []SessionDigest) []minedTurn {
	var turns []minedTurn
	for _, d := range digests {
		for i, prompt := range d.Prompts {
			t := minedTurn{task: skill.MarkdownTask{
				ID:        minedTaskID(d.SessionID, i, prompt),
				Intent:    prompt,
				SkillHint: skillHint(d),
				Origin:    "real",
			}}
			if i < len(d.Finals) {
				t.task.ContextExcerpt = d.Finals[i]
			}
			var neg, pos bool
			for _, sig := range d.FeedbackSignals {
				if negativeSignal[sig] {
					neg = true
				}
				if positiveSignal[sig] {
					pos = true
				}
			}
			final := t.task.ContextExcerpt
			switch {
			case finalErrorRe.MatchString(final) || lessonsRe.MatchString(final):
				neg = true
			}
			// Retry co-occurrence: the NEXT prompt asks to redo this turn.
			if i+1 < len(d.Prompts) && retryRe.MatchString(d.Prompts[i+1]) {
				neg = true
			}
			switch {
			case neg && pos:
				t.outcome = "mixed"
				t.weight = 0
				t.task.Tags = append(t.task.Tags, outcomeMixed)
			case neg:
				t.outcome = "fail"
				t.weight = 0
				t.task.Tags = append(t.task.Tags, outcomeFail)
			case pos:
				t.outcome = "success"
				t.weight = 2
				t.task.Tags = append(t.task.Tags, outcomeSuccess)
			default:
				t.weight = 1
			}
			deriveCheck(&t.task, prompt, final)
			turns = append(turns, t)
		}
	}
	return turns
}

// skillHint clusters a digest by its skills_used: the first (sorted) entry,
// else the managed catch-all.
func skillHint(d SessionDigest) string {
	if len(d.SkillsUsed) > 0 && d.SkillsUsed[0] != "" {
		return d.SkillsUsed[0]
	}
	return ManagedCatchAll
}

// deriveCheck attaches a judge contract: exact yes/no checks where
// derivable, rubric + needs_review everywhere else (heuristic miner never
// invents references).
func deriveCheck(t *skill.MarkdownTask, prompt, final string) {
	if yesNoPromptRe.MatchString(prompt) && final != "" {
		if m := yesNoAnswerRe.FindStringSubmatch(final); m != nil {
			t.ReferenceKind = "exact"
			t.Reference = strings.ToLower(m[1])
			return
		}
	}
	t.ReferenceKind = "rubric"
	t.Tags = append(t.Tags, needsReviewTag)
}

// minedTaskID derives a stable task ID from the session, turn index and
// prompt: re-harvesting the same turn yields the same ID (disjointness and
// dedup depend on it), different turns never collide.
func minedTaskID(sessionID string, turnIdx int, prompt string) string {
	sum := sha256.Sum256([]byte(sessionID + "|" + fmt.Sprint(turnIdx) + "|" + prompt))
	return "m-" + hex.EncodeToString(sum[:])[:12]
}

// finalizeTasks applies priority+recency ordering, the nightly cap, the
// seeded split, and origin quarantine; every non-silent decision is logged.
func finalizeTasks(turns []minedTurn, maxTasks int, trainF, valF, testF float64, seed int64) MineResult {
	res := MineResult{}

	// Failure-shaped turns carry the learning signal; successes only anchor
	// the reflector. Order: fail/mixed, then unknown, then success; within
	// a class, newest session first (digests arrive newest-first from the
	// harvester, so a stable sort preserves recency inside each class).
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].weight < turns[j].weight })

	if len(turns) > maxTasks {
		res.Log = append(res.Log, fmt.Sprintf("cap: kept %d of %d candidate turns (max_tasks_per_night)", maxTasks, len(turns)))
		turns = turns[:maxTasks]
	}

	// Origin quarantine before the split: dream-origin tasks validate on
	// synthetic data, so they train only (plan SL-010/SL-021).
	quarantined := 0
	for i := range turns {
		origin := strings.ToLower(strings.TrimSpace(turns[i].task.Origin))
		if origin == "" {
			turns[i].task.Origin = "real"
		}
	}
	realOnly := turns[:0]
	for _, t := range turns {
		if strings.EqualFold(t.task.Origin, "dream") {
			t.task.Split = "train"
			quarantined++
			res.Tasks = append(res.Tasks, t.task)
			continue
		}
		realOnly = append(realOnly, t)
	}
	if quarantined > 0 {
		res.Log = append(res.Log, fmt.Sprintf("quarantine: %d dream-origin task(s) pinned to train", quarantined))
	}

	// Seeded shuffle over real-origin candidates, then contiguous split
	// assignment: disjoint by construction, reproducible for a given input.
	shuffled := append([]minedTurn(nil), realOnly...)
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	trainF, valF, testF, ok := normalizeFractions(trainF, valF, testF)
	if !ok {
		res.Log = append(res.Log, "split: invalid fractions, fell back to 0.6/0.2/0")
	}
	n := len(shuffled)
	nTest := int(float64(n) * testF)
	nVal := int(float64(n) * valF)
	for i, t := range shuffled {
		switch {
		case i < nTest:
			t.task.Split = "test"
		case i < nTest+nVal:
			t.task.Split = "val"
		default:
			t.task.Split = "train"
		}
		res.Tasks = append(res.Tasks, t.task)
	}
	res.Log = append(res.Log, fmt.Sprintf("split: %d real-origin tasks -> %.2f/%.2f/%.2f (seed %d)", n, trainF, valF, testF, seed))

	// Disjointness is a hard invariant: verify and refuse to ship a file
	// that violates it (defends the LLM-miner path and future refactors).
	if !splitsDisjoint(res.Tasks) {
		res.Log = append(res.Log, "INVARIANT VIOLATION: split disjointness failed")
		res.Tasks = nil
	}
	return res
}

// normalizeFractions validates the split fractions. All-zero opts are the
// documented defaults (valid, no fallback); anything else must sum to one
// or the documented defaults are used with ok=false (caller logs it).
func normalizeFractions(trainF, valF, testF float64) (float64, float64, float64, bool) {
	if trainF == 0 && valF == 0 && testF == 0 {
		return DefaultTrainFraction, DefaultValFraction, DefaultTestFraction, true
	}
	sum := trainF + valF + testF
	if sum < 0.999 || sum > 1.001 || trainF < 0 || valF < 0 || testF < 0 {
		return DefaultTrainFraction, DefaultValFraction, DefaultTestFraction, false
	}
	return trainF, valF, testF, true
}

// splitsDisjoint asserts every task appears in exactly one split and that
// no test task is also train/val (disjoint-by-ID, plan SL-021).
func splitsDisjoint(tasks []skill.MarkdownTask) bool {
	seen := make(map[string]string, len(tasks))
	for _, t := range tasks {
		if t.ID == "" {
			return false
		}
		if prev, ok := seen[t.ID]; ok && prev != t.Split {
			return false
		}
		seen[t.ID] = t.Split
	}
	return true
}

// MineTasksFile is the on-disk shape of a mined task file: the machine
// marker (generated_at) is what triggers the M4 review requirement.
type MineTasksFile struct {
	GeneratedAt string                `json:"generated_at"`
	Tasks       []skill.MarkdownTask `json:"tasks"`
}

// WriteMineTasks writes the mined tasks with the machine-generation marker
// (0600, atomic). The marker is normative: files carrying it are refused by
// real-backend consumers until `sleep review` signs them.
func WriteMineTasks(path string, tasks []skill.MarkdownTask, generatedAt string) error {
	blob, err := json.MarshalIndent(MineTasksFile{GeneratedAt: generatedAt, Tasks: tasks}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, blob, 0o600)
}
