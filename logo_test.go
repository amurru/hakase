package main

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestCleanLogoContent verifies the logo cleanup: truecolor sequences preserved,
// terminal-control artifacts stripped, blank lines trimmed.
func TestCleanLogoContent(t *testing.T) {
	content := "\n\n" +
		"\x1b[38;2;0;198;255m█\x1b[38;2;0;196;255m█\x1b[39m\n" +
		"\x1b[38;2;0;198;255m║\x1b[39m\n" +
		"\n" +
		"\x1b[0m\x1b[?25h\x1b[K\n"

	lines := cleanLogoContent(content)
	if len(lines) != 2 {
		t.Fatalf("want 2 cleaned logo lines, got %d: %q", len(lines), lines)
	}
	for i, want := range []string{"█", "║"} {
		if !strings.Contains(lines[i], want) {
			t.Fatalf("line %d missing glyph %q: %q", i, want, lines[i])
		}
		if !strings.Contains(lines[i], "\x1b[38;2;") {
			t.Fatalf("line %d lost its truecolor sequence: %q", i, lines[i])
		}
	}
	for _, l := range lines {
		for _, artifact := range []string{"\x1b[0m", "\x1b[?25h", "\x1b[K"} {
			if strings.Contains(l, artifact) {
				t.Fatalf("terminal-control artifact %q must be stripped: %q", artifact, l)
			}
		}
	}
}

func TestCleanLogoContentNoVisibleContent(t *testing.T) {
	if lines := cleanLogoContent("\n\x1b[0m\x1b[?25h\x1b[K\n"); lines != nil {
		t.Fatalf("artifact-only logo must return nil, got %q", lines)
	}
}

// TestEmbeddedLogoLoaded verifies the embedded logo.txt compiled into the
// binary cleans to the expected 6 art rows with no terminal-control artifacts.
func TestEmbeddedLogoLoaded(t *testing.T) {
	if len(startupLogoLines) != 6 {
		t.Fatalf("embedded logo should clean to 6 rows, got %d: %q", len(startupLogoLines), startupLogoLines)
	}
	for _, l := range startupLogoLines {
		for _, artifact := range []string{"\x1b[0m", "\x1b[?25h", "\x1b[K"} {
			if strings.Contains(l, artifact) {
				t.Fatalf("embedded logo retains artifact %q: %q", artifact, l)
			}
		}
	}
}

// TestStartupBannerShownAtBoot verifies the logo renders in the chat pane on a
// fresh boot and that /new clears it (the banner is program-start only).
func TestStartupBannerShownAtBoot(t *testing.T) {
	old := startupLogoLines
	startupLogoLines = []string{"\x1b[38;2;0;198;255m██╗\x1b[39m", "\x1b[38;2;0;198;255m██║\x1b[39m"}
	defer func() { startupLogoLines = old }()

	m := newModelWithSvc(t)
	if c := m.chatViewport.View(); !strings.Contains(c, "██╗") {
		t.Fatalf("startup banner should render in the chat pane:\n%s", c)
	}
	if c := m.chatViewport.View(); !strings.Contains(c, startupTagline) {
		t.Fatalf("startup banner should render the tagline underneath the logo:\n%s", c)
	}

	// /new must not re-show the banner (program-start only).
	m.newSession()
	m.renderChatViewport()
	if c := m.chatViewport.View(); strings.Contains(c, "██╗") {
		t.Fatalf("banner must not appear after /new:\n%s", c)
	}
	if c := m.chatViewport.View(); strings.Contains(c, startupTagline) {
		t.Fatalf("tagline must not appear after /new:\n%s", c)
	}
}

// TestStartupBannerClearedByFirstMessage verifies the banner hides once the
// conversation starts and that a session switch does not re-show it.
func TestStartupBannerClearedByFirstMessage(t *testing.T) {
	old := startupLogoLines
	startupLogoLines = []string{"\x1b[38;2;0;198;255m██╗\x1b[39m"}
	defer func() { startupLogoLines = old }()

	m := newTestModel(t)
	if c := m.chatViewport.View(); !strings.Contains(c, "██╗") {
		t.Fatalf("startup banner should render in the chat pane:\n%s", c)
	}

	// First user message: launchTurn hides the banner and shows the message.
	m.launchTurn("hello", nil)
	if c := m.chatViewport.View(); strings.Contains(c, "██╗") {
		t.Fatalf("banner must clear on the first user message:\n%s", c)
	}
	if c := m.chatViewport.View(); strings.Contains(c, startupTagline) {
		t.Fatalf("tagline must clear on the first user message:\n%s", c)
	}
	if c := m.chatViewport.View(); !strings.Contains(c, "hello") {
		t.Fatalf("user message should be visible after banner clears:\n%s", c)
	}
}

// TestStartupBannerHiddenWhenNoLogo verifies the empty-chat path stays empty
// when no logo is available (e.g. no embedded art).
func TestStartupBannerHiddenWhenNoLogo(t *testing.T) {
	old := startupLogoLines
	startupLogoLines = nil
	defer func() { startupLogoLines = old }()

	m := newModel(context.Background(), nil, nil, 100, true, "test-model", "")
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = *(model.(*appModel))
	if c := m.chatViewport.View(); strings.Contains(c, "\x1b[38;2;") {
		t.Fatalf("no banner expected without a loaded logo:\n%s", c)
	}
}
