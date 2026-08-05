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

// readImageFromClipboard reads image bytes from the system clipboard,
// preferring PNG. It probes the same backends as copyToClipboard: wl-paste
// (Wayland), xclip (X11), xsel (X11). Returns the bytes and MIME type, or an
// error when no supported image is available. Text-only clipboards return an
// error so the caller can fall through to text paste.
func readImageFromClipboard() ([]byte, string, error) {
	// wl-paste (Wayland). --type image/png is the portable target; older
	// wl-paste builds accept image/* and return the preferred type.
	if _, err := exec.LookPath("wl-paste"); err == nil {
		for _, t := range []string{"image/png", "image/*"} {
			out, err := exec.Command("wl-paste", "--type", t).Output()
			if err == nil && len(out) > 0 {
				return out, "image/png", nil
			}
		}
	}

	// xclip (X11).
	if _, err := exec.LookPath("xclip"); err == nil {
		for _, t := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
			out, err := exec.Command("xclip", "-selection", "clipboard", "-t", t, "-o").Output()
			if err == nil && len(out) > 0 {
				return out, t, nil
			}
		}
	}

	// xsel (X11 alternative) exposes only a single target per call; probe
	// for the common image targets via --output.
	if _, err := exec.LookPath("xsel"); err == nil {
		for _, t := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
			out, err := exec.Command("xsel", "--clipboard", "--output", "--target", t).Output()
			if err == nil && len(out) > 0 {
				return out, t, nil
			}
		}
	}

	return nil, "", fmt.Errorf("no image in clipboard")
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
