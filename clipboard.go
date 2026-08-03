package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
)

// copyToClipboard copies the given text to the system clipboard.
// It tries multiple backends in order: wl-copy (Wayland), xclip (X11),
// xsel (X11 alternative), and finally the atotto/clipboard Go library.
func copyToClipboard(text string) error {
	if text == "" {
		return nil
	}

	// Try wl-copy first (Wayland).
	if _, err := exec.LookPath("wl-copy"); err == nil {
		cmd := exec.Command("wl-copy")
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}

	// Try xclip (X11).
	if _, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command("xclip", "-selection", "clipboard")
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}

	// Try xsel (X11 alternative).
	if _, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.Command("xsel", "--clipboard", "--input")
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}

	// Fallback to atotto/clipboard (works on macOS and Windows natively).
	return clipboard.WriteAll(text)
}

// copyPaneContent copies the visible content of the focused output pane
// to the clipboard and returns a user-facing confirmation message.
func (m *appModel) copyFocusedPaneContent() string {
	var text string
	switch m.focus {
	case chatFocus:
		text = m.chatViewport.View()
	case logFocus:
		text = m.logViewport.View()
	case taskFocus:
		text = m.taskViewport.View()
	default:
		return ""
	}

	// Strip ANSI escape sequences so the clipboard contains plain text.
	text = ansi.Strip(text)

	if text == "" {
		return ""
	}

	if err := copyToClipboard(text); err != nil {
		return ""
	}
	return fmt.Sprintf("Copied %d chars to clipboard", len(text))
}
