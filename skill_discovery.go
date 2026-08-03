// skill_discovery.go - discovery of markdown skills (SKILL.md) from project
// and user-level directories.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// standardSkillDirs are the per-tool skill directories checked at each level
// of the walk from cwd up to the project root, in priority order.
var standardSkillDirs = []string{
	".agents/skills",
	".claude/skills",
	".opencode/skills",
	".gemini/skills",
}

// FindProjectRoot walks from cwd upward and returns the first directory
// containing a ".git" entry (file or directory, checked via os.Stat). If no
// ".git" is found before reaching the filesystem root, cwd is returned
// (documented fallback; no silent guess).
func FindProjectRoot(cwd string) string {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return cwd
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

// DiscoverMarkdownSkills scans project and user-level skill directories for
// SKILL.md files and returns the parsed skills, sorted by name. Candidate
// directories are processed in priority order (standard walk, project
// library, extraDirs, user level) and duplicate skill names are resolved by
// first match wins. Invalid skills are skipped with a logged warning and
// never fail the discovery. The returned slice is never nil.
func DiscoverMarkdownSkills(cwd string, extraDirs []string, log LogFunc) []MarkdownSkill {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	root := FindProjectRoot(abs)

	skills := make([]MarkdownSkill, 0)
	seen := make(map[string]string)
	duplicateCount := 0

	add := func(s MarkdownSkill) {
		if _, dup := seen[s.Frontmatter.Name]; dup {
			duplicateCount++
			return
		}
		if len(s.Body) > 100*1024 {
			if log != nil {
				log(fmt.Sprintf("[skills] Markdown skill '%s' has a large body (>100KB)", s.Frontmatter.Name))
			}
		}
		seen[s.Frontmatter.Name] = s.Path
		skills = append(skills, s)
	}

	// Build the candidate skill dirs in priority order. Non-existent dirs
	// are skipped silently (no log noise); real Stat errors are logged.
	var candidates []string
	addCandidate := func(dir string) {
		if dir == "" {
			return
		}
		st, err := os.Stat(dir)
		if err != nil {
			if !os.IsNotExist(err) && log != nil {
				log(fmt.Sprintf("[skills] Cannot stat candidate dir %s: %v", dir, err))
			}
			return
		}
		if st.IsDir() {
			candidates = append(candidates, dir)
		}
	}

	// a) Standard walk: every directory from cwd up to root INCLUSIVE,
	// nearest first, each checked for the standard per-tool skill dirs.
	for d := abs; ; {
		for _, sub := range standardSkillDirs {
			addCandidate(filepath.Join(d, sub))
		}
		if d == root {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}

	// b) Project library: <root>/skills.
	addCandidate(filepath.Join(root, "skills"))

	// c) Extra dirs, resolved against root when relative.
	for _, dir := range extraDirs {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		addCandidate(dir)
	}

	// d) User level.
	if home, err := os.UserHomeDir(); err == nil {
		addCandidate(filepath.Join(home, ".agents", "skills"))
		addCandidate(filepath.Join(home, ".claude", "skills"))
		addCandidate(filepath.Join(home, ".gemini", "skills"))
		opencodeDir := filepath.Join(home, ".config", "opencode", "skills")
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			opencodeDir = filepath.Join(xdg, "opencode", "skills")
		}
		addCandidate(opencodeDir)
	}

	// Scan each candidate dir: one level of nesting, entries that are
	// directories (following symlinked skill dirs via os.Stat, no
	// recursion) with a SKILL.md file.
	for _, dir := range candidates {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if log != nil {
				log(fmt.Sprintf("[skills] Cannot read skill directory %s: %v", dir, err))
			}
			continue
		}
		for _, entry := range entries {
			subdir := filepath.Join(dir, entry.Name())
			isDir := entry.IsDir()
			if !isDir && entry.Type()&os.ModeSymlink != 0 {
				st, err := os.Stat(subdir)
				if err != nil {
					if log != nil {
						log(fmt.Sprintf("[skills] Cannot stat %s: %v", subdir, err))
					}
					continue
				}
				isDir = st.IsDir()
			}
			if !isDir {
				continue
			}
			skillPath := filepath.Join(subdir, "SKILL.md")
			if _, err := os.Stat(skillPath); err != nil {
				if !os.IsNotExist(err) && log != nil {
					log(fmt.Sprintf("[skills] Cannot stat %s: %v", skillPath, err))
				}
				continue
			}
			skill, err := ParseMarkdownSkill(skillPath)
			if err != nil {
				if log != nil {
					log(fmt.Sprintf("[skills] Skipping invalid markdown skill at %s: %v", skillPath, err))
				}
				continue
			}
			skill.Source = dir
			add(*skill)
		}
	}

	if duplicateCount > 0 && log != nil {
		log(fmt.Sprintf("[skills] Discovered %d skills, skipped %d duplicate(s)", len(skills), duplicateCount))
	}

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Frontmatter.Name < skills[j].Frontmatter.Name
	})
	return skills
}
