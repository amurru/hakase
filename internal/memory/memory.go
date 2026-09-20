// Package memory persists agent-written auto-memory: short typed notes the
// orchestrator accumulates across sessions via the remember/forget_memory
// tools and that are injected back into a session's first model call. The
// file lives at ~/.hakase/memory/notes.json (0600). The package is
// intentionally a leaf (only internal/config and internal/util) so the CLI's
// `hakase memory` command and the web handlers can inspect and mutate notes
// without importing the agent runtime; the tool definitions themselves live
// in internal/agent (internal/memory must not import it - internal/context
// consumes the rendered block through a provider func, and memory → agent →
// context would cycle).
package memory

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// FileName is the store file name under the hakase home.
const FileName = "memory/notes.json"

// MaxNoteChars caps one note's content at write time. Memory notes are
// one-liners; the cap keeps a single write from dominating the injected
// block.
const MaxNoteChars = 2000

// Version is the on-disk schema version.
const Version = 1

// Categories is the fixed category enum in render order. `user`/`feedback`
// are about the human and their preferences, `project` about the codebase,
// `lesson` about what the agent learned the hard way. Reference material
// deliberately has no category here - it belongs in the knowledge wiki.
var Categories = []string{"user", "feedback", "project", "lesson"}

// ValidCategory reports whether category is one of the typed categories.
func ValidCategory(category string) bool {
	for _, c := range Categories {
		if c == category {
			return true
		}
	}
	return false
}

// Note is one memory entry. Project carries the absolute project root the
// note was written under ("" = global); injection shows a note when its
// project is empty or equals the current session's project root.
type Note struct {
	ID        string    `json:"id"`
	Category  string    `json:"category"`
	Content   string    `json:"content"`
	Project   string    `json:"project,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// State is the on-disk document.
type State struct {
	Version int    `json:"version"`
	Notes   []Note `json:"notes,omitempty"`
}

// NormalizeContent trims a note's content and rejects it when empty or over
// MaxNoteChars. Shared by the remember tool and the CLI add path so both
// enforce the same shape.
func NormalizeContent(content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("content is empty")
	}
	if len(content) > MaxNoteChars {
		return "", fmt.Errorf("content is %d chars; the cap is %d - keep notes to one line each", len(content), MaxNoteChars)
	}
	return content, nil
}

// SelectForProject returns the notes visible to a session rooted at project:
// global notes (Project == "") plus notes stamped with exactly that root.
// Output order is category groups in Categories order, newest-updated first
// within a group - the same order RenderBlock presents.
func SelectForProject(st State, project string) []Note {
	var selected []Note
	for _, cat := range Categories {
		var group []Note
		for _, n := range st.Notes {
			if n.Category != cat {
				continue
			}
			if n.Project != "" && n.Project != project {
				continue
			}
			group = append(group, n)
		}
		sort.SliceStable(group, func(i, j int) bool {
			return group[i].UpdatedAt.After(group[j].UpdatedAt)
		})
		selected = append(selected, group...)
	}
	return selected
}

// RenderBlock renders the injected AUTO MEMORY block: category groups in
// fixed order, note ids so the model can update or remove what it sees.
// The block is hard-capped at maxChars; notes that no longer fit are dropped
// from the tail and replaced by a truncation line so the model knows the
// list is partial. Returns "" when there is nothing to inject.
func RenderBlock(notes []Note, maxChars int) string {
	if len(notes) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = 1 << 30
	}

	var b strings.Builder
	b.WriteString("AUTO MEMORY - notes you (or a previous session) saved about this user and project; ids are in parentheses (update with remember{id, content}, remove with forget_memory):")
	used := b.Len()
	omitted := 0
	for _, cat := range Categories {
		groupHeader := false
		for _, n := range notes {
			if n.Category != cat {
				continue
			}
			line := fmt.Sprintf("\n- [%s] (%s) %s", n.Category, n.ID, strings.ReplaceAll(n.Content, "\n", " "))
			cost := len(line)
			if !groupHeader {
				cost += len(cat) + 2
			}
			if used+cost > maxChars {
				omitted++
				continue
			}
			if !groupHeader {
				b.WriteString(fmt.Sprintf("\n[%s]", cat))
				used += len(cat) + 2
				groupHeader = true
			}
			b.WriteString(line)
			used += len(line)
		}
	}
	if omitted > 0 {
		// Appended even when it nudges the block a line over the cap: the
		// model must know the list is partial, that matters more than the
		// last ~130 chars of budget.
		b.WriteString(fmt.Sprintf("\n(%d more note(s) omitted: block capped at %d chars - prune with forget_memory if these matter)", omitted, maxChars))
	}
	return b.String()
}
