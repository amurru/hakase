package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeWaylandPaste installs a fake wl-paste on PATH that serves PNG bytes for
// --type image/png and fails otherwise, so readImageFromClipboard exercises
// the real probing logic without a Wayland session.
func fakeWaylandPaste(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "--type" ] && [ "$2" = "image/png" ]; then
  printf '\211PNG\r\n\032\nFAKEDATA'
  exit 0
fi
exit 1
`
	path := filepath.Join(dir, "wl-paste")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	// Isolate PATH to the fake dir so no real clipboard tool can be found.
	t.Setenv("PATH", dir)
}

func TestReadImageFromClipboardWayland(t *testing.T) {
	fakeWaylandPaste(t)

	data, mimeType, err := readImageFromClipboard()
	if err != nil {
		t.Fatalf("readImageFromClipboard: %v", err)
	}
	if mimeType != "image/png" {
		t.Fatalf("mime = %q, want image/png", mimeType)
	}
	if string(data) != "\x89PNG\r\n\x1a\nFAKEDATA" {
		t.Fatalf("data = %q", string(data))
	}
}

func TestReadImageFromClipboardNoTool(t *testing.T) {
	t.Setenv("PATH", "")
	_, _, err := readImageFromClipboard()
	if err == nil {
		t.Fatal("expected an error when no clipboard tool is available")
	}
}

func TestReadImageFromClipboardEmptyOutput(t *testing.T) {
	// A fake wl-paste that always exits non-zero: no image -> error.
	dir := t.TempDir()
	script := "#!/bin/sh\nexit 1\n"
	path := filepath.Join(dir, "wl-paste")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	if _, _, err := readImageFromClipboard(); err == nil {
		t.Fatal("expected an error when the clipboard holds no image")
	}
}

func TestAddImageAttachmentChip(t *testing.T) {
	m := newTestModel(t)
	m.addImageAttachment([]byte("pngdata"), "image/png")
	if len(m.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(m.attachments))
	}
	a := m.attachments[0]
	if a.Kind != "image" || a.Label != "[image 1]" || a.MIME != "image/png" {
		t.Fatalf("attachment = %+v", a)
	}
	// Numbering continues across pastes.
	m.addImageAttachment([]byte("more"), "image/png")
	if m.attachments[1].Label != "[image 2]" {
		t.Fatalf("second image label = %q, want [image 2]", m.attachments[1].Label)
	}
}

func TestAddImageAttachmentTooLarge(t *testing.T) {
	m := newTestModel(t)
	big := make([]byte, maxAttachImageBytes+1)
	m.addImageAttachment(big, "image/png")
	if len(m.attachments) != 0 {
		t.Fatal("oversized pasted image must be rejected")
	}
	if !strings.Contains(strings.Join(m.logLines, "\n"), "too large") {
		t.Fatalf("rejection must log a hint, got %v", m.logLines)
	}
}
