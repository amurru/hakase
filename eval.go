// eval.go - shared eval-set formats for hakase knowledge bench (Phase 3d-5)
// and the skill evolver (Phase 3b/3c). Both consume input/expected pairs from
// JSON files, so a single set of types backs the bench CLI and the evolution
// loop's per-skill evaluator.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// BenchQuery pairs a search query with the note slugs it should retrieve.
// K overrides the bench-wide --k flag for this query (0 = use the flag).
type BenchQuery struct {
	Query    string   `json:"query"`
	Expected []string `json:"expected"`
	K        int      `json:"k,omitempty"`
}

// BenchSet is the eval-set file for `hakase knowledge bench`: a list of
// queries plus the slugs they must return. Located at <knowledge_dir>/bench.json
// by default (overridable via --eval).
type BenchSet struct {
	Queries []BenchQuery `json:"queries"`
}

// SkillEvalCase is a single input/expected pair for a skill's evaluator.
// The skill's entry function is invoked with Input; its output is compared
// to Expected using Match ("exact" | "contains" | "regex"; default
// "contains"). Train=true cases are visible to the mutator; Train=false are
// holdout cases used only to detect overfitting.
type SkillEvalCase struct {
	Name     string      `json:"name,omitempty"`
	Input    interface{} `json:"input"`
	Expected string      `json:"expected"`
	Match    string      `json:"match,omitempty"`
	Train    bool        `json:"train,omitempty"`
}

// SkillEvalSet is the eval-set file for one skill, located at
// skills/<name>.eval.json. Skills without an eval set are skipped by the
// evolver (they cannot be scored objectively).
type SkillEvalSet struct {
	Cases []SkillEvalCase `json:"cases"`
}

// loadBenchSet reads and validates a bench eval file.
func loadBenchSet(path string) (*BenchSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var set BenchSet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, err
	}
	if len(set.Queries) == 0 {
		return nil, fmt.Errorf("bench set %s has no queries", path)
	}
	for _, q := range set.Queries {
		if q.Query == "" {
			return nil, fmt.Errorf("bench set %s contains an empty query", path)
		}
	}
	return &set, nil
}

// loadSkillEvalSet reads and validates a per-skill eval file.
func loadSkillEvalSet(path string) (*SkillEvalSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var set SkillEvalSet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, err
	}
	if len(set.Cases) == 0 {
		return nil, fmt.Errorf("eval set %s has no cases", path)
	}
	return &set, nil
}
