package skill

import (
	"amurru/hakase/internal/config"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// InstallOptions parameters for skill installation.
type InstallOptions struct {
	Target string // owner/repo, URL, or local path
	Skill  string // optional skill name filter
	Global bool   // install into ~/.hakase/skills instead of project
	Yes    bool   // skip confirmation
}

// InstallSkill installs a skill into discovery directories.
func InstallSkill(opts InstallOptions) (string, error) {
	if opts.Target == "" {
		return "", fmt.Errorf("target is required")
	}

	var destParent string
	if opts.Global {
		home := config.HakaseHome()
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("getting user home dir: %w", err)
			}
			home = filepath.Join(home, ".hakase")
		}
		destParent = filepath.Join(home, "skills")
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting cwd: %w", err)
		}
		root := FindProjectRoot(cwd)
		destParent = filepath.Join(root, ".agents", "skills")
	}

	if err := os.MkdirAll(destParent, 0o755); err != nil {
		return "", fmt.Errorf("creating skill directory %s: %w", destParent, err)
	}

	// 1. Check if local directory or file
	if st, err := os.Stat(opts.Target); err == nil {
		skillName := filepath.Base(opts.Target)
		if opts.Skill != "" {
			skillName = opts.Skill
		}

		targetDir := filepath.Join(destParent, skillName)
		if st.IsDir() {
			if err := copyDir(opts.Target, targetDir); err != nil {
				return "", fmt.Errorf("copying skill dir: %w", err)
			}
			return targetDir, nil
		}

		// Single SKILL.md file
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return "", err
		}
		destFile := filepath.Join(targetDir, "SKILL.md")
		if err := copyFile(opts.Target, destFile); err != nil {
			return "", err
		}
		return targetDir, nil
	}

	// 2. HTTP URL
	if strings.HasPrefix(opts.Target, "http://") || strings.HasPrefix(opts.Target, "https://") {
		skillName := opts.Skill
		if skillName == "" {
			skillName = filepath.Base(opts.Target)
			skillName = strings.TrimSuffix(skillName, ".md")
			skillName = strings.TrimSuffix(skillName, "SKILL")
			if skillName == "" {
				skillName = "downloaded_skill"
			}
		}

		targetDir := filepath.Join(destParent, skillName)
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return "", err
		}

		resp, err := http.Get(opts.Target)
		if err != nil {
			return "", fmt.Errorf("fetching skill URL: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP error fetching skill: %s", resp.Status)
		}

		destFile := filepath.Join(targetDir, "SKILL.md")
		out, err := os.Create(destFile)
		if err != nil {
			return "", err
		}
		defer out.Close()

		_, err = io.Copy(out, resp.Body)
		if err != nil {
			return "", err
		}
		return targetDir, nil
	}

	// 3. owner/repo format shorthand
	parts := strings.Split(opts.Target, "/")
	if len(parts) == 2 {
		rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/SKILL.md", parts[0], parts[1])
		opts.Target = rawURL
		return InstallSkill(opts)
	}

	return "", fmt.Errorf("unsupported skill target format %q", opts.Target)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}
		return copyFile(path, targetPath)
	})
}
