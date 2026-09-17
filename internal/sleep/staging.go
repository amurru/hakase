// staging.go - staged proposals and verified adopt for markdown-skill
// evolution (plan Phase 1, SL-013).
//
// Consolidation never touches the live skill: accepted candidates land in
// outputs/sleep/<ts>/ with their evidence, pinned to the live file's hash
// and realpath. AdoptStaging re-verifies both pins fail-closed before
// installing (symlink swaps and concurrent hand-edits abort the adopt).
package sleep

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"amurru/hakase/internal/skill"
)

// LoadMarkdownTasks reads a --tasks file in either shape:
// {"tasks": [...]} or a bare [...] array.
func LoadMarkdownTasks(path string) ([]skill.MarkdownTask, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wrapped skill.MarkdownTaskFile
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Tasks != nil {
		return wrapped.Tasks, nil
	}
	var bare []skill.MarkdownTask
	if err := json.Unmarshal(data, &bare); err != nil {
		return nil, fmt.Errorf("tasks file %s is neither {\"tasks\":[...]} nor [...]: %w", path, err)
	}
	return bare, nil
}

// AdoptMeta pins the staged proposal to the live file it was derived from.
type AdoptMeta struct {
	SkillName    string `json:"skill_name"`
	LivePath     string `json:"live_path"`
	LiveSHA256   string `json:"live_sha256"`
	LiveRealpath string `json:"live_realpath"`
	Accepted     bool   `json:"accepted"`
	GateAction   string `json:"gate_action"`
	StagedAt     string `json:"staged_at"`
}

// sha256Hex returns the hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// RenderSleepReport renders the human-readable night report.
func RenderSleepReport(skillName string, result ConsolidationResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Skill evolution: %s\n\n", skillName))
	b.WriteString(fmt.Sprintf("- held-out score: %.3f -> %.3f\n", result.BaselineScore, result.CandidateScore))
	b.WriteString(fmt.Sprintf("- gate: **%s** (accepted=%v)\n", result.GateAction, result.Accepted))
	if result.HoldoutLeaked {
		b.WriteString("- **Not validated.** The validation slice was not disjoint from training.\n")
	}
	if result.NoEditsReason != "" {
		b.WriteString(fmt.Sprintf("- note: %s\n", result.NoEditsReason))
	}
	b.WriteString("\n")
	if len(result.Deltas) > 0 {
		b.WriteString("## Held-out task changes\n\n")
		b.WriteString("| Task | Baseline | Candidate | Change |\n|---|---|---:|---:|---|\n")
		for _, d := range result.Deltas {
			b.WriteString(fmt.Sprintf("| `%s` | %.3f | %.3f | %s |\n",
				d.TaskID, d.BaselineScore, d.CandidateScore, d.Status()))
		}
		b.WriteString("\n")
	}
	if len(result.Applied) > 0 {
		b.WriteString("## Accepted edits\n\n")
		for _, e := range result.Applied {
			b.WriteString(fmt.Sprintf("- [%s] %s\n  _why: %s_\n", e.Op, e.Content, e.Rationale))
		}
		b.WriteString("\n")
	}
	if len(result.Rejected) > 0 {
		b.WriteString("## Rejected by gate (kept as negative feedback)\n\n")
		for _, e := range result.Rejected {
			b.WriteString(fmt.Sprintf("- [%s] %s\n", e.Op, e.Content))
		}
		b.WriteString("\n")
	}
	if len(result.RankingDetails) > 0 {
		b.WriteString(fmt.Sprintf("## Edit ranking (pool exceeded the learning rate; %d clipped)\n\n", result.ClippedEdits))
		b.WriteString("| # | Op | Selected | Note |\n|---|---|---|---|\n")
		for _, d := range result.RankingDetails {
			note := d.Reason
			if d.Selected {
				note = fmt.Sprintf("kept (rank score %.0f)", d.Score)
			}
			b.WriteString(fmt.Sprintf("| %d | %s | %v | %s |\n", d.Index, d.Op, d.Selected, note))
		}
		b.WriteString("\n")
	}
	if result.LapseBypassed {
		if result.Accepted {
			b.WriteString(fmt.Sprintf("## Execution reminders (bypassed the gate by design, plan SL-032)\n\n%d EXECUTION_LAPSE reminder(s) landed in the protected appendix without validation:\n\n", len(result.LapseReminders)))
			for _, e := range result.LapseReminders {
				content := e.Content
				if content == "" {
					content = e.Rationale
				}
				b.WriteString("- " + content + "\n")
			}
			b.WriteString("\n")
		} else {
			b.WriteString(fmt.Sprintf("_%d execution-lapse reminder(s) bypassed gate scoring but were dropped with the rejected candidate; they ship only with an accepted proposal._\n\n", len(result.LapseReminders)))
		}
	}
	if result.NoiseRange && result.Accepted {
		b.WriteString("_Noise-range win: the single-seed delta is under 1.5 points or the validation slice has fewer than 20 tasks. Treat as unproven until an evalkit A/B carries a confidence interval (plan SL-034)._")
		if result.GateAction == "accept_lapse_only" {
			b.WriteString(" (appendix-only night: no learned-guidance change was scored.)")
		}
		b.WriteString("\n\n")
	}
	if len(result.Unmatched) > 0 {
		b.WriteString("## Proposed but changed nothing (never reached the gate)\n\n")
		for _, e := range result.Unmatched {
			anchor := ""
			if e.Anchor != "" {
				anchor = fmt.Sprintf(" (anchor: `%s`)", e.Anchor)
			}
			b.WriteString(fmt.Sprintf("- [%s] %s%s\n", e.Op, e.Content, anchor))
		}
		b.WriteString("\n")
	}
	if result.CallError != "" {
		redacted, _ := RedactSecrets(result.CallError)
		b.WriteString(fmt.Sprintf("_Backend note: %s_\n", redacted))
	}
	return b.String()
}

// StageConsolidation writes the staging directory for one consolidation:
// report.md, report.json, diagnostics.json, proposed_SKILL.md (accepted
// only), and adopt.json with the live-file pins. File layout and modes
// follow SL-005 (0700 dirs, 0600 files). It returns the staging dir.
func StageConsolidation(baseDir, skillName, livePath string, result ConsolidationResult) (string, error) {
	ts := time.Now().UTC().Format("20060102-150405")
	dir := filepath.Join(baseDir, ts)
	for i := 1; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(baseDir, fmt.Sprintf("%s-%d", ts, i))
	}
	return dir, StageConsolidationInto(dir, skillName, livePath, result)
}

// StageConsolidationInto stages one consolidation result into an explicit
// directory (the cycle uses one night dir with a per-skill subdir). The
// directory is created if missing; all checks and modes match
// StageConsolidation.
func StageConsolidationInto(dir, skillName, livePath string, result ConsolidationResult) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	liveBytes, err := os.ReadFile(livePath)
	if err != nil {
		return fmt.Errorf("read live skill: %w", err)
	}
	liveRealpath, err := filepath.EvalSymlinks(livePath)
	if err != nil {
		return fmt.Errorf("resolve live skill: %w", err)
	}
	meta := AdoptMeta{
		SkillName: skillName, LivePath: livePath,
		LiveSHA256: sha256Hex(liveBytes), LiveRealpath: liveRealpath,
		Accepted: result.Accepted, GateAction: result.GateAction,
		StagedAt: time.Now().UTC().Format(time.RFC3339),
	}

	writeStaged := func(name string, data []byte) error {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return err
		}
		_ = os.Chmod(p, 0o600)
		return nil
	}
	if err := writeStaged("report.md", []byte(RenderSleepReport(skillName, result))); err != nil {
		return err
	}
	reportJSON, err := json.MarshalIndent(map[string]any{
		"skill_name": skillName,
		"staged_at":  meta.StagedAt,
		"result":     result,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeStaged("report.json", reportJSON); err != nil {
		return err
	}
	reflectHead := result.ReflectRaw
	if len(reflectHead) > 1200 {
		reflectHead = reflectHead[:1200]
	}
	reflectHead, _ = RedactSecrets(reflectHead)
	callErr, _ := RedactSecrets(result.CallError)
	diagnostics, err := json.MarshalIndent(map[string]any{
		"skill_name":       skillName,
		"gate_action":      result.GateAction,
		"accepted":         result.Accepted,
		"baseline_score":   result.BaselineScore,
		"candidate_score":  result.CandidateScore,
		"holdout_leaked":   result.HoldoutLeaked,
		"n_applied":        len(result.Applied),
		"n_rejected":       len(result.Rejected),
		"n_unmatched":      len(result.Unmatched),
		"n_clipped":        result.ClippedEdits,
		"no_edits_reason":  result.NoEditsReason,
		"call_error":       callErr,
		"reflect_raw_head": reflectHead,
		"deltas":           result.Deltas,
		"holdout_detail":   result.HoldoutDetail,
		"ranking_details":  result.RankingDetails,
		"lapse_bypassed":   result.LapseBypassed,
		"noise_range":      result.NoiseRange,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeStaged("diagnostics.json", diagnostics); err != nil {
		return err
	}
	if result.Accepted {
		if err := writeStaged("proposed_SKILL.md", []byte(result.NewSkill)); err != nil {
			return err
		}
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := writeStaged("adopt.json", metaJSON); err != nil {
		return err
	}
	return nil
}

// AdoptStaging verifies a staged proposal against the live file and
// installs it. Every check fails closed: hash mismatch (concurrent
// hand-edit), realpath mismatch (symlink swap), unaccepted staging, and
// frontmatter drift all abort with the live file untouched. The incumbent
// is preserved as <path>.bak.
func AdoptStaging(stagingDir string) (string, error) {
	metaBytes, err := os.ReadFile(filepath.Join(stagingDir, "adopt.json"))
	if err != nil {
		return "", fmt.Errorf("read adopt.json: %w", err)
	}
	var meta AdoptMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return "", fmt.Errorf("parse adopt.json: %w", err)
	}
	if !meta.Accepted {
		return "", fmt.Errorf("nothing accepted to adopt (gate: %s)", meta.GateAction)
	}
	// Fail closed on disabled skills (plan SL-013/M6): a skill disabled
	// after staging must not adopt, and corrupt state aborts (unlike the
	// lenient live-path check).
	if err := skill.CheckSkillEnabled(skill.KindMarkdown, meta.SkillName); err != nil {
		return "", fmt.Errorf("adopt blocked: %w", err)
	}
	if meta.LivePath == "" || meta.LiveSHA256 == "" || meta.LiveRealpath == "" {
		return "", fmt.Errorf("adopt.json missing live-file pins")
	}
	liveBytes, err := os.ReadFile(meta.LivePath)
	if err != nil {
		return "", fmt.Errorf("read live skill: %w", err)
	}
	if sha256Hex(liveBytes) != meta.LiveSHA256 {
		return "", fmt.Errorf("live skill changed since staging (hash mismatch): review and re-run")
	}
	liveRealpath, err := filepath.EvalSymlinks(meta.LivePath)
	if err != nil {
		return "", fmt.Errorf("resolve live skill: %w", err)
	}
	if liveRealpath != meta.LiveRealpath {
		return "", fmt.Errorf("live skill path changed since staging (symlink swap?): review and re-run")
	}
	proposed, err := os.ReadFile(filepath.Join(stagingDir, "proposed_SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("read proposal: %w", err)
	}
	if err := skill.CheckFrontmatterFrozen(string(liveBytes), string(proposed)); err != nil {
		return "", fmt.Errorf("proposal rejected: %w", err)
	}
	// Full skill validation through a temp file in the live dir (the
	// parser checks directory-name agreement, so the temp must sit next
	// to the live file, not in os.TempDir).
	tmp, err := os.CreateTemp(filepath.Dir(meta.LivePath), ".adopt-*.md")
	if err != nil {
		return "", fmt.Errorf("stage proposal: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(proposed); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("stage proposal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("stage proposal: %w", err)
	}
	if _, err := skill.ParseMarkdownSkill(tmpName); err != nil {
		return "", fmt.Errorf("proposal failed skill validation: %w", err)
	}
	bakPath := meta.LivePath + ".bak"
	if err := os.WriteFile(bakPath, liveBytes, 0o600); err != nil {
		return "", fmt.Errorf("write .bak: %w", err)
	}
	_ = os.Chmod(bakPath, 0o600)
	if err := os.Rename(tmpName, meta.LivePath); err != nil {
		return "", fmt.Errorf("install proposal: %w", err)
	}
	_ = os.Chmod(meta.LivePath, 0o600)
	return meta.LivePath, nil
}
