// knowledge.go - wiki-style persistent knowledge base for the hakase agent.
//
// Notes are markdown files with YAML frontmatter and [[wikilinks]]. The
// knowledge directory is walked on demand to build an in-memory index; note
// files themselves are the source of truth (no separate registry).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	// Metadata holds structured key/value facts extracted at save time (e.g.
	// GitHub project fields: owner, maintainers, stars, language, license).
	Metadata map[string]string `yaml:"metadata,omitempty"`
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
		var metadataText string
		for _, v := range note.Frontmatter.Metadata {
			metadataText += v + " "
		}
		matched := strings.Contains(strings.ToLower(note.Frontmatter.Title), query) ||
			strings.Contains(strings.ToLower(strings.Join(note.Frontmatter.Aliases, " ")), query) ||
			strings.Contains(strings.ToLower(strings.Join(note.Frontmatter.Tags, " ")), query) ||
			strings.Contains(strings.ToLower(note.Frontmatter.Summary), query) ||
			strings.Contains(strings.ToLower(metadataText), query) ||
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

// ------------------- enrichment helpers ---------------------------------------
//
// save_knowledge enriches a new note before persisting it: a summary and a
// plain-text excerpt are derived from the content, existing notes that share
// significant keywords are linked under a Related section, and GitHub project
// metadata (owner, maintainers, stars, language, ...) is captured when the
// content or sources reference a repository.

// plainText strips markdown formatting, code fences, inline code, links, and
// wikilinks from content, returning readable prose with collapsed whitespace.
func plainText(content string) string {
	// Drop fenced code blocks (even-indexed chunks are outside fences).
	chunks := strings.Split(content, "```")
	var out strings.Builder
	for i, chunk := range chunks {
		if i%2 != 0 {
			continue
		}
		out.WriteString(chunk)
	}
	s := out.String()
	// Inline code.
	s = regexp.MustCompile("`[^`]*`").ReplaceAllString(s, " ")
	// Markdown links: [text](url) -> text.
	s = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`).ReplaceAllString(s, "$1")
	// Wikilinks: [[target|label]] / [[target#heading]] -> target.
	s = regexp.MustCompile(`\[\[([^\]|#]+)(?:[|#][^\]]*)?\]\]`).ReplaceAllString(s, "$1")
	// Headings, emphasis, and list markers.
	s = regexp.MustCompile(`(?m)^#{1,6}\s+`).ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "*", "")
	var cleaned []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "-*># "))
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, " ")
}

// splitSentences splits text on sentence-ending punctuation (. ! ?) that is
// followed by a space or the end of the string.
func splitSentences(text string) []string {
	var sentences []string
	var current []rune
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		current = append(current, c)
		if (c == '.' || c == '!' || c == '?') &&
			(i+1 >= len(runes) || runes[i+1] == ' ') {
			if s := strings.TrimSpace(string(current)); s != "" {
				sentences = append(sentences, s)
			}
			current = current[:0]
		}
	}
	if s := strings.TrimSpace(string(current)); s != "" {
		sentences = append(sentences, s)
	}
	return sentences
}

// deriveSummary returns a one-line summary of content: the first sentence,
// extended with the second when it is very short, capped at 240 runes. Empty
// content falls back to fallbackTitle.
func deriveSummary(content, fallbackTitle string) string {
	text := strings.Join(strings.Fields(plainText(content)), " ")
	if text == "" {
		return fallbackTitle
	}
	parts := splitSentences(text)
	if len(parts) == 0 {
		return truncateRunes(text, 240)
	}
	summary := parts[0]
	if len([]rune(summary)) < 40 && len(parts) > 1 {
		summary = parts[0] + " " + parts[1]
	}
	return truncateRunes(summary, 240)
}

// deriveExcerpt returns the first ~300 runes of plain text from content.
func deriveExcerpt(content string) string {
	text := strings.Join(strings.Fields(plainText(content)), " ")
	if text == "" {
		return ""
	}
	return truncateRunes(text, 300)
}

// knowledgeStopwords are common English function words excluded from related-
// note keyword extraction.
var knowledgeStopwords = map[string]bool{
	"about": true, "above": true, "after": true, "again": true, "against": true,
	"also": true, "been": true, "before": true, "being": true, "below": true,
	"between": true, "both": true, "could": true, "does": true, "doing": true,
	"down": true, "each": true, "else": true, "from": true, "further": true,
	"have": true, "having": true, "here": true, "into": true, "just": true,
	"like": true, "more": true, "most": true, "much": true, "must": true,
	"only": true, "other": true, "over": true, "same": true, "such": true,
	"than": true, "that": true, "their": true, "them": true, "then": true,
	"there": true, "these": true, "they": true, "this": true, "those": true,
	"through": true, "under": true, "until": true, "upon": true, "very": true,
	"were": true, "what": true, "when": true, "where": true, "which": true,
	"while": true, "will": true, "with": true, "within": true, "would": true,
	"your": true, "yours": true, "should": true, "because": true, "using": true,
}

// significantKeywords tokenizes the given texts into lowercase alphanumeric
// words of length >= 4 that are not stopwords. Returns a deduplicated set.
func significantKeywords(texts ...string) map[string]bool {
	kw := make(map[string]bool)
	for _, text := range texts {
		for _, w := range strings.Fields(strings.ToLower(plainText(text))) {
			w = strings.Trim(w, ".,;:!?()[]{}\"'`-")
			r := []rune(w)
			if len(r) < 4 {
				continue
			}
			alpha := true
			for _, c := range r {
				if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
					alpha = false
					break
				}
			}
			if !alpha || knowledgeStopwords[w] {
				continue
			}
			kw[w] = true
		}
	}
	return kw
}

// findRelatedNotes scores existing notes by how many significant keywords of
// the new note appear in their title (weight 3), tags (weight 2), summary (1),
// and body (1). Notes scoring below 2 are skipped, archived notes are excluded,
// and at most maxResults notes are returned ordered by score desc, then title.
func findRelatedNotes(idx *KnowledgeIndex, note *KnowledgeNote, maxResults int) []*KnowledgeNote {
	keywords := significantKeywords(
		note.Frontmatter.Title,
		note.Frontmatter.Summary,
		strings.Join(note.Frontmatter.Tags, " "),
		note.Body,
	)
	if len(keywords) == 0 {
		return nil
	}
	type scored struct {
		note  *KnowledgeNote
		score int
	}
	var candidates []scored
	for slug, other := range idx.BySlug {
		if slug == note.Slug || other.Frontmatter.Status == "archived" {
			continue
		}
		title := strings.ToLower(other.Frontmatter.Title)
		summary := strings.ToLower(other.Frontmatter.Summary)
		body := strings.ToLower(other.Body)
		score := 0
		for kw := range keywords {
			if strings.Contains(title, kw) {
				score += 3
			}
			for _, t := range other.Frontmatter.Tags {
				if strings.EqualFold(t, kw) {
					score += 2
					break
				}
			}
			if strings.Contains(summary, kw) {
				score++
			}
			if strings.Contains(body, kw) {
				score++
			}
		}
		if score >= 2 {
			candidates = append(candidates, scored{other, score})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].note.Frontmatter.Title < candidates[j].note.Frontmatter.Title
	})
	if len(candidates) > maxResults {
		candidates = candidates[:maxResults]
	}
	var out []*KnowledgeNote
	for _, c := range candidates {
		out = append(out, c.note)
	}
	return out
}

// GitHub repository detection. githubURLRe matches github.com URLs; the bare
// pattern matches owner/repo mentions in prose (validated by the API later).
var (
	githubURLRe = regexp.MustCompile(`(?:https?://)?(?:www\.)?github\.com/([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)/([A-Za-z0-9_.-]+)`)
	githubBareRe = regexp.MustCompile(`(^|[^A-Za-z0-9/])([A-Za-z0-9](?:[A-Za-z0-9-]{0,38}[A-Za-z0-9])?)/([A-Za-z0-9_.-]+)([^A-Za-z0-9_.-]|$)`)
)

// extractGitHubRepos returns explicit (github.com URL) and bare (owner/repo)
// repository candidates found in content and sources, deduplicated and in
// discovery order. github.com URLs are masked before bare matching so URL path
// segments (e.g. "com/amurru" inside github.com/amurru/gocaster) are not
// misread as prose mentions; captured segments are trimmed of trailing
// punctuation.
func extractGitHubRepos(content string, sources []string) (explicit, bare [][2]string) {
	seen := make(map[[2]string]bool)
	texts := append([]string{content}, sources...)

	for _, text := range texts {
		for _, m := range githubURLRe.FindAllStringSubmatch(text, -1) {
			if len(m) < 3 {
				continue
			}
			repo := strings.TrimRight(m[2], "._-")
			if repo == "" {
				continue
			}
			c := [2]string{m[1], repo}
			if !seen[c] {
				seen[c] = true
				explicit = append(explicit, c)
			}
		}
	}

	masked := make([]string, len(texts))
	for i, text := range texts {
		masked[i] = githubURLRe.ReplaceAllString(text, " ")
	}
	for _, text := range masked {
		for _, m := range githubBareRe.FindAllStringSubmatch(text, -1) {
			if len(m) < 4 {
				continue
			}
			repo := strings.TrimRight(m[3], "._-")
			if repo == "" {
				continue
			}
			c := [2]string{m[2], repo}
			if seen[c] {
				continue
			}
			seen[c] = true
			bare = append(bare, c)
		}
	}
	return explicit, bare
}

// fetchGitHubMetadata fetches public repository metadata from the GitHub REST
// API: description, language, stars, forks, license, default branch, owner,
// and the top contributors (a proxy for maintainers). It is a package-level
// variable so tests can stub it without network access. The call is bounded by
// an 8-second context deadline.
var fetchGitHubMetadata = func(owner, repo string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	client := &http.Client{}
	meta := map[string]string{
		"github_owner": owner,
		"github_repo":  repo,
		"github_url":   "https://github.com/" + owner + "/" + repo,
	}

	repoPath := "https://api.github.com/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, repoPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hakase-knowledge-bot")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api status %d for %s/%s", resp.StatusCode, owner, repo)
	}

	var repoInfo struct {
		Description   string `json:"description"`
		Language      string `json:"language"`
		Stargazers    int    `json:"stargazers_count"`
		Forks         int    `json:"forks_count"`
		License       *struct {
			SpdxID string `json:"spdx_id"`
		} `json:"license"`
		DefaultBranch string `json:"default_branch"`
		Archived      bool   `json:"archived"`
		UpdatedAt     string `json:"updated_at"`
		Owner         struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"owner"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&repoInfo); err != nil {
		return nil, err
	}

	if repoInfo.Description != "" {
		meta["github_description"] = repoInfo.Description
	}
	if repoInfo.Language != "" {
		meta["github_language"] = repoInfo.Language
	}
	meta["github_stars"] = strconv.Itoa(repoInfo.Stargazers)
	meta["github_forks"] = strconv.Itoa(repoInfo.Forks)
	if repoInfo.License != nil && repoInfo.License.SpdxID != "" {
		meta["github_license"] = repoInfo.License.SpdxID
	}
	if repoInfo.DefaultBranch != "" {
		meta["github_default_branch"] = repoInfo.DefaultBranch
	}
	meta["github_archived"] = strconv.FormatBool(repoInfo.Archived)
	if len(repoInfo.UpdatedAt) >= 10 {
		meta["github_updated"] = repoInfo.UpdatedAt[:10]
	}
	if repoInfo.Owner.Login != "" {
		meta["github_owner_type"] = repoInfo.Owner.Type
	}

	// Top contributors as maintainers (best effort; shares the context deadline).
	maintainers := []string{repoInfo.Owner.Login}
	if cReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/contributors?per_page=5", nil); err == nil {
		cReq.Header.Set("Accept", "application/vnd.github+json")
		cReq.Header.Set("User-Agent", "hakase-knowledge-bot")
		if cResp, err := client.Do(cReq); err == nil {
			defer cResp.Body.Close()
			if cResp.StatusCode == http.StatusOK {
				var contribs []struct {
					Login string `json:"login"`
				}
				if err := json.NewDecoder(cResp.Body).Decode(&contribs); err == nil {
					for _, c := range contribs {
						if c.Login != "" && c.Login != repoInfo.Owner.Login {
							maintainers = append(maintainers, c.Login)
						}
					}
				}
			}
		}
	}
	if len(maintainers) > 0 {
		meta["github_maintainers"] = strings.Join(maintainers, ", ")
	}
	return meta, nil
}

// enrichGitHubMetadata detects a GitHub repository in content or sources and
// fetches best-effort project metadata. Explicit github.com URLs are trusted
// even when the fetch fails (the URL itself is evidence); bare owner/repo
// mentions are only recorded when the API validates them. Returns nil when no
// repository can be identified.
func enrichGitHubMetadata(content string, sources []string) map[string]string {
	explicit, bare := extractGitHubRepos(content, sources)
	for _, c := range explicit {
		if meta, err := fetchGitHubMetadata(c[0], c[1]); err == nil && meta != nil {
			return meta
		}
	}
	for _, c := range bare {
		if meta, err := fetchGitHubMetadata(c[0], c[1]); err == nil && meta != nil {
			return meta
		}
	}
	if len(explicit) > 0 {
		c := explicit[0]
		return map[string]string{
			"github_owner": c[0],
			"github_repo":  c[1],
			"github_url":   "https://github.com/" + c[0] + "/" + c[1],
		}
	}
	return nil
}

// ------------------- model-backed enrichment ----------------------------------
//
// save_knowledge asks the configured summarization model to produce structured
// enrichment data (summary, excerpt, tags, aliases, related notes, metadata)
// in a strict JSON shape. The deterministic extractors above are the fallback
// when no model is available or the call fails.

// KnowledgeEnrichment is the structured data produced for save_knowledge by
// the summarization model (primary) or derived deterministically (fallback).
type KnowledgeEnrichment struct {
	Summary  string            `json:"summary"`
	Excerpt  string            `json:"excerpt"`
	Tags     []string          `json:"tags"`
	Aliases  []string          `json:"aliases"`
	Related  []string          `json:"related"`
	Metadata map[string]string `json:"metadata"`
}

// enrichKnowledgeFn is the model-backed enrichment callback, set in
// setupRunner with access to the configured summary model (falling back to
// the primary model). When nil (CLI, tests, headless runs without a model),
// save_knowledge uses only the deterministic extractors.
var enrichKnowledgeFn func(ctx context.Context, prompt string) (string, error)

// knowledgeEnrichTemplate instructs the model to return strict JSON.
const knowledgeEnrichTemplate = `You enrich a knowledge note before it is persisted. Given the note title and content, and the list of existing notes, produce STRICT JSON (no markdown fences, no preamble) with exactly this shape:

{"summary": "...", "excerpt": "...", "tags": [...], "aliases": [...], "related": [...], "metadata": {...}}

Field rules:
- summary: one or two sentence plain-text summary of the note (max 240 characters).
- excerpt: a representative plain-text excerpt from the content (max 300 characters).
- tags: 3-6 concise lowercase tags for search filtering.
- aliases: 1-3 alternative names or short forms of the title; empty array when none.
- related: at most 5 slugs from the EXISTING NOTES list that are genuinely related to this note; empty array when none. Use the exact slug strings.
- metadata: structured string facts. When the note concerns a GitHub project (github.com/owner/repo or owner/repo is referenced), include github_owner, github_repo, github_url, github_maintainers (owner and known maintainers, comma-separated), github_language, github_stars, github_license, and github_description - only facts you are confident about. Otherwise an empty object {}.

TITLE: %s

TAGS PROVIDED: %s

CONTENT:
%s

EXISTING NOTES:
%s

Output ONLY the JSON object.`

// buildKnowledgeEnrichPrompt assembles the enrichment prompt for the model.
// Content is capped so the call stays cheap; candidates are existing notes
// with title, slug, and summary so the model can pick related notes.
func buildKnowledgeEnrichPrompt(title, content string, tags []string, candidates []*KnowledgeNote) string {
	if len(content) > 4000 {
		content = content[:4000] + "..."
	}
	tagStr := "none"
	if len(tags) > 0 {
		tagStr = strings.Join(tags, ", ")
	}
	var notes strings.Builder
	for _, n := range candidates {
		summary := n.Frontmatter.Summary
		if summary == "" {
			summary = "-"
		}
		fmt.Fprintf(&notes, "- %s (%s) - %s\n", n.Frontmatter.Title, n.Slug, summary)
	}
	return fmt.Sprintf(knowledgeEnrichTemplate, title, tagStr, content, notes.String())
}

// parseKnowledgeEnrichment extracts a KnowledgeEnrichment from the model's
// raw response, tolerating markdown fences and surrounding prose. Returns nil
// when the response does not parse or contains no usable summary.
func parseKnowledgeEnrichment(raw string) *KnowledgeEnrichment {
	raw = strings.TrimSpace(raw)
	// Strip markdown code fences when present.
	if strings.HasPrefix(raw, "```") {
		if i := strings.IndexByte(raw, '\n'); i >= 0 {
			raw = raw[i+1:]
		}
		if i := strings.LastIndex(raw, "```"); i >= 0 {
			raw = raw[:i]
		}
		raw = strings.TrimSpace(raw)
	}
	var enr KnowledgeEnrichment
	if err := json.Unmarshal([]byte(raw), &enr); err != nil {
		// Tolerant fallback: extract the first balanced { ... } block.
		start := strings.IndexByte(raw, '{')
		end := strings.LastIndexByte(raw, '}')
		if start < 0 || end <= start {
			return nil
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &enr); err != nil {
			return nil
		}
	}
	if strings.TrimSpace(enr.Summary) == "" {
		return nil
	}
	enr.Tags = dedupeStrings(enr.Tags)
	enr.Aliases = dedupeStrings(enr.Aliases)
	if enr.Metadata == nil {
		enr.Metadata = map[string]string{}
	}
	return &enr
}

// modelEnrichKnowledge runs the model-backed enrichment for a note being
// saved. It returns nil when no model callback is configured, when the call
// fails, or when the response does not parse - callers then fall back to the
// deterministic extractors.
func modelEnrichKnowledge(title, content string, tags []string, idx *KnowledgeIndex) *KnowledgeEnrichment {
	if enrichKnowledgeFn == nil {
		return nil
	}
	var candidates []*KnowledgeNote
	for _, n := range idx.BySlug {
		candidates = append(candidates, n)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Frontmatter.Title < candidates[j].Frontmatter.Title
	})
	const maxCandidates = 60
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}

	prompt := buildKnowledgeEnrichPrompt(title, content, tags, candidates)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	raw, err := enrichKnowledgeFn(ctx, prompt)
	if err != nil {
		return nil
	}
	return parseKnowledgeEnrichment(raw)
}

// resolveRelated resolves model-suggested related slugs against the index,
// skipping unknown or already-selected targets. When none of the suggested
// slugs resolve, it falls back to keyword-based discovery of related notes.
func resolveRelated(idx *KnowledgeIndex, note *KnowledgeNote, slugs []string) []*KnowledgeNote {
	var out []*KnowledgeNote
	seen := make(map[string]bool)
	for _, s := range slugs {
		if r, ok := ResolveTarget(idx, s); ok && !seen[r.Slug] {
			seen[r.Slug] = true
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return findRelatedNotes(idx, note, 5)
	}
	return out
}

// dedupeStrings removes empty and duplicate entries, preserving order.
func dedupeStrings(list []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range list {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
