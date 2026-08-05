// knowledge_tools.go - ADK tools for the knowledge base: save, recall, search,
// update, link, cite, list and lint.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// ------------------- input/output structs ------------------------------------

// SaveKnowledgeInput is the input for the save_knowledge tool.
type SaveKnowledgeInput struct {
	Title      string   `json:"title" doc:"Required. Display title for the note (slug is derived from it)."`
	Content    string   `json:"content" doc:"Required. Markdown body content. [[wikilinks]] will be resolved and dangling targets reported."`
	Tags       []string `json:"tags,omitempty" doc:"Optional tags for search filtering."`
	Sources    []string `json:"sources,omitempty" doc:"Optional source URLs or raw/ paths for provenance."`
	Confidence string   `json:"confidence,omitempty" doc:"Optional confidence level: high, medium, or low."`
	Aliases    []string `json:"aliases,omitempty" doc:"Optional alternative names for resolution."`
	Status     string   `json:"status,omitempty" doc:"Optional status: draft (default), permanent, or archived."`
}

// SaveKnowledgeOutput is the output for the save_knowledge tool.
type SaveKnowledgeOutput struct {
	Slug          string   `json:"slug" doc:"The kebab-case slug derived from the title."`
	Path          string   `json:"path" doc:"File path where the note was saved."`
	DanglingLinks []string `json:"dangling_links" doc:"[[wikilink]] targets from the content that do not resolve to existing notes."`
	Message       string   `json:"message" doc:"Human-readable status."`
}

// RecallKnowledgeInput is the input for the recall_knowledge tool.
type RecallKnowledgeInput struct {
	Name string `json:"name" doc:"Required. Note slug, basename, or alias to recall."`
}

// RecallKnowledgeOutput is the output for the recall_knowledge tool.
type RecallKnowledgeOutput struct {
	Title         string   `json:"title" doc:"Display title from frontmatter."`
	Slug          string   `json:"slug" doc:"Note slug."`
	Content       string   `json:"content" doc:"Full markdown body."`
	Summary       string   `json:"summary" doc:"One-line summary from frontmatter."`
	Backlinks     []string `json:"backlinks" doc:"Slugs of notes that link to this note."`
	Related       []string `json:"related" doc:"Related note titles from frontmatter."`
	Sources       []string `json:"sources" doc:"Source URLs from frontmatter."`
	Tags          []string `json:"tags" doc:"Tags from frontmatter."`
	Updated       string   `json:"updated" doc:"Last-updated date."`
	Status        string   `json:"status" doc:"Note status: draft, permanent, or archived."`
	DanglingLinks []string `json:"dangling_links" doc:"[[wikilink]] targets in the body that do not exist."`
}

// SearchKnowledgeInput is the input for the search_knowledge tool.
type SearchKnowledgeInput struct {
	Query           string   `json:"query" doc:"Required. Case-insensitive search term matched against title, aliases, tags, summary, and body."`
	Tags            []string `json:"tags,omitempty" doc:"Optional tags filter; notes must have ALL listed tags."`
	IncludeArchived bool     `json:"include_archived,omitempty" doc:"Include archived notes in results (default false)."`
}

// KnowledgeSearchResult is a single search result.
type KnowledgeSearchResult struct {
	Title   string   `json:"title" doc:"Display title."`
	Slug    string   `json:"slug" doc:"Note slug."`
	Summary string   `json:"summary" doc:"One-line summary."`
	Tags    []string `json:"tags" doc:"Tags."`
	Updated string   `json:"updated" doc:"Last-updated date."`
	Status  string   `json:"status" doc:"Note status."`
	Snippet string   `json:"snippet" doc:"First ~200 characters of body or the matching line."`
}

// SearchKnowledgeOutput is the output for the search_knowledge tool.
type SearchKnowledgeOutput struct {
	Results []KnowledgeSearchResult `json:"results" doc:"Matching notes sorted by title."`
}

// UpdateKnowledgeInput is the input for the update_knowledge tool.
type UpdateKnowledgeInput struct {
	Name       string   `json:"name" doc:"Required. Note slug, basename, or alias to update."`
	Content    string   `json:"content,omitempty" doc:"Replacement markdown body. If empty and append_text is set, append_text is appended instead."`
	AppendText string   `json:"append_text,omitempty" doc:"Text to append to the existing body (used when content is empty)."`
	Tags       []string `json:"tags,omitempty" doc:"Replacement tag list."`
	Sources    []string `json:"sources,omitempty" doc:"Replacement source URLs."`
	Confidence string   `json:"confidence,omitempty" doc:"New confidence level."`
	Aliases    []string `json:"aliases,omitempty" doc:"Replacement alias list."`
	Status     string   `json:"status,omitempty" doc:"New status."`
}

// UpdateKnowledgeOutput is the output for the update_knowledge tool.
type UpdateKnowledgeOutput struct {
	Slug          string   `json:"slug" doc:"Note slug."`
	Path          string   `json:"path" doc:"File path."`
	DanglingLinks []string `json:"dangling_links" doc:"Unresolved [[wikilink]] targets in the updated body."`
	Message       string   `json:"message" doc:"Human-readable status."`
}

// LinkKnowledgeInput is the input for the link_knowledge tool.
type LinkKnowledgeInput struct {
	From  string `json:"from" doc:"Required. Source note slug, basename, or alias."`
	To    string `json:"to" doc:"Required. Target note slug, basename, or alias to link to."`
	Label string `json:"label,omitempty" doc:"Optional display label for the link (defaults to To)."`
}

// LinkKnowledgeOutput is the output for the link_knowledge tool.
type LinkKnowledgeOutput struct {
	Slug     string   `json:"slug" doc:"Source note slug."`
	To       string   `json:"to" doc:"Target note slug."`
	Dangling []string `json:"dangling" doc:"If non-empty, the target does not exist and the link was NOT written."`
	Message  string   `json:"message" doc:"Human-readable status."`
}

// CiteKnowledgeInput is the input for the cite_knowledge tool.
type CiteKnowledgeInput struct {
	Name string `json:"name" doc:"Required. Note slug, basename, or alias to cite."`
}

// CiteKnowledgeOutput is the output for the cite_knowledge tool.
type CiteKnowledgeOutput struct {
	Title    string `json:"title" doc:"Display title."`
	Slug     string `json:"slug" doc:"Note slug."`
	Excerpt  string `json:"excerpt" doc:"First ~200 characters of body."`
	Citation string `json:"citation" doc:"Formatted citation string (anthropic-style footnote)."`
	Source   string `json:"source" doc:"First source URL or 'n/a'."`
	Updated  string `json:"updated" doc:"Last-updated date."`
}

// ListKnowledgeInput is the input for the list_knowledge tool.
type ListKnowledgeInput struct {
	IncludeArchived bool `json:"include_archived,omitempty" doc:"Include archived notes in results (default false)."`
}

// KnowledgeSummary is a lightweight note listing entry.
type KnowledgeSummary struct {
	Title   string   `json:"title" doc:"Display title."`
	Slug    string   `json:"slug" doc:"Note slug."`
	Summary string   `json:"summary" doc:"One-line summary."`
	Tags    []string `json:"tags" doc:"Tags."`
	Updated string   `json:"updated" doc:"Last-updated date."`
	Status  string   `json:"status" doc:"Note status."`
}

// ListKnowledgeOutput is the output for the list_knowledge tool.
type ListKnowledgeOutput struct {
	Notes         []KnowledgeSummary `json:"notes" doc:"Listed notes sorted by title."`
	Total         int                `json:"total" doc:"Total number of notes (excluding archived unless requested)."`
	DanglingTotal int                `json:"dangling_total" doc:"Number of unique unresolved [[wikilink]] targets across all notes."`
}

// LintKnowledgeInput is the input for the lint_knowledge tool.
type LintKnowledgeInput struct{}

// LintKnowledgeOutput is the output for the lint_knowledge tool.
type LintKnowledgeOutput struct {
	Total         int      `json:"total" doc:"Total number of notes."`
	Orphans       []string `json:"orphans" doc:"Slugs of notes with no backlinks and no outlinks."`
	DanglingLinks []string `json:"dangling_links" doc:"Unique unresolved [[wikilink]] targets across all notes."`
	Archived      []string `json:"archived" doc:"Slugs of archived notes."`
	BrokenIndex   bool     `json:"broken_index" doc:"True if index.md is missing or its note count differs from the actual count."`
	Issues        []string `json:"issues" doc:"Additional issues found (e.g. missing titles)."`
}

// ------------------- helpers --------------------------------------------------

// serializeNote rebuilds Raw from Frontmatter + Body and returns the full
// markdown bytes ready for writing.
func serializeNote(note *KnowledgeNote) []byte {
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.WriteString(fmt.Sprintf("title: %s\n", quoteYAML(note.Frontmatter.Title)))
	if len(note.Frontmatter.Aliases) > 0 {
		builder.WriteString("aliases:\n")
		for _, a := range note.Frontmatter.Aliases {
			builder.WriteString(fmt.Sprintf("  - %s\n", quoteYAML(a)))
		}
	}
	if len(note.Frontmatter.Tags) > 0 {
		builder.WriteString("tags:\n")
		for _, t := range note.Frontmatter.Tags {
			builder.WriteString(fmt.Sprintf("  - %s\n", quoteYAML(t)))
		}
	}
	builder.WriteString(fmt.Sprintf("created: %s\n", quoteYAML(note.Frontmatter.Created)))
	builder.WriteString(fmt.Sprintf("updated: %s\n", quoteYAML(note.Frontmatter.Updated)))
	if note.Frontmatter.Status != "" {
		builder.WriteString(fmt.Sprintf("status: %s\n", quoteYAML(note.Frontmatter.Status)))
	}
	if note.Frontmatter.Confidence != "" {
		builder.WriteString(fmt.Sprintf("confidence: %s\n", quoteYAML(note.Frontmatter.Confidence)))
	}
	if len(note.Frontmatter.Sources) > 0 {
		builder.WriteString("sources:\n")
		for _, s := range note.Frontmatter.Sources {
			if s.URL != "" {
				builder.WriteString(fmt.Sprintf("  - url: %s\n", quoteYAML(s.URL)))
			}
			if s.Path != "" {
				builder.WriteString(fmt.Sprintf("  - path: %s\n", quoteYAML(s.Path)))
			}
		}
	}
	if note.Frontmatter.Summary != "" {
		builder.WriteString(fmt.Sprintf("summary: %s\n", quoteYAML(note.Frontmatter.Summary)))
	}
	if len(note.Frontmatter.Related) > 0 {
		builder.WriteString("related:\n")
		for _, r := range note.Frontmatter.Related {
			builder.WriteString(fmt.Sprintf("  - %s\n", quoteYAML(r)))
		}
	}
	builder.WriteString("---\n\n")
	builder.WriteString(note.Body)

	raw := builder.String()
	note.Raw = raw
	return []byte(raw)
}

// quoteYAML returns a YAML double-quoted string, escaping special characters.
func quoteYAML(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return "\"" + s + "\""
}

// ------------------- tool handlers --------------------------------------------

const knowledgeLogEmoji = "📚 [knowledge]"

// createKnowledgeTools builds the eight knowledge-base tools and returns them
// as a []tool.Tool slice ready to append to an ADK agent's tool list.
// dir is the knowledge directory path; if empty, "./knowledge" is used.
// Each handler builds a fresh index at call time.
func createKnowledgeTools(log LogFunc, dir string) ([]tool.Tool, error) {
	var tools []tool.Tool

	// 1. save_knowledge
	saveTool, err := newDocTool(functiontool.Config{
		Name:        "save_knowledge",
		Description: "Save a new knowledge note. The title is slugified into a filename. [[wikilinks]] in the content are resolved against existing notes; unresolved targets are reported as dangling links.",
	}, func(ctx agent.Context, input SaveKnowledgeInput) (SaveKnowledgeOutput, error) {
		if input.Title == "" {
			return SaveKnowledgeOutput{}, fmt.Errorf("title is required")
		}
		if input.Content == "" {
			return SaveKnowledgeOutput{}, fmt.Errorf("content is required")
		}

		slug := Slugify(input.Title)
		if slug == "note" && input.Title != "note" {
			return SaveKnowledgeOutput{}, fmt.Errorf("title %q produced invalid slug", input.Title)
		}

		// Check if note already exists.
		idx, err := BuildKnowledgeIndex(dir)
		if err != nil {
			return SaveKnowledgeOutput{}, fmt.Errorf("building index: %w", err)
		}
		if _, ok := idx.BySlug[slug]; ok {
			return SaveKnowledgeOutput{}, fmt.Errorf("note %q already exists (use update_knowledge to modify)", slug)
		}

		today := time.Now().Format("2006-01-02")
		status := input.Status
		if status == "" {
			status = "draft"
		}

		// Parse sources from string URLs.
		var sources []KnowledgeSource
		for _, s := range input.Sources {
			sources = append(sources, KnowledgeSource{URL: s})
		}

		note := &KnowledgeNote{
			Slug: slug,
			Path: notePath(dir, slug),
			Frontmatter: KnowledgeFrontmatter{
				Title:      input.Title,
				Aliases:    input.Aliases,
				Tags:       input.Tags,
				Created:    today,
				Updated:    today,
				Status:     status,
				Confidence: input.Confidence,
				Sources:    sources,
			},
			Body: input.Content,
		}
		serializeNote(note)

		if err := SaveNote(dir, note); err != nil {
			return SaveKnowledgeOutput{}, fmt.Errorf("saving note: %w", err)
		}

		// Resolve dangling links from the saved note.
		outlinks := ExtractWikilinks(note.Body)
		var dangling []string
		for _, target := range outlinks {
			if _, ok := ResolveTarget(idx, target); !ok {
				dangling = append(dangling, target)
			}
		}

		// Rebuild index and update index/log.
		if newIdx, err := BuildKnowledgeIndex(dir); err == nil {
			_ = UpdateIndexFile(dir, newIdx)
		}
		_ = AppendLog(dir, "save", input.Title)

		log(fmt.Sprintf("%s saved note %q", knowledgeLogEmoji, slug))

		return SaveKnowledgeOutput{
			Slug:          slug,
			Path:          note.Path,
			DanglingLinks: dangling,
			Message:       fmt.Sprintf("Note %q saved successfully.", slug),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, saveTool)

	// 2. recall_knowledge
	recallTool, err := newDocTool(functiontool.Config{
		Name:        "recall_knowledge",
		Description: "Recall a knowledge note by slug, basename, or alias. Returns full body, backlinks, related notes, and any dangling [[wikilinks]] in the note body.",
	}, func(ctx agent.Context, input RecallKnowledgeInput) (RecallKnowledgeOutput, error) {
		if input.Name == "" {
			return RecallKnowledgeOutput{}, fmt.Errorf("name is required")
		}

		idx, err := BuildKnowledgeIndex(dir)
		if err != nil {
			return RecallKnowledgeOutput{}, fmt.Errorf("building index: %w", err)
		}

		note, ok := ResolveTarget(idx, input.Name)
		if !ok {
			return RecallKnowledgeOutput{}, fmt.Errorf("note %q not found", input.Name)
		}

		// Dangling links from note body.
		outlinks := ExtractWikilinks(note.Body)
		var dangling []string
		for _, target := range outlinks {
			if _, ok := ResolveTarget(idx, target); !ok {
				dangling = append(dangling, target)
			}
		}

		// Source URLs.
		var sourceURLs []string
		for _, s := range note.Frontmatter.Sources {
			if s.URL != "" {
				sourceURLs = append(sourceURLs, s.URL)
			}
		}

		return RecallKnowledgeOutput{
			Title:         note.Frontmatter.Title,
			Slug:          note.Slug,
			Content:       sanitizeContextContent(note.Body),
			Summary:       note.Frontmatter.Summary,
			Backlinks:     idx.Backlinks[note.Slug],
			Related:       note.Frontmatter.Related,
			Sources:       sourceURLs,
			Tags:          note.Frontmatter.Tags,
			Updated:       note.Frontmatter.Updated,
			Status:        note.Frontmatter.Status,
			DanglingLinks: dangling,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, recallTool)

	// 3. search_knowledge
	searchTool, err := newDocTool(functiontool.Config{
		Name:        "search_knowledge",
		Description: "Search knowledge notes by case-insensitive substring over title, aliases, tags, summary, and body. Optional tag filter requires ALL tags to match.",
	}, func(ctx agent.Context, input SearchKnowledgeInput) (SearchKnowledgeOutput, error) {
		if input.Query == "" {
			return SearchKnowledgeOutput{}, fmt.Errorf("query is required")
		}

		idx, err := BuildKnowledgeIndex(dir)
		if err != nil {
			return SearchKnowledgeOutput{}, fmt.Errorf("building index: %w", err)
		}

		notes := SearchKnowledge(idx, input.Query, input.Tags, input.IncludeArchived)

		var results []KnowledgeSearchResult
		for _, n := range notes {
			snippet := firstSnippet(n.Body, input.Query)
			results = append(results, KnowledgeSearchResult{
				Title:   n.Frontmatter.Title,
				Slug:    n.Slug,
				Summary: n.Frontmatter.Summary,
				Tags:    n.Frontmatter.Tags,
				Updated: n.Frontmatter.Updated,
				Status:  n.Frontmatter.Status,
				Snippet: sanitizeContextContent(snippet),
			})
		}

		return SearchKnowledgeOutput{Results: results}, nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, searchTool)

	// 4. update_knowledge
	updateTool, err := newDocTool(functiontool.Config{
		Name:        "update_knowledge",
		Description: "Update an existing knowledge note. Replaces body and/or frontmatter fields. If content is empty and append_text is set, append_text is appended to the existing body.",
	}, func(ctx agent.Context, input UpdateKnowledgeInput) (UpdateKnowledgeOutput, error) {
		if input.Name == "" {
			return UpdateKnowledgeOutput{}, fmt.Errorf("name is required")
		}

		idx, err := BuildKnowledgeIndex(dir)
		if err != nil {
			return UpdateKnowledgeOutput{}, fmt.Errorf("building index: %w", err)
		}

		note, ok := ResolveTarget(idx, input.Name)
		if !ok {
			return UpdateKnowledgeOutput{}, fmt.Errorf("note %q not found", input.Name)
		}

		today := time.Now().Format("2006-01-02")
		note.Frontmatter.Updated = today

		if input.Content != "" {
			note.Body = input.Content
		} else if input.AppendText != "" {
			note.Body += "\n" + input.AppendText
		}
		if input.Tags != nil {
			note.Frontmatter.Tags = input.Tags
		}
		if input.Confidence != "" {
			note.Frontmatter.Confidence = input.Confidence
		}
		if input.Aliases != nil {
			note.Frontmatter.Aliases = input.Aliases
		}
		if input.Status != "" {
			note.Frontmatter.Status = input.Status
		}
		if input.Sources != nil {
			var sources []KnowledgeSource
			for _, s := range input.Sources {
				sources = append(sources, KnowledgeSource{URL: s})
			}
			note.Frontmatter.Sources = sources
		}

		serializeNote(note)

		if err := UpdateNote(dir, note); err != nil {
			return UpdateKnowledgeOutput{}, fmt.Errorf("updating note: %w", err)
		}

		// Resolve dangling links.
		outlinks := ExtractWikilinks(note.Body)
		var dangling []string
		for _, target := range outlinks {
			if _, ok := ResolveTarget(idx, target); !ok {
				dangling = append(dangling, target)
			}
		}

		if newIdx, err := BuildKnowledgeIndex(dir); err == nil {
			_ = UpdateIndexFile(dir, newIdx)
		}
		_ = AppendLog(dir, "update", note.Frontmatter.Title)

		log(fmt.Sprintf("%s updated note %q", knowledgeLogEmoji, note.Slug))

		return UpdateKnowledgeOutput{
			Slug:          note.Slug,
			Path:          note.Path,
			DanglingLinks: dangling,
			Message:       fmt.Sprintf("Note %q updated successfully.", note.Slug),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, updateTool)

	// 5. link_knowledge
	linkTool, err := newDocTool(functiontool.Config{
		Name:        "link_knowledge",
		Description: "Link two knowledge notes by adding a [[wikilink]] from one to the other. If the target does not exist, the link is NOT written and the target is reported as dangling.",
	}, func(ctx agent.Context, input LinkKnowledgeInput) (LinkKnowledgeOutput, error) {
		if input.From == "" || input.To == "" {
			return LinkKnowledgeOutput{}, fmt.Errorf("from and to are required")
		}

		idx, err := BuildKnowledgeIndex(dir)
		if err != nil {
			return LinkKnowledgeOutput{}, fmt.Errorf("building index: %w", err)
		}

		fromNote, ok := ResolveTarget(idx, input.From)
		if !ok {
			return LinkKnowledgeOutput{}, fmt.Errorf("source note %q not found", input.From)
		}

		// Resolve target.
		targetNote, targetOK := ResolveTarget(idx, input.To)
		if !targetOK {
			return LinkKnowledgeOutput{
				Slug:     fromNote.Slug,
				To:       input.To,
				Dangling: []string{input.To},
				Message:  "target does not exist - ask the user whether to create it",
			}, nil
		}

		label := input.Label
		if label == "" {
			label = targetNote.Frontmatter.Title
		}
		// Link by slug (the resolvable identifier), with the title as label.
		linkLine := fmt.Sprintf("- [[%s|%s]]", targetNote.Slug, label)

		// Check if already linked.
		if strings.Contains(fromNote.Body, "[["+targetNote.Slug) ||
			strings.Contains(fromNote.Body, "[["+targetNote.Frontmatter.Title) {
			return LinkKnowledgeOutput{
				Slug:    fromNote.Slug,
				To:      targetNote.Slug,
				Message: "link already exists",
			}, nil
		}

		// Append link under "## Related" section if present, otherwise create it.
		if strings.Contains(fromNote.Body, "## Related") {
			fromNote.Body = appendAfterSection(fromNote.Body, "## Related", linkLine)
		} else {
			fromNote.Body += "\n\n## Related\n\n" + linkLine + "\n"
		}

		today := time.Now().Format("2006-01-02")
		fromNote.Frontmatter.Updated = today
		serializeNote(fromNote)

		if err := UpdateNote(dir, fromNote); err != nil {
			return LinkKnowledgeOutput{}, fmt.Errorf("updating note: %w", err)
		}

		if newIdx, err := BuildKnowledgeIndex(dir); err == nil {
			_ = UpdateIndexFile(dir, newIdx)
		}
		_ = AppendLog(dir, "link", fromNote.Frontmatter.Title)

		log(fmt.Sprintf("%s linked %q -> %q", knowledgeLogEmoji, fromNote.Slug, targetNote.Slug))

		return LinkKnowledgeOutput{
			Slug:    fromNote.Slug,
			To:      targetNote.Slug,
			Message: fmt.Sprintf("Linked %q to %q.", fromNote.Slug, targetNote.Slug),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, linkTool)

	// 6. cite_knowledge
	citeTool, err := newDocTool(functiontool.Config{
		Name:        "cite_knowledge",
		Description: "Generate an anthropic-style footnote citation for a knowledge note.",
	}, func(ctx agent.Context, input CiteKnowledgeInput) (CiteKnowledgeOutput, error) {
		if input.Name == "" {
			return CiteKnowledgeOutput{}, fmt.Errorf("name is required")
		}

		idx, err := BuildKnowledgeIndex(dir)
		if err != nil {
			return CiteKnowledgeOutput{}, fmt.Errorf("building index: %w", err)
		}

		note, ok := ResolveTarget(idx, input.Name)
		if !ok {
			return CiteKnowledgeOutput{}, fmt.Errorf("note %q not found", input.Name)
		}

		source := "n/a"
		for _, s := range note.Frontmatter.Sources {
			if s.URL != "" {
				source = s.URL
				break
			}
		}

		accessed := time.Now().Format("2006-01-02")
		citation := fmt.Sprintf("[1] %s (%s.md), source: %s, accessed %s",
			note.Frontmatter.Title, note.Slug, source, accessed)

		excerpt := note.Body
		if len(excerpt) > 200 {
			excerpt = excerpt[:200]
		}

		return CiteKnowledgeOutput{
			Title:    note.Frontmatter.Title,
			Slug:     note.Slug,
			Excerpt:  excerpt,
			Citation: citation,
			Source:   source,
			Updated:  note.Frontmatter.Updated,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, citeTool)

	// 7. list_knowledge
	listTool, err := newDocTool(functiontool.Config{
		Name:        "list_knowledge",
		Description: "List all knowledge notes sorted by title, with a summary of dangling links across all notes.",
	}, func(ctx agent.Context, input ListKnowledgeInput) (ListKnowledgeOutput, error) {
		idx, err := BuildKnowledgeIndex(dir)
		if err != nil {
			return ListKnowledgeOutput{}, fmt.Errorf("building index: %w", err)
		}

		// Collect notes sorted by title.
		var allNotes []*KnowledgeNote
		for _, n := range idx.BySlug {
			allNotes = append(allNotes, n)
		}
		for i := 0; i < len(allNotes); i++ {
			for j := i + 1; j < len(allNotes); j++ {
				if allNotes[i].Frontmatter.Title > allNotes[j].Frontmatter.Title {
					allNotes[i], allNotes[j] = allNotes[j], allNotes[i]
				}
			}
		}

		var summaries []KnowledgeSummary
		total := 0
		for _, n := range allNotes {
			if !input.IncludeArchived && n.Frontmatter.Status == "archived" {
				continue
			}
			total++
			summaries = append(summaries, KnowledgeSummary{
				Title:   n.Frontmatter.Title,
				Slug:    n.Slug,
				Summary: n.Frontmatter.Summary,
				Tags:    n.Frontmatter.Tags,
				Updated: n.Frontmatter.Updated,
				Status:  n.Frontmatter.Status,
			})
		}

		return ListKnowledgeOutput{
			Notes:         summaries,
			Total:         total,
			DanglingTotal: len(idx.Dangling),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, listTool)

	// 8. lint_knowledge
	lintTool, err := newDocTool(functiontool.Config{
		Name:        "lint_knowledge",
		Description: "Health check for the knowledge base. Reports orphan notes (no backlinks AND no outlinks), dangling links, archived notes, and index file integrity.",
	}, func(ctx agent.Context, input LintKnowledgeInput) (LintKnowledgeOutput, error) {
		idx, err := BuildKnowledgeIndex(dir)
		if err != nil {
			return LintKnowledgeOutput{}, fmt.Errorf("building index: %w", err)
		}

		var orphans []string
		for slug, note := range idx.BySlug {
			outlinks := ExtractWikilinks(note.Body)
			backlinks := idx.Backlinks[slug]
			if len(backlinks) == 0 && len(outlinks) == 0 {
				orphans = append(orphans, slug)
			}
		}

		var danglingTargets []string
		for target := range idx.Dangling {
			danglingTargets = append(danglingTargets, target)
		}
		// Sort for determinism.
		for i := 0; i < len(danglingTargets); i++ {
			for j := i + 1; j < len(danglingTargets); j++ {
				if danglingTargets[i] > danglingTargets[j] {
					danglingTargets[i], danglingTargets[j] = danglingTargets[j], danglingTargets[i]
				}
			}
		}

		var archived []string
		var issues []string
		for slug, note := range idx.BySlug {
			if note.Frontmatter.Status == "archived" {
				archived = append(archived, slug)
			}
			if note.Frontmatter.Title == "" {
				issues = append(issues, fmt.Sprintf("%s: missing title", slug))
			}
		}

		// Check broken index.
		resolvedDir := knowledgeDir(dir)
		brokenIndex := false
		indexPath := filepath.Join(resolvedDir, "index.md")
		if _, statErr := os.Stat(indexPath); os.IsNotExist(statErr) {
			brokenIndex = true
			issues = append(issues, "index.md is missing, run any knowledge mutation to regenerate it")
		}

		return LintKnowledgeOutput{
			Total:         len(idx.BySlug),
			Orphans:       orphans,
			DanglingLinks: danglingTargets,
			Archived:      archived,
			BrokenIndex:   brokenIndex,
			Issues:        issues,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, lintTool)

	return tools, nil
}

// ------------------- snippet helper ------------------------------------------

// firstSnippet returns the first ~200 characters of body. If query is
// non-empty, returns the line containing the first match (up to 200 chars).
func firstSnippet(body, query string) string {
	query = strings.ToLower(query)
	if query != "" {
		lines := strings.Split(body, "\n")
		for _, line := range lines {
			if strings.Contains(strings.ToLower(line), query) {
				if len(line) > 200 {
					return line[:200]
				}
				return line
			}
		}
	}
	// Fall back to first 200 chars of body.
	if len(body) > 200 {
		return body[:200]
	}
	return body
}

// appendAfterSection appends text after a section header in the body.
func appendAfterSection(body, section, text string) string {
	lines := strings.Split(body, "\n")
	var result []string
	inserted := false
	for i := 0; i < len(lines); i++ {
		result = append(result, lines[i])
		if strings.TrimSpace(lines[i]) == section && !inserted {
			result = append(result, text)
			inserted = true
		}
	}
	return strings.Join(result, "\n")
}
