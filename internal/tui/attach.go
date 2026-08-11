package tui

import (
	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/util"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"google.golang.org/genai"
)

// attach.go implements message attachments: files selected via the "@"
// mention menu and images pasted from the clipboard. Attachments render as
// chips in a row above the input pane ("@file.go", "[image 1]") and are
// converted into genai content parts when the message is submitted.
//
// The mention menu follows the session-list modal pattern: a derived open
// condition (the word being typed starts with "@"), a filtered candidate
// list from a bounded workspace walk, and arrow-key navigation.

const (
	// maxMentionFiles bounds the workspace walk for the @ file menu.
	maxMentionFiles = 500
	// maxAttachTextBytes caps text files attached via @ (warn + skip above).
	maxAttachTextBytes = 200 * 1024
	// maxAttachImageBytes caps images attached via @ or paste.
	maxAttachImageBytes = 10 * 1024 * 1024
)

// attachment is a single file or image attached to the message being
// composed. Data holds the content read at selection time; Label is the chip
// text ("@name.go" or "[image N]").
type attachment struct {
	ID    int
	Kind  string // "file" or "image"
	Name  string // display name (basename)
	Path  string // resolved absolute path ("" for pasted clipboard images)
	MIME  string
	Data  []byte
	Label string
}

// imageMimes is the set of MIME types treated as images (inline data parts).
var imageMimes = map[string]bool{
	"image/png":     true,
	"image/jpeg":    true,
	"image/gif":     true,
	"image/webp":    true,
	"image/bmp":     true,
	"image/x-icon":  true,
	"image/svg+xml": true,
}

// mimeTypeFor guesses a MIME type from a file path extension, falling back
// to text/plain for unknown or extensionless files.
func mimeTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	}
	if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
		// Strip any "; charset=..." parameter so imageMimes and persistence
		// see a clean MIME type.
		if i := strings.IndexByte(t, ';'); i >= 0 {
			t = strings.TrimSpace(t[:i])
		}
		return t
	}
	return "text/plain"
}

// buildMessageParts converts the input text plus the current attachments into
// genai content parts: text verbatim, text files as text parts, images as
// inline data parts. Attachments whose data is missing are skipped.
func buildMessageParts(text string, atts []attachment) []*genai.Part {
	var parts []*genai.Part
	if strings.TrimSpace(text) != "" {
		parts = append(parts, genai.NewPartFromText(text))
	}
	for _, a := range atts {
		if len(a.Data) == 0 {
			continue
		}
		if imageMimes[a.MIME] {
			parts = append(parts, genai.NewPartFromBytes(a.Data, a.MIME))
		} else {
			parts = append(parts, genai.NewPartFromText(string(a.Data)))
		}
	}
	return parts
}

// attachmentTokens estimates the token cost of an attachment for session
// token accounting (mirrors tokenutil's flat image estimate).
func attachmentTokens(a attachment) int {
	if imageMimes[a.MIME] {
		return 1200
	}
	return util.EstimateTokens(string(a.Data))
}

// currentWord returns the word being typed at the end of the input (the text
// after the last whitespace) and the byte offset where it starts.
func currentWord(s string) (word string, start int) {
	start = len(s)
	for start > 0 {
		ch := s[start-1]
		if ch == ' ' || ch == '\n' || ch == '\t' || ch == '\r' {
			break
		}
		start--
	}
	return s[start:], start
}

// mentionMenuOpen reports whether the @ file menu should be visible: the
// input is focused, the agent is idle, and the word being typed starts with
// "@".
func (m *AppModel) mentionMenuOpen() bool {
	if m.focus != inputFocus || m.IsProcessing {
		return false
	}
	word, _ := currentWord(m.input.Value())
	return strings.HasPrefix(word, "@")
}

// mentionCandidates returns the cached bounded workspace file listing used by
// the @ menu (lazily walked on first use).
func (m *AppModel) mentionCandidates() []string {
	if m.mentionFiles == nil {
		m.mentionFiles = walkWorkspaceFiles(".", maxMentionFiles)
	}
	return m.mentionFiles
}

// walkWorkspaceFiles walks root collecting relative file paths, skipping
// hidden and heavy directories, up to max entries.
func walkWorkspaceFiles(root string, max int) []string {
	var out []string
	root = filepath.Clean(root)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root {
				name := d.Name()
				if strings.HasPrefix(name, ".") ||
					name == "node_modules" || name == "vendor" ||
					name == "__pycache__" || name == ".venv" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		if len(out) >= max {
			return filepath.SkipAll
		}
		return nil
	})
	return out
}

// filterMentionCandidates recomputes the filtered @ file list from the
// current filter text (the word after "@").
func (m *AppModel) filterMentionCandidates() {
	word, _ := currentWord(m.input.Value())
	filter := ""
	if strings.HasPrefix(word, "@") {
		filter = strings.ToLower(word[1:])
	}
	m.mentionFiltered = m.mentionFiltered[:0]
	for _, f := range m.mentionCandidates() {
		if filter == "" || strings.Contains(strings.ToLower(f), filter) {
			m.mentionFiltered = append(m.mentionFiltered, f)
		}
	}
	if m.mentionMenuIndex >= len(m.mentionFiltered) {
		m.mentionMenuIndex = len(m.mentionFiltered) - 1
		if m.mentionMenuIndex < 0 {
			m.mentionMenuIndex = 0
		}
	}
}

// handleMentionMenuKey processes key presses while the @ file menu is open.
// Navigation and selection keys are intercepted; character keys fall through
// to the textarea so the filter updates naturally. Returns (cmd, handled).
func (m *AppModel) handleMentionMenuKey(key string) (tea.Cmd, bool) {
	word, start := currentWord(m.input.Value())
	if !strings.HasPrefix(word, "@") {
		return nil, false
	}

	switch key {
	case "up":
		if m.mentionMenuIndex > 0 {
			m.mentionMenuIndex--
		}
		return nil, true
	case "down":
		if m.mentionMenuIndex < len(m.mentionFiltered)-1 {
			m.mentionMenuIndex++
		}
		return nil, true
	case "tab", "enter":
		if len(m.mentionFiltered) > 0 {
			idx := m.mentionMenuIndex
			if idx >= len(m.mentionFiltered) {
				idx = 0
			}
			m.attachMention(m.mentionFiltered[idx])
			return nil, true
		}
		// No match: let Enter fall through to normal submit.
		return nil, false
	case "esc":
		// Remove the partial @word from the input.
		val := m.input.Value()
		m.input.SetValue(val[:start])
		m.input.CursorEnd()
		return nil, true
	case "backspace", "ctrl+h":
		// Backspace over a lone "@" clears it (closes the menu); otherwise
		// the textarea deletes a filter character below.
		if word == "@" {
			val := m.input.Value()
			m.input.SetValue(val[:start])
			m.input.CursorEnd()
			return nil, true
		}
		return nil, false
	}
	return nil, false
}

// attachMention attaches the selected file as a chip: it removes the partial
// @word from the input, resolves the path through the sandbox (read mode),
// reads the file with size caps, and appends an attachment entry.
func (m *AppModel) attachMention(path string) {
	// Remove the partial @word from the input.
	val := m.input.Value()
	_, start := currentWord(val)
	m.input.SetValue(val[:start])
	m.input.CursorEnd()

	// Resolve through the sandbox so out-of-workspace paths are rejected
	// before the file is read.
	resolved := path
	if sandbox.CurrentSandbox != nil {
		r, err := sandbox.CurrentSandbox.ResolveScopedPath(path, false)
		if err != nil {
			m.AppendLog("⚠ cannot attach " + path + ": " + err.Error())
			return
		}
		resolved = r
	} else if r, err := filepath.Abs(path); err == nil {
		resolved = r
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		m.AppendLog("⚠ cannot read " + path + ": " + err.Error())
		return
	}

	mt := mimeTypeFor(resolved)
	kind := "file"
	if imageMimes[mt] {
		kind = "image"
	}
	switch {
	case kind == "file" && len(data) > maxAttachTextBytes:
		m.AppendLog(fmt.Sprintf("⚠ %s is too large to attach (%d KB, max %d KB)",
			filepath.Base(resolved), len(data)/1024, maxAttachTextBytes/1024))
		return
	case kind == "image" && len(data) > maxAttachImageBytes:
		m.AppendLog(fmt.Sprintf("⚠ %s is too large to attach (%d MB, max %d MB)",
			filepath.Base(resolved), len(data)/(1024*1024), maxAttachImageBytes/(1024*1024)))
		return
	}

	label := "@" + filepath.Base(resolved)
	if kind == "image" {
		label = fmt.Sprintf("[image %d]", m.nextImageNumber())
	}
	m.attachments = append(m.attachments, attachment{
		ID:    len(m.attachments) + 1,
		Kind:  kind,
		Name:  filepath.Base(resolved),
		Path:  resolved,
		MIME:  mt,
		Data:  data,
		Label: label,
	})
}

// addImageAttachment appends a pasted clipboard image as a chip.
func (m *AppModel) addImageAttachment(data []byte, mimeType string) {
	if len(data) > maxAttachImageBytes {
		m.AppendLog(fmt.Sprintf("⚠ pasted image too large (%d MB, max %d MB)",
			len(data)/(1024*1024), maxAttachImageBytes/(1024*1024)))
		return
	}
	m.attachments = append(m.attachments, attachment{
		ID:    len(m.attachments) + 1,
		Kind:  "image",
		Name:  fmt.Sprintf("image %d", m.nextImageNumber()),
		Path:  "",
		MIME:  mimeType,
		Data:  data,
		Label: fmt.Sprintf("[image %d]", m.nextImageNumber()),
	})
}

// nextImageNumber returns the 1-based number for the next image chip.
func (m *AppModel) nextImageNumber() int {
	n := 0
	for _, a := range m.attachments {
		if a.Kind == "image" {
			n++
		}
	}
	return n + 1
}

// removeLastAttachment drops the most recently attached chip (backspace on an
// empty input).
func (m *AppModel) removeLastAttachment() {
	if len(m.attachments) == 0 {
		return
	}
	m.attachments = m.attachments[:len(m.attachments)-1]
}

// chipRow renders the attachment chips as a single dimmed line shown inside
// the input pane above the textarea.
func (m *AppModel) chipRow() string {
	if len(m.attachments) == 0 {
		return ""
	}
	var labels []string
	for _, a := range m.attachments {
		labels = append(labels, a.Label)
	}
	s := strings.Join(labels, "  ·  ")
	// Truncate to the input width so a long chip list cannot overflow.
	w := m.input.Width()
	if w > 4 && len(s) > w-4 {
		s = s[:w-4] + "…"
	}
	return chipStyle.Render(s)
}

// mentionMenuView renders the @ file menu overlay: the filtered workspace
// files with the highlighted selection marked.
func (m *AppModel) mentionMenuView() string {
	m.filterMentionCandidates()
	if len(m.mentionFiltered) == 0 {
		return menuBoxStyle.Render("  (no matching files)  ")
	}

	maxLines := 8
	var lines []string
	for i, f := range m.mentionFiltered {
		if i >= maxLines {
			lines = append(lines, "  …")
			break
		}
		marker := "  "
		if i == m.mentionMenuIndex {
			marker = "❯ "
		}
		name := f
		if len(name) > 40 {
			name = "…" + name[len(name)-39:]
		}
		lines = append(lines, marker+"@"+name)
	}
	return menuBoxStyle.Render(strings.Join(lines, "\n"))
}

// attachmentLabels renders the attachment chip labels for a message, used to
// display what was attached in the chat history.
func attachmentLabels(atts []attachment) string {
	var labels []string
	for _, a := range atts {
		labels = append(labels, a.Label)
	}
	return strings.Join(labels, " ")
}
