// knowledge.go - wiki-style persistent knowledge base for the hakase agent.
//
// Notes are markdown files with YAML frontmatter and [[wikilinks]]. The
// knowledge directory is walked on demand to build an in-memory index; note
// files themselves are the source of truth (no separate registry).
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// slugRegex validates a kebab-case slug: lowercase alphanumeric tokens
// separated by single hyphens, at most 64 characters.
var slugRegex = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// frontmatterDelimiter is the YAML frontmatter boundary marker.
const frontmatterDelimiter = "---"

// KnowledgeSource records provenance for a note: a URL or a local raw/ path.
type KnowledgeSource struct {
	URL  string `yaml:"url,omitempty"`
	Path string `yaml:"path,omitempty"`
}

// KnowledgeFrontmatter is the YAML frontmatter block of a knowledge note.
type KnowledgeFrontmatter struct {
	Title      string            `yaml:"title"`
	Aliases    []string          `yaml:"aliases,omitempty"`
	Tags       []string          `yaml:"tags,omitempty"`
	Created    string            `yaml:"created"`
	Updated    string            `yaml:"updated"`
	Status     string            `yaml:"status,omitempty"`
	Confidence string            `yaml:"confidence,omitempty"`
	Sources    []KnowledgeSource `yaml:"sources,omitempty"`
	Summary    string            `yaml:"summary,omitempty"`
	Related    []string          `yaml:"related,omitempty"`
}

// KnowledgeNote is a single knowledge note: its slug, file path, parsed
// frontmatter, body (markdown after the frontmatter delimiter), and the
// raw string content as read from disk.
type KnowledgeNote struct {
	Slug        string
	Path        string
	Frontmatter KnowledgeFrontmatter
	Body        string
	Raw         string
}

// KnowledgeIndex is the in-memory lookup structure built from a directory
// walk. Backlinks and Dangling are computed by scanning every note's outlinks.
// ByAlias is populated lazily on the first alias-based lookup miss.
type KnowledgeIndex struct {
	BySlug     map[string]*KnowledgeNote
	ByBasename map[string][]string
	ByAlias    map[string]string
	Backlinks  map[string][]string
	Dangling   map[string][]string
}

// KnowledgeLint is the health report returned by lint_knowledge.
type KnowledgeLint struct {
	Total         int
	Orphans       []string
	DanglingLinks []string
	Archived      []string
	BrokenIndex   bool
	Issues        []string
}

// knowledgeMu serializes mutations to the knowledge directory (saves,
// updates, index/log regeneration).
var knowledgeMu sync.Mutex

// ------------------- helpers ------------------------------------------------

// knowledgeDir resolves an empty dir to the default "./knowledge". A leading
// "~/" is expanded to the user's home directory, so a user-global knowledge
// base can be configured as e.g. "~/.hakase/knowledge" (or "$HAKASE_HOME").
func knowledgeDir(dir string) string {
	if dir == "" {
		return "./knowledge"
	}
	if strings.HasPrefix(dir, "~/") || dir == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			if dir == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(dir, "~/"))
		}
	}
	return dir
}

// notePath returns the preferred path for a note given a directory and slug.
// If notes/ subdirectory exists and contains <slug>.md, prefer it; otherwise
// return <dir>/<slug>.md.
func notePath(dir, slug string) string {
	notesDir := filepath.Join(dir, "notes")
	notesPath := filepath.Join(notesDir, slug+".md")
	if _, err := os.Stat(notesPath); err == nil {
		return notesPath
	}
	return filepath.Join(dir, slug+".md")
}

// ------------------- wikilinks -----------------------------------------------

var wikilinkRe = regexp.MustCompile(`\[\[([^\]|#]+)(?:[|#][^\]]*)?\]\]`)

// ExtractWikilinks extracts all [[wikilink]] targets from a markdown body.
// Targets are the first segment before `|` or `#`. Links inside fenced code
// blocks (delimited by ```) are skipped. Returns unique targets in discovery
// order.
func ExtractWikilinks(body string) []string {
	// Split on fenced code block markers; scan only non-fenced segments
	// (even-indexed chunks).
	chunks := strings.Split(body, "```")
	var targets []string
	seen := make(map[string]bool)
	for i, chunk := range chunks {
		if i%2 != 0 {
			continue // inside fenced block
		}
		matches := wikilinkRe.FindAllStringSubmatch(chunk, -1)
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			target := strings.TrimSpace(m[1])
			if target == "" {
				continue
			}
			if !seen[target] {
				seen[target] = true
				targets = append(targets, target)
			}
		}
	}
	return targets
}

// ------------------- slugify --------------------------------------------------

// Slugify converts a title to a kebab-case slug. If the result does not
// match slugRegex or is empty, falls back to "note".
func Slugify(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	// Replace runs of non-alphanumeric characters with a single hyphen.
	var b strings.Builder
	inHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			inHyphen = false
		} else {
			if !inHyphen {
				b.WriteByte('-')
				inHyphen = true
			}
		}
	}
	s = strings.Trim(b.String(), "-")
	if s == "" || len(s) > 64 || !slugRegex.MatchString(s) {
		return "note"
	}
	return s
}

// ------------------- parse ----------------------------------------------------

// ParseKnowledgeNote parses a knowledge note from raw bytes. The parsing
// protocol mirrors ParseMarkdownSkill: strip BOM, normalize CRLF, frontmatter
// must start at byte 0 with "---\n", first trimmed "---" line closes,
// tolerant YAML fallback on full-unmarshal error.
func ParseKnowledgeNote(path string, data []byte) (*KnowledgeNote, error) {
	// Strip UTF-8 BOM.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	// Normalize CRLF.
	content := strings.ReplaceAll(string(data), "\r\n", "\n")

	// Frontmatter must start at byte 0.
	if !strings.HasPrefix(content, frontmatterDelimiter+"\n") {
		return nil, fmt.Errorf("frontmatter must start with ---")
	}

	lines := strings.Split(content, "\n")
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == frontmatterDelimiter {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return nil, fmt.Errorf("missing closing --- delimiter")
	}

	body := strings.Join(lines[closeIdx+1:], "\n")
	frontmatterYAML := strings.Join(lines[1:closeIdx], "\n")

	var fm KnowledgeFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatterYAML), &fm); err != nil {
		// Tolerant fallback: extract at minimum title and summary.
		var minimal struct {
			Title   string `yaml:"title"`
			Summary string `yaml:"summary,omitempty"`
		}
		if merr := yaml.Unmarshal([]byte(frontmatterYAML), &minimal); merr != nil {
			return nil, err
		}
		fm = KnowledgeFrontmatter{Title: minimal.Title, Summary: minimal.Summary}
	}

	slug := strings.TrimSuffix(filepath.Base(path), ".md")

	return &KnowledgeNote{
		Slug:        slug,
		Path:        path,
		Frontmatter: fm,
		Body:        body,
		Raw:         content,
	}, nil
}

// ------------------- index ----------------------------------------------------

// BuildKnowledgeIndex walks dir for *.md files (excluding index.md, log.md,
// and any path containing /raw/ or raw/), parses each note, and builds the
// lookup maps. Invalid notes are skipped (logged via a nil-safe printf) but
// never stop the walk. The notes/ subdirectory is preferred per notePath.
func BuildKnowledgeIndex(dir string) (*KnowledgeIndex, error) {
	idx := &KnowledgeIndex{
		BySlug:     make(map[string]*KnowledgeNote),
		ByBasename: make(map[string][]string),
		ByAlias:    make(map[string]string),
		Backlinks:  make(map[string][]string),
		Dangling:   make(map[string][]string),
	}

	dir = knowledgeDir(dir)

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip non-markdown files.
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		basename := filepath.Base(path)
		// Exclude special files.
		if basename == "index.md" || basename == "log.md" {
			return nil
		}
		// Exclude raw/ directory contents.
		rel, _ := filepath.Rel(dir, path)
		if strings.HasPrefix(rel, "raw"+string(filepath.Separator)) || rel == "raw" {
			return nil
		}

		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		note, perr := ParseKnowledgeNote(path, data)
		if perr != nil {
			// Skip invalid notes; don't abort the walk.
			return nil
		}

		slug := strings.ToLower(note.Slug)
		note.Slug = slug
		note.Path = path

		// notes/ preference: if a note already exists for this slug and the
		// new note is NOT under notes/, skip it in favor of the notes/ copy.
		if existing, ok := idx.BySlug[slug]; ok {
			existingRel, _ := filepath.Rel(dir, existing.Path)
			newRel, _ := filepath.Rel(dir, path)
			if strings.HasPrefix(existingRel, "notes"+string(filepath.Separator)) {
				return nil // keep the existing notes/ copy
			}
			if strings.HasPrefix(newRel, "notes"+string(filepath.Separator)) {
				// new is notes/, replace the old one.
			}
		}

		idx.BySlug[slug] = note
		stem := strings.ToLower(strings.TrimSuffix(basename, ".md"))
		idx.ByBasename[stem] = append(idx.ByBasename[stem], slug)

		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return idx, err
	}

	// Build backlinks and dangling: scan every note's outlinks, resolving each
	// target with the documented resolution order (slug -> basename -> alias).
	for slug, note := range idx.BySlug {
		outlinks := ExtractWikilinks(note.Body)
		for _, target := range outlinks {
			if resolved, ok := ResolveTarget(idx, target); ok {
				idx.Backlinks[resolved.Slug] = append(idx.Backlinks[resolved.Slug], slug)
			} else {
				idx.Dangling[target] = append(idx.Dangling[target], slug)
			}
		}
	}

	return idx, nil
}

// resolveAliasFor builds the ByAlias map from scratch. Called lazily on the
// first alias-based lookup miss.
func resolveAliasFor(idx *KnowledgeIndex, slug string) bool {
	note, ok := idx.BySlug[slug]
	if !ok {
		return false
	}
	for _, alias := range note.Frontmatter.Aliases {
		if _, exists := idx.ByAlias[alias]; !exists {
			idx.ByAlias[alias] = slug
		}
	}
	return true
}

// ResolveTarget resolves a target string to a note. Resolution order:
// exact slug -> unique basename -> alias -> unresolved.
// Ambiguous basename (2+ matches) = unresolved.
// Matching is case-insensitive (Obsidian semantics): [[My Note]] resolves to
// the note whose slug or basename matches case-insensitively.
func ResolveTarget(idx *KnowledgeIndex, target string) (*KnowledgeNote, bool) {
	lower := strings.ToLower(target)

	// 1. Exact slug match (case-insensitive; slugs are already lowercase).
	if note, ok := idx.BySlug[lower]; ok {
		return note, true
	}

	// 2. Unique basename match (case-insensitive).
	if slugs, ok := idx.ByBasename[lower]; ok {
		if len(slugs) == 1 {
			return idx.BySlug[slugs[0]], true
		}
		// Ambiguous: 2+ matches = unresolved.
		return nil, false
	}

	// 3. Alias match. Build ByAlias lazily from all notes.
	if slug, ok := idx.ByAlias[lower]; ok {
		return idx.BySlug[slug], true
	}
	// Lazy build: scan all notes for aliases (keys normalized to lowercase).
	for _, note := range idx.BySlug {
		for _, alias := range note.Frontmatter.Aliases {
			aliasLower := strings.ToLower(alias)
			if _, exists := idx.ByAlias[aliasLower]; !exists {
				idx.ByAlias[aliasLower] = note.Slug
			}
		}
	}
	if slug, ok := idx.ByAlias[lower]; ok {
		return idx.BySlug[slug], true
	}

	return nil, false
}

// SearchKnowledge performs a case-insensitive substring search over Title,
// Aliases, Tags, Summary, and Body. If tags is non-empty, the note must have
// ALL given tags (case-insensitive). Archived notes are excluded unless
// includeArchived is true. Results are sorted by Title.
func SearchKnowledge(idx *KnowledgeIndex, query string, tags []string, includeArchived bool) []KnowledgeNote {
	query = strings.ToLower(query)
	var results []KnowledgeNote

noteloop:
	for _, note := range idx.BySlug {
		if !includeArchived && note.Frontmatter.Status == "archived" {
			continue
		}

		// Tag filter: note must have ALL requested tags.
		for _, t := range tags {
			found := false
			for _, nt := range note.Frontmatter.Tags {
				if strings.EqualFold(nt, t) {
					found = true
					break
				}
			}
			if !found {
				continue noteloop
			}
		}

		// Substring match across searchable fields.
		matched := strings.Contains(strings.ToLower(note.Frontmatter.Title), query) ||
			strings.Contains(strings.ToLower(strings.Join(note.Frontmatter.Aliases, " ")), query) ||
			strings.Contains(strings.ToLower(strings.Join(note.Frontmatter.Tags, " ")), query) ||
			strings.Contains(strings.ToLower(note.Frontmatter.Summary), query) ||
			strings.Contains(strings.ToLower(note.Body), query)

		if matched {
			results = append(results, *note)
		}
	}

	// Sort by Title.
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[i].Frontmatter.Title > results[j].Frontmatter.Title {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	return results
}

// ------------------- persistence ---------------------------------------------

// SaveNote writes a new knowledge note to disk atomically. Returns an error
// if a note with the same slug already exists.
func SaveNote(dir string, note *KnowledgeNote) error {
	knowledgeMu.Lock()
	defer knowledgeMu.Unlock()

	dir = knowledgeDir(dir)
	path := filepath.Join(dir, note.Slug+".md")

	// Ensure notes/ subdirectory exists.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("note %q already exists (use update_knowledge to modify)", note.Slug)
	}

	// Atomic write: temp file then rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(note.Raw), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// UpdateNote overwrites an existing knowledge note atomically. If the note
// does not exist, it is created (similar semantics to os.WriteFile with
// create permission).
func UpdateNote(dir string, note *KnowledgeNote) error {
	knowledgeMu.Lock()
	defer knowledgeMu.Unlock()

	dir = knowledgeDir(dir)
	path := filepath.Join(dir, note.Slug+".md")

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(note.Raw), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// UpdateIndexFile regenerates index.md in the knowledge directory from the
// current index. Writes atomically.
func UpdateIndexFile(dir string, idx *KnowledgeIndex) error {
	knowledgeMu.Lock()
	defer knowledgeMu.Unlock()

	dir = knowledgeDir(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Collect notes and sort by title.
	var notes []*KnowledgeNote
	for _, n := range idx.BySlug {
		notes = append(notes, n)
	}
	for i := 0; i < len(notes); i++ {
		for j := i + 1; j < len(notes); j++ {
			if notes[i].Frontmatter.Title > notes[j].Frontmatter.Title {
				notes[i], notes[j] = notes[j], notes[i]
			}
		}
	}

	var b strings.Builder
	b.WriteString("# Knowledge Index\n\n")
	for _, n := range notes {
		summary := n.Frontmatter.Summary
		if summary == "" {
			summary = "-"
		}
		b.WriteString(fmt.Sprintf("- [[%s]] - %s\n", n.Frontmatter.Title, summary))
	}

	path := filepath.Join(dir, "index.md")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AppendLog appends a log entry to log.md in the knowledge directory.
// Creates the file with a header if it does not exist.
func AppendLog(dir, action, title string) error {
	knowledgeMu.Lock()
	defer knowledgeMu.Unlock()

	dir = knowledgeDir(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	path := filepath.Join(dir, "log.md")
	timestamp := time.Now().Format("2006-01-02 15:04")
	entry := fmt.Sprintf("## [%s] %s | %s\n", timestamp, action, title)

	// If log doesn't exist, create it with a header.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		entry = "# Knowledge Log\n\n" + entry
	} else if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(entry)
	return err
}
