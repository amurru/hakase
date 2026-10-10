package skill

import (
	"amurru/hakase/internal/config"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// MaxSkillDownloadBytes caps a single fetched SKILL.md (H1).
const MaxSkillDownloadBytes = 2 << 20 // 2 MB

// MaxSkillCopyBytes caps total bytes copied for a local skill install.
const MaxSkillCopyBytes = 20 << 20 // 20 MB

// MaxSkillCopyFiles caps the number of files copied for a local skill install.
const MaxSkillCopyFiles = 2000

var ownerRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(@[A-Za-z0-9_./-]+)?$`)

var skillHTTPClient = &http.Client{Timeout: 30 * time.Second}

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
		if err := ValidateSkillName(normalizeSkillName(skillName)); err != nil {
			return "", fmt.Errorf("invalid skill name %q: %w", skillName, err)
		}
		skillName = normalizeSkillName(skillName)

		targetDir := filepath.Join(destParent, skillName)
		if err := checkDestOverwrite(targetDir, opts.Yes); err != nil {
			return "", err
		}
		if st.IsDir() {
			if err := copyDir(opts.Target, targetDir); err != nil {
				return "", fmt.Errorf("copying skill dir: %w", err)
			}
			if err := validateInstalledSkill(targetDir); err != nil {
				return "", err
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
		if err := validateSkillFile(destFile); err != nil {
			return "", err
		}
		return targetDir, nil
	}

	// 2. HTTP URL
	if strings.HasPrefix(opts.Target, "http://") || strings.HasPrefix(opts.Target, "https://") {
		if err := checkRemoteURL(opts.Target); err != nil {
			return "", err
		}
		skillName := opts.Skill
		if skillName == "" {
			skillName = deriveSkillNameFromURL(opts.Target)
		}
		if err := ValidateSkillName(normalizeSkillName(skillName)); err != nil {
			return "", fmt.Errorf("invalid skill name %q: %w (pass -s with a valid name)", skillName, err)
		}
		skillName = normalizeSkillName(skillName)

		targetDir := filepath.Join(destParent, skillName)
		if err := checkDestOverwrite(targetDir, opts.Yes); err != nil {
			return "", err
		}
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return "", err
		}

		resp, err := skillHTTPClient.Get(opts.Target)
		if err != nil {
			return "", fmt.Errorf("fetching skill URL: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP error fetching skill: %s", resp.Status)
		}

		destFile := filepath.Join(targetDir, "SKILL.md")
		out, err := os.OpenFile(destFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return "", err
		}

		_, err = io.Copy(out, io.LimitReader(resp.Body, MaxSkillDownloadBytes+1))
		closeErr := out.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		if st, err := os.Stat(destFile); err == nil && st.Size() > MaxSkillDownloadBytes {
			_ = os.Remove(destFile)
			return "", fmt.Errorf("skill download exceeds %d bytes, refusing to install", MaxSkillDownloadBytes)
		}
		if err := validateSkillFile(destFile); err != nil {
			return "", err
		}
		return targetDir, nil
	}

	// 3. owner/repo format shorthand (optionally owner/repo@ref)
	if ownerRepoRe.MatchString(opts.Target) && !looksLikePath(opts.Target) {
		owner, repo, ref := splitOwnerRepo(opts.Target)
		branch := ref
		if branch == "" {
			branch = "main"
		}
		rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/SKILL.md", owner, repo, branch)
		opts.Target = rawURL
		return InstallSkill(opts)
	}

	return "", fmt.Errorf("unsupported skill target format %q", opts.Target)
}

// normalizeSkillName maps a user-supplied or URL-derived name onto the
// portable skill-name space (lowercase, hyphens) before validation.
func normalizeSkillName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	return name
}

// deriveSkillNameFromURL builds a skill directory name from a URL path,
// stripping query strings and suffixes that filepath.Base would keep.
func deriveSkillNameFromURL(raw string) string {
	u, err := url.Parse(raw)
	base := ""
	if err == nil && u.Path != "" {
		base = filepath.Base(u.Path)
	} else {
		base = filepath.Base(raw)
	}
	base = strings.TrimSuffix(base, ".md")
	base = strings.TrimSuffix(base, ".MD")
	if strings.EqualFold(base, "skill") || base == "" || base == "." || base == "/" {
		return "downloaded-skill"
	}
	return base
}

// looksLikePath reports whether s looks like a filesystem path rather than
// an owner/repo shorthand (contains a dot-segment, leading dot or slash).
func looksLikePath(s string) bool {
	if strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") {
		return true
	}
	for _, part := range strings.Split(s, "/") {
		if part == "." || part == ".." {
			return true
		}
	}
	return false
}

// splitOwnerRepo splits "owner/repo[@ref]" into its parts.
func splitOwnerRepo(s string) (owner, repo, ref string) {
	parts := strings.SplitN(s, "/", 2)
	owner = parts[0]
	rest := parts[1]
	if i := strings.Index(rest, "@"); i >= 0 {
		repo = rest[:i]
		ref = rest[i+1:]
	} else {
		repo = rest
	}
	return owner, repo, ref
}

// checkRemoteURL rejects plain http except loopback and validates the URL.
func checkRemoteURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid skill URL %q: %w", raw, err)
	}
	if u.Scheme == "http" {
		host := strings.ToLower(u.Hostname())
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fmt.Errorf("refusing plain http skill URL %q (use https or loopback)", raw)
		}
	} else if u.Scheme != "https" {
		return fmt.Errorf("unsupported skill URL scheme %q", u.Scheme)
	}
	return nil
}

// checkDestOverwrite refuses to clobber an existing install without -y.
func checkDestOverwrite(targetDir string, yes bool) error {
	if st, err := os.Stat(targetDir); err == nil && st.IsDir() {
		if !yes {
			return fmt.Errorf("skill directory %s already exists (pass -y to overwrite)", targetDir)
		}
		if err := os.RemoveAll(targetDir); err != nil {
			return fmt.Errorf("removing existing skill directory: %w", err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	if st, err := os.Lstat(src); err != nil {
		return err
	} else if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to copy symlink %s", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if st, err := os.Lstat(dst); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write through symlink %s", dst)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, io.LimitReader(in, MaxSkillCopyBytes+1))
	return err
}

func copyDir(src, dst string) error {
	var totalBytes int64
	var fileCount int
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to install symlink %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}
		fileCount++
		if fileCount > MaxSkillCopyFiles {
			return fmt.Errorf("skill directory exceeds %d files, refusing to install", MaxSkillCopyFiles)
		}
		totalBytes += info.Size()
		if totalBytes > MaxSkillCopyBytes {
			return fmt.Errorf("skill directory exceeds %d bytes, refusing to install", MaxSkillCopyBytes)
		}
		if err := copyFile(path, targetPath); err != nil {
			return err
		}
		if info.Mode().Perm()&0o100 != 0 {
			_ = os.Chmod(targetPath, 0o755)
		}
		return nil
	})
}

// validateSkillFile light-validates a fetched SKILL.md: frontmatter name and
// description must be present and the name portable. It deliberately skips the
// directory-name match (the install dir was just derived) - full validation
// runs at discovery time.
func validateSkillFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return fmt.Errorf("downloaded skill %s has no frontmatter (expected ---)", path)
	}
	lines := strings.Split(content, "\n")
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return fmt.Errorf("downloaded skill %s has unterminated frontmatter", path)
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closeIdx], "\n")), &fm); err != nil {
		return fmt.Errorf("parsing skill frontmatter: %w", err)
	}
	if err := ValidateSkillName(strings.ToLower(strings.TrimSpace(fm.Name))); err != nil {
		return fmt.Errorf("invalid skill name in frontmatter: %w", err)
	}
	if strings.TrimSpace(fm.Description) == "" {
		return fmt.Errorf("skill description cannot be empty")
	}
	return nil
}

// validateInstalledSkill ensures a copied directory contains at least one
// valid SKILL.md.
func validateInstalledSkill(dir string) error {
	var found int
	var firstErr error
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Base(path) != "SKILL.md" {
			return nil
		}
		found++
		if firstErr == nil {
			firstErr = validateSkillFile(path)
		}
		return nil
	})
	if found == 0 {
		return fmt.Errorf("no SKILL.md found in installed skill %s", dir)
	}
	return firstErr
}
