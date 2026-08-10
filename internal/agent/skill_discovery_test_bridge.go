package agent

import (
	"amurru/hakase/internal/config"
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

// findProjectRoot walks from cwd upward and returns the first directory
// containing a ".git" entry (file or directory, checked via os.Stat).
func findProjectRoot(cwd string) string {
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

// discoverMarkdownSkillsForTestImpl is the test implementation of DiscoverMarkdownSkillsForTest.
// This is a simplified version that works for testing purposes.
func discoverMarkdownSkillsForTestImpl(cwd string, extraDirs []string, log LogFunc) []MarkdownSkill {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	root := findProjectRoot(abs)

	skills := make([]MarkdownSkill, 0)
	seen := make(map[string]string)

	add := func(s MarkdownSkill) {
		if _, dup := seen[s.Frontmatter.Name]; dup {
			return
		}
		seen[s.Frontmatter.Name] = s.Path
		skills = append(skills, s)
	}

	var candidates []string
	addCandidate := func(dir string) {
		if dir == "" {
			return
		}
		st, err := os.Stat(dir)
		if err != nil {
			return
		}
		if st.IsDir() {
			candidates = append(candidates, dir)
		}
	}

	// Standard walk
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

	// Project library
	addCandidate(filepath.Join(root, "skills"))

	// Extra dirs
	for _, dir := range extraDirs {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		addCandidate(dir)
	}

	// User level
	if home := config.HakaseHome(); home != "" {
		addCandidate(filepath.Join(home, "skills"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		addCandidate(filepath.Join(home, ".agents", "skills"))
		addCandidate(filepath.Join(home, ".claude", "skills"))
		addCandidate(filepath.Join(home, ".gemini", "skills"))
	}

	// Scan candidates
	for _, dir := range candidates {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			subdir := filepath.Join(dir, entry.Name())
			isDir := entry.IsDir()
			if !isDir && entry.Type()&os.ModeSymlink != 0 {
				st, err := os.Stat(subdir)
				if err != nil {
					continue
				}
				isDir = st.IsDir()
			}
			if !isDir {
				continue
			}
			skillPath := filepath.Join(subdir, "SKILL.md")
			if _, err := os.Stat(skillPath); err != nil {
				continue
			}
			// Parse the skill (simplified for tests)
			data, err := os.ReadFile(skillPath)
			if err != nil {
				continue
			}
			content := string(data)
			// Simple frontmatter parsing
			if len(content) < 3 || content[:3] != "---" {
				continue
			}
			endIdx := -1
			for i := 4; i < len(content); i++ {
				if i+3 <= len(content) && content[i:i+3] == "---" {
					endIdx = i
					break
				}
			}
			if endIdx == -1 {
				continue
			}
			frontmatter := content[4:endIdx]
			body := ""
			if endIdx+3 < len(content) {
				body = content[endIdx+3:]
			}
			// Parse name and description from frontmatter
			var name, description string
			for _, line := range splitLines(frontmatter) {
				if len(line) > 6 && line[:6] == "name: " {
					name = line[6:]
				} else if len(line) > 13 && line[:13] == "description: " {
					description = line[13:]
				}
			}
			if name == "" {
				continue
			}
			skill := MarkdownSkill{
				Frontmatter: MarkdownSkillFrontmatter{
					Name:        name,
					Description: description,
				},
				Path:    skillPath,
				Dir:     subdir,
				Body:    body,
				Source:  dir,
				Scripts: []string{},
			}
			// Check for scripts
			scriptsDir := filepath.Join(subdir, "scripts")
			if st, err := os.Stat(scriptsDir); err == nil && st.IsDir() {
				entries, err := os.ReadDir(scriptsDir)
				if err == nil {
					for _, entry := range entries {
						if !entry.IsDir() {
							skill.Scripts = append(skill.Scripts, "scripts/"+entry.Name())
						}
					}
				}
			}
			add(skill)
		}
	}

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Frontmatter.Name < skills[j].Frontmatter.Name
	})
	return skills
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(s) {
		line := s[start:]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		lines = append(lines, line)
	}
	return lines
}

func init() {
	// Initialize the test bridge function
	DiscoverMarkdownSkillsForTest = discoverMarkdownSkillsForTestImpl
}
