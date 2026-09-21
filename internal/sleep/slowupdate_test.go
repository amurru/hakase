// slowupdate_test.go - SL-031 acceptance: trend classification, guidance,
// and the optimizer-memory sidecar (never part of the skill document).
package sleep

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func rec(base float64, cand float64, accepted bool, at time.Time) NightGroupRecord {
	return NightGroupRecord{SkillName: "demo", StartedAt: at, BaselineScore: base, CandidateScore: cand, Accepted: accepted, Consolidated: true}
}

func TestClassifyEpochTrend(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name    string
		records []NightGroupRecord
		want    string
	}{
		{"no history", nil, ""},
		{"one night", []NightGroupRecord{rec(0, 0.5, true, now)}, ""},
		{"improved", []NightGroupRecord{rec(0, 0, false, now), rec(0, 0.5, true, now.Add(time.Hour))}, "improved"},
		{"regressed", []NightGroupRecord{rec(0, 0.5, true, now), rec(0, 0.2, true, now.Add(time.Hour))}, "regressed"},
		{"persistent fail", []NightGroupRecord{rec(0, 0, false, now), rec(0, 0, false, now.Add(time.Hour)), rec(0, 0, false, now.Add(2*time.Hour))}, "persistent_fail"},
		{"stable success", []NightGroupRecord{rec(1, 1, true, now), rec(1, 1, true, now.Add(time.Hour))}, "stable_success"},
		{"flat is no trend", []NightGroupRecord{rec(0, 0.5, true, now), rec(0, 0.5, true, now.Add(time.Hour))}, ""},
		{"dead groups excluded", []NightGroupRecord{
			{SkillName: "demo", Consolidated: false}, {SkillName: "demo", Consolidated: false}}, ""},
		{"failed candidate counts baseline", []NightGroupRecord{
			rec(0.7, 0.9, false, now), rec(0.3, 0.5, false, now.Add(time.Hour))}, "regressed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyEpochTrend(tc.records); got != tc.want {
				t.Errorf("ClassifyEpochTrend = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTrendGuidance(t *testing.T) {
	for _, trend := range []string{"improved", "regressed", "persistent_fail", "stable_success"} {
		if len(TrendGuidance(trend)) == 0 {
			t.Errorf("trend %q must render guidance", trend)
		}
	}
	if TrendGuidance("") != nil || TrendGuidance("bogus") != nil {
		t.Error("unknown trends render no guidance")
	}
}

func TestOptimizerMemorySidecarRoundtrip(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(live, []byte("---\nname: demo\ndescription: d.\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No sidecar yet: memory reads empty.
	if got := ReadOptimizerMemory(live); got != "" {
		t.Errorf("missing sidecar must read empty, got %q", got)
	}
	now := time.Now().UTC()
	records := []NightGroupRecord{
		rec(0, 0, false, now.Add(-2*time.Hour)),
		rec(0, 0.5, true, now.Add(-time.Hour)),
		rec(0.5, 0.7, true, now),
	}
	trend := ClassifyEpochTrend(records)
	if trend != "improved" {
		t.Fatalf("trend = %q, want improved", trend)
	}
	if err := WriteOptimizerMemory(live, records, trend); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	sidecar := OptimizerMemoryPath(live)
	if !strings.Contains(sidecar, filepath.Join("references", "optimizer-memory.md")) {
		t.Errorf("sidecar path = %s", sidecar)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(sidecar); err != nil {
			t.Fatal(err)
		} else if info.Mode().Perm() != 0o600 {
			t.Errorf("sidecar mode = %o, want 600", info.Mode().Perm())
		}
		if info, err := os.Stat(filepath.Dir(sidecar)); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("references dir mode = %v err=%v, want 700", info, err)
		}
	}
	mem := ReadOptimizerMemory(live)
	for _, want := range []string{"Optimizer memory", "improved", "Per-night outcomes"} {
		if !strings.Contains(mem, want) {
			t.Errorf("sidecar missing %q: %s", want, mem)
		}
	}
	// Memory never leaks secrets (defense-in-depth) and is head-truncated.
	if len(mem) > OptimizerMemoryHead {
		t.Error("sidecar excerpt must be truncated")
	}
}

func TestSkillHistoryFromState(t *testing.T) {
	now := time.Now().UTC()
	state := SleepState{Nights: []NightRecord{
		{StartedAt: now.Add(-time.Hour), Outcome: "staged", Groups: []NightGroupRecord{
			{SkillName: "demo", Consolidated: true}, {SkillName: "other", Consolidated: true}}},
		{StartedAt: now, Outcome: "empty"},
	}}
	got := skillHistory(state, "DEMO")
	if len(got) != 1 || got[0].SkillName != "demo" {
		t.Errorf("skillHistory = %+v", got)
	}
}

func TestRenderOptimizerMemory(t *testing.T) {
	out := RenderOptimizerMemory([]NightGroupRecord{rec(0.25, 0.75, true, time.Now().UTC())}, "improved")
	for _, want := range []string{"trend: improved", "| 0.250 | 0.750 | yes |", "Never deployed"} {
		if !strings.Contains(out, want) {
			t.Errorf("memory render missing %q:\n%s", want, out)
		}
	}
}
