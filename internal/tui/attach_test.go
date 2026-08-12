package tui

import (
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/session"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentWord(t *testing.T) {
	cases := []struct {
		in, word string
		start    int
	}{
		{"", "", 0},
		{"hello", "hello", 0},
		{"look at @fi", "@fi", 8},
		{"a b c", "c", 4},
		{"multi\nline @x", "@x", 11},
		{"tab\t@y", "@y", 4},
	}
	for _, c := range cases {
		word, start := currentWord(c.in)
		if word != c.word || start != c.start {
			t.Fatalf("currentWord(%q) = (%q, %d), want (%q, %d)", c.in, word, start, c.word, c.start)
		}
	}
}

func TestBuildMessagePartsTextAndImages(t *testing.T) {
	parts := buildMessageParts("look here", []attachment{
		{Kind: "file", Name: "f.txt", MIME: "text/plain", Data: []byte("file content")},
		{Kind: "image", Name: "shot", MIME: "image/png", Data: []byte("pngbytes")},
	})
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3", len(parts))
	}
	if parts[0].Text != "look here" {
		t.Fatalf("parts[0].Text = %q", parts[0].Text)
	}
	if parts[1].Text != "file content" {
		t.Fatalf("text file must be a text part, got %q", parts[1].Text)
	}
	if parts[2].InlineData == nil || parts[2].InlineData.MIMEType != "image/png" ||
		string(parts[2].InlineData.Data) != "pngbytes" {
		t.Fatalf("image must be an inline data part, got %+v", parts[2])
	}
}

func TestBuildMessagePartsSkipsMissingData(t *testing.T) {
	parts := buildMessageParts("", []attachment{{Kind: "image", MIME: "image/png"}})
	if len(parts) != 0 {
		t.Fatalf("empty-data attachments must be skipped, got %d parts", len(parts))
	}
}

func TestMimeTypeFor(t *testing.T) {
	cases := map[string]string{
		"a.png":     "image/png",
		"a.jpg":     "image/jpeg",
		"a.JPEG":    "image/jpeg",
		"a.webp":    "image/webp",
		"a.go":      "text/x-go",
		"a.md":      "text/markdown",
		"a.unknown": "text/plain",
		"noext":     "text/plain",
	}
	for path, want := range cases {
		if got := mimeTypeFor(path); got != want {
			t.Fatalf("mimeTypeFor(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestWalkWorkspaceFilesSkipsHiddenAndHeavy(t *testing.T) {
	dir := t.TempDir()
	mkdirOrFail(t, filepath.Join(dir, ".git"))
	mkdirOrFail(t, filepath.Join(dir, "src"))
	mkdirOrFail(t, filepath.Join(dir, "node_modules"))
	writeFileOrFail(t, filepath.Join(dir, "a.txt"), "a")
	writeFileOrFail(t, filepath.Join(dir, ".git", "config"), "x")
	writeFileOrFail(t, filepath.Join(dir, "src", "b.go"), "b")
	writeFileOrFail(t, filepath.Join(dir, "node_modules", "big.js"), "c")

	files := walkWorkspaceFiles(dir, 500)
	joined := strings.Join(files, ",")
	for _, want := range []string{"a.txt", "src/b.go"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("workspace walk missing %q, got %v", want, files)
		}
	}
	for _, bad := range []string{".git", "node_modules"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("workspace walk must skip %q, got %v", bad, files)
		}
	}
}

func TestAttachMentionAddsTextChip(t *testing.T) {
	dir := t.TempDir()
	writeFileOrFail(t, filepath.Join(dir, "notes.txt"), "hello world")
	t.Chdir(dir)

	m := newTestModel(t)
	m.input.SetValue("look at @notes.txt")
	m.input.CursorEnd()
	m.attachMention("notes.txt")

	if len(m.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(m.attachments))
	}
	a := m.attachments[0]
	if a.Kind != "file" || a.Name != "notes.txt" || a.Label != "@notes.txt" {
		t.Fatalf("attachment = %+v", a)
	}
	if got := m.input.Value(); got != "look at " {
		t.Fatalf("input after attach = %q, want %q", got, "look at ")
	}
}

func TestAttachMentionImageChipsNumbered(t *testing.T) {
	dir := t.TempDir()
	writeFileOrFail(t, filepath.Join(dir, "pic.png"), "fakepng")
	t.Chdir(dir)

	m := newTestModel(t)
	m.attachMention("pic.png")
	m.attachMention("pic.png")

	if len(m.attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(m.attachments))
	}
	if m.attachments[0].Label != "[image 1]" || m.attachments[1].Label != "[image 2]" {
		t.Fatalf("image labels = %q, %q", m.attachments[0].Label, m.attachments[1].Label)
	}
	if m.attachments[0].MIME != "image/png" {
		t.Fatalf("mime = %q, want image/png", m.attachments[0].MIME)
	}
}

func TestAttachMentionRejectsLargeTextFile(t *testing.T) {
	dir := t.TempDir()
	writeFileOrFail(t, filepath.Join(dir, "big.txt"), string(bytes.Repeat([]byte("x"), maxAttachTextBytes+1)))
	t.Chdir(dir)

	m := newTestModel(t)
	m.attachMention("big.txt")
	if len(m.attachments) != 0 {
		t.Fatal("oversized text file must be rejected")
	}
	found := false
	for _, l := range m.logLines {
		if strings.Contains(l, "too large") {
			found = true
		}
	}
	if !found {
		t.Fatalf("rejection must log a hint, got %v", m.logLines)
	}
}

func TestAttachMentionRejectsMissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	m := newTestModel(t)
	m.attachMention("nope.txt")
	if len(m.attachments) != 0 {
		t.Fatal("missing file must not attach")
	}
}

func TestAttachMentionRejectsOutsideSandbox(t *testing.T) {
	root := t.TempDir()
	sb := sandbox.LoadSandboxConfig(&sandbox.SandboxJSON{Mode: "paths", WorkspaceRoots: []string{root}})
	old := sandbox.CurrentSandbox
	sandbox.CurrentSandbox = sb
	t.Cleanup(func() { sandbox.CurrentSandbox = old })

	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeFileOrFail(t, outside, "s")

	m := newTestModel(t)
	m.attachMention(outside)
	if len(m.attachments) != 0 {
		t.Fatal("out-of-sandbox path must be rejected")
	}
}

func TestMentionMenuOpensOnAtWord(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("check @")
	m.input.CursorEnd()
	if !m.mentionMenuOpen() {
		t.Fatal("word starting with @ must open the mention menu")
	}
	if m.mentionMenuOpen() == false {
		t.Fatal("mentionMenuOpen should be false")
	}
}

func TestMentionMenuClosedWithoutAt(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("plain text")
	m.input.CursorEnd()
	if m.mentionMenuOpen() {
		t.Fatal("mention menu must not open without @")
	}
}

func TestMentionEnterSelectsAndClearsToken(t *testing.T) {
	dir := t.TempDir()
	writeFileOrFail(t, filepath.Join(dir, "alpha.txt"), "a")
	writeFileOrFail(t, filepath.Join(dir, "beta.go"), "b")
	t.Chdir(dir)

	m := newTestModel(t)
	m.input.SetValue("see @alp")
	m.input.CursorEnd()
	m.filterMentionCandidates()
	if len(m.mentionFiltered) != 1 || !strings.Contains(m.mentionFiltered[0], "alpha.txt") {
		t.Fatalf("mention filter = %v", m.mentionFiltered)
	}

	// Enter attaches the highlighted candidate and removes the @token.
	model, cmd := m.Update(keyMsg("enter"))
	mm := model.(*AppModel)
	if cmd != nil {
		t.Fatalf("mention select returned cmd %v", cmd)
	}
	if len(mm.attachments) != 1 || mm.attachments[0].Name != "alpha.txt" {
		t.Fatalf("attachments = %+v", mm.attachments)
	}
	if got := mm.input.Value(); got != "see " {
		t.Fatalf("input after mention select = %q, want %q", got, "see ")
	}
}

func TestMentionEscClearsToken(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("see @alp")
	m.input.CursorEnd()
	model, _ := m.Update(keyMsg("esc"))
	mm := model.(*AppModel)
	if got := mm.input.Value(); got != "see " {
		t.Fatalf("esc must clear the @token, got %q", got)
	}
	if len(mm.attachments) != 0 {
		t.Fatal("esc must not attach anything")
	}
}

func TestRemoveLastAttachment(t *testing.T) {
	m := newTestModel(t)
	m.attachments = []attachment{{ID: 1, Label: "[image 1]"}, {ID: 2, Label: "@a.txt"}}
	m.removeLastAttachment()
	if len(m.attachments) != 1 || m.attachments[0].Label != "[image 1]" {
		t.Fatalf("attachments after remove = %+v", m.attachments)
	}
}

func TestChipRowRendersLabels(t *testing.T) {
	m := newTestModel(t)
	m.attachments = []attachment{
		{Kind: "image", Label: "[image 1]"},
		{Kind: "file", Label: "@notes.txt"},
	}
	row := m.chipRow()
	for _, want := range []string{"[image 1]", "@notes.txt"} {
		if !strings.Contains(row, want) {
			t.Fatalf("chip row missing %q, got %q", want, row)
		}
	}
}

func TestBackspaceRemovesChipOnEmptyInput(t *testing.T) {
	m := newTestModel(t)
	m.attachments = []attachment{{ID: 1, Label: "[image 1]"}}
	model, _ := m.Update(keyMsg("backspace"))
	mm := model.(*AppModel)
	if len(mm.attachments) != 0 {
		t.Fatal("backspace on empty input must remove the last chip")
	}
}

func TestMessageToContentWithAttachments(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "img.png")
	writeFileOrFail(t, img, "pngbytes")
	txt := filepath.Join(dir, "note.txt")
	writeFileOrFail(t, txt, "file text")

	c := hctx.MessageToContent(session.Message{
		Role:    "user",
		Content: "see",
		Attachments: []session.AttachmentRef{
			{Name: "img.png", Path: img, MIME: "image/png", Label: "[image 1]"},
			{Name: "note.txt", Path: txt, MIME: "text/plain", Label: "@note.txt"},
		},
	})
	if len(c.Parts) != 3 {
		t.Fatalf("parts = %d, want 3", len(c.Parts))
	}
	wantSee := hctx.WrapUntrustedData("see")
	if c.Parts[0].Text != wantSee {
		t.Fatalf("parts[0] = %q, want wrapped %q", c.Parts[0].Text, wantSee)
	}
	if c.Parts[1].InlineData == nil || string(c.Parts[1].InlineData.Data) != "pngbytes" {
		t.Fatalf("image attachment must rebuild as inline data, got %+v", c.Parts[1])
	}
	wantFileText := hctx.WrapUntrustedData("file text")
	if c.Parts[2].Text != wantFileText {
		t.Fatalf("text attachment must rebuild as text part, got %q, want wrapped %q", c.Parts[2].Text, wantFileText)
	}
}

func TestMessageToContentSkipsMissingAttachmentFile(t *testing.T) {
	c := hctx.MessageToContent(session.Message{
		Role:        "user",
		Content:     "hi",
		Attachments: []session.AttachmentRef{{Name: "gone.png", Path: filepath.Join(t.TempDir(), "gone.png"), MIME: "image/png"}},
	})
	wantHi := hctx.WrapUntrustedData("hi")
	if len(c.Parts) != 1 || c.Parts[0].Text != wantHi {
		t.Fatalf("missing attachment file must be skipped, got %+v, want wrapped %q", c.Parts, wantHi)
	}
}

func TestCurrentUserMessageMatches(t *testing.T) {
	att := []session.AttachmentRef{{Name: "x", Path: "/x", MIME: "image/png"}}

	cases := []struct {
		msg  session.Message
		text string
		want bool
	}{
		{session.Message{Role: "user", Content: "hi"}, "hi", true},
		{session.Message{Role: "user", Content: "hi"}, "hi\nattached", false}, // different text, no attachments
		{session.Message{Role: "user", Content: "hi", Attachments: att}, "hi\nfile content", true},
		{session.Message{Role: "user", Content: "", Attachments: att}, "", true}, // image-only
		{session.Message{Role: "user", Content: "old"}, "new", false},
		{session.Message{Role: "user", Content: "old", Attachments: att}, "different", false},
	}
	for _, c := range cases {
		if got := hctx.CurrentUserMessageMatches(c.msg, c.text); got != c.want {
			t.Fatalf("currentUserMessageMatches(%q, %q) = %v, want %v", c.msg.Content, c.text, got, c.want)
		}
	}
}

// mustWriteFile creates a file with the given content, failing the test on
// error. (Named to avoid clashing with pathconfine_test.go helpers.)
func writeFileOrFail(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// mkdirQuiet creates a directory, failing the test on error.
func mkdirOrFail(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
}
