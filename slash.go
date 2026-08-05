package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// slash.go implements the built-in slash command system: a registry of
// commands with names/aliases/descriptions and a run function, a parser that
// splits an input line starting with "/" into name + args, and the dispatch
// path that executes commands locally instead of sending them to the model.
//
// The command menu (typing "/" in the input) and its key handling live here
// too, following the session-list modal pattern (ui.go handleSessionListKey).

// SlashCommand describes a built-in slash command. Run receives the trimmed
// arguments after the command name and returns a tea.Cmd (often nil).
type SlashCommand struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string
	Run         func(m *appModel, args string) tea.Cmd
}

// builtinCommands is the slash command registry, rendered by the command menu
// and consulted by dispatch (case-insensitive name or alias match).
var builtinCommands = []SlashCommand{
	{
		Name:        "compact",
		Description: "Summarize the conversation to free context, continuing the same session",
		Usage:       "/compact [focus]",
		Run: func(m *appModel, args string) tea.Cmd {
			if m.isProcessing {
				m.appendLog("⚠ cannot compact while the agent is working")
				return nil
			}
			return m.compactSession(args)
		},
	},
	{
		Name:        "new",
		Description: "Start a fresh session (previous sessions stay resumable)",
		Usage:       "/new",
		Run: func(m *appModel, args string) tea.Cmd {
			if m.isProcessing {
				m.appendLog("⚠ cannot start a new session while the agent is working")
				return nil
			}
			return m.newSession()
		},
	},
	{
		Name:        "sessions",
		Aliases:     []string{"resume"},
		Description: "Open the session chooser to switch or resume a previous session",
		Usage:       "/sessions",
		Run: func(m *appModel, args string) tea.Cmd {
			if m.isProcessing {
				m.appendLog("⚠ cannot switch sessions while the agent is working")
				return nil
			}
			return m.toggleSessionList()
		},
	},
	{
		Name:        "board",
		Aliases:     []string{"tasks", "task"},
		Description: "Manage the task board: summary, list, new, get, update, done, fail, cancel, delete, archive, claim",
		Usage:       "/board <subcommand> [args]",
		Run: func(m *appModel, args string) tea.Cmd {
			return runBoardCommand(m, args)
		},
	},
	{
		Name:        "exit",
		Aliases:     []string{"quit"},
		Description: "Exit hakase",
		Usage:       "/exit",
		Run: func(m *appModel, args string) tea.Cmd {
			return tea.Quit
		},
	},
	{
		Name:        "help",
		Aliases:     []string{"?"},
		Description: "Show the keyboard shortcut and slash command reference",
		Usage:       "/help",
		Run: func(m *appModel, args string) tea.Cmd {
			m.showHelp = true
			return nil
		},
	},
}

// findSlashCommand returns the command registered under name or any of its
// aliases (case-insensitive), or nil when nothing matches.
func findSlashCommand(name string) *SlashCommand {
	for i := range builtinCommands {
		cmd := &builtinCommands[i]
		if strings.EqualFold(cmd.Name, name) {
			return cmd
		}
		for _, alias := range cmd.Aliases {
			if strings.EqualFold(alias, name) {
				return cmd
			}
		}
	}
	return nil
}

// parseSlashCommand splits an input line starting with "/" into the command
// name and trailing arguments. ok is false when the text does not start with
// "/" or contains nothing after the slash.
func parseSlashCommand(text string) (name, args string, ok bool) {
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "/"))
	if rest == "" {
		return "", "", false
	}
	parts := strings.SplitN(rest, " ", 2)
	name = strings.TrimSpace(parts[0])
	if name == "" {
		return "", "", false
	}
	if len(parts) == 2 {
		args = strings.TrimSpace(parts[1])
	}
	return name, args, true
}

// runSlashCommand executes a parsed slash command on the model. It returns the
// command to run (or nil). Unknown commands are logged and still consume the
// input so they never reach the model.
func runSlashCommand(m *appModel, name, args string) tea.Cmd {
	cmd := findSlashCommand(name)
	if cmd == nil {
		m.appendLog(fmt.Sprintf("unknown command: /%s (try /help)", name))
		return nil
	}
	if args != "" {
		m.appendLog(fmt.Sprintf("⚡ /%s %s", cmd.Name, args))
	} else {
		m.appendLog(fmt.Sprintf("⚡ /%s", cmd.Name))
	}
	return cmd.Run(m, args)
}

// commandMenuOpen reports whether the slash command menu should be visible:
// the input is focused, the agent is idle, and the input starts with "/".
func (m *appModel) commandMenuOpen() bool {
	return m.focus == inputFocus && !m.isProcessing && strings.HasPrefix(m.input.Value(), "/")
}

// commandMenuFilter returns the command-name filter derived from the input
// (everything after the leading "/").
func (m *appModel) commandMenuFilter() string {
	return strings.TrimPrefix(m.input.Value(), "/")
}

// filteredCommands returns the builtin commands whose name or alias starts
// with the current filter (prefix match, case-insensitive). Empty filter
// lists all commands.
func (m *appModel) filteredCommands() []SlashCommand {
	filter := strings.ToLower(strings.TrimSpace(m.commandMenuFilter()))
	if filter == "" {
		return builtinCommands
	}
	var out []SlashCommand
	for _, c := range builtinCommands {
		if strings.HasPrefix(strings.ToLower(c.Name), filter) {
			out = append(out, c)
			continue
		}
		for _, alias := range c.Aliases {
			if strings.HasPrefix(strings.ToLower(alias), filter) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// handleCommandMenuKey processes key presses while the command menu is open.
// Navigation (up/down), completion (tab), and execution (enter) are
// intercepted; character keys fall through to the textarea so the filter
// updates naturally. Returns (cmd, handled); when handled is false the caller
// continues normal key processing.
func (m *appModel) handleCommandMenuKey(key string) (tea.Cmd, bool) {
	filtered := m.filteredCommands()
	value := m.input.Value()

	switch key {
	case "up", "k":
		if m.commandMenuIndex > 0 {
			m.commandMenuIndex--
		}
		return nil, true
	case "down", "j":
		if m.commandMenuIndex < len(filtered)-1 {
			m.commandMenuIndex++
		}
		return nil, true
	case "tab":
		// Complete the highlighted command name into the input (no execute).
		if len(filtered) > 0 {
			idx := m.commandMenuIndex
			if idx >= len(filtered) {
				idx = 0
			}
			m.input.SetValue("/" + filtered[idx].Name)
			m.input.CursorEnd()
			m.commandMenuIndex = 0
		}
		return nil, true
	case "enter":
		// A fully typed command (name, possibly with args) runs as-is.
		if name, args, ok := parseSlashCommand(value); ok && findSlashCommand(name) != nil {
			m.input.Reset()
			cmd := runSlashCommand(m, name, args)
			return cmd, true
		}
		// Otherwise run the highlighted suggestion.
		if len(filtered) > 0 {
			idx := m.commandMenuIndex
			if idx >= len(filtered) {
				idx = 0
			}
			m.input.Reset()
			cmd := runSlashCommand(m, filtered[idx].Name, "")
			return cmd, true
		}
		// No match: fall through to normal submit (the raw text is sent).
		return nil, false
	case "esc":
		m.input.Reset()
		m.commandMenuIndex = 0
		return nil, true
	case "backspace", "ctrl+h":
		// Backspace over a lone "/" closes the menu; otherwise the textarea
		// deletes a filter character below.
		if value == "/" {
			m.input.Reset()
			m.commandMenuIndex = 0
			return nil, true
		}
		return nil, false
	}
	return nil, false
}

// commandMenuView renders the slash command menu overlay: the filtered
// command list with the highlighted selection marked. The box spans the
// input pane width so command descriptions are readable, wrapping long
// descriptions across lines with a hanging indent.
func (m *appModel) commandMenuView() string {
	filtered := m.filteredCommands()
	if len(filtered) == 0 {
		return menuBoxStyle.Render("  (no matching command)  ")
	}

	// The menu overlays the input pane (leftWidth wide) at X(0); cap the box
	// there so it never bleeds into the right pane. Box width = content +
	// horizontal padding (2) + border (2).
	rightWidth := m.width / 5
	maxContent := m.width - rightWidth - 4 - 4
	if maxContent < 20 {
		maxContent = 20
	}

	maxLines := 8
	var lines []string
	for i, c := range filtered {
		if i >= maxLines {
			lines = append(lines, "  …")
			break
		}
		marker := "  "
		if i == m.commandMenuIndex {
			marker = "❯ "
		}
		head := fmt.Sprintf("%s/%s", marker, c.Name)
		descCol := lipgloss.Width(head) + 2
		desc := wrapCommandDesc(c.Description, maxContent-descCol, descCol)
		lines = append(lines, head+"  "+desc)
	}
	return menuBoxStyle.Render(strings.Join(lines, "\n"))
}

// wrapCommandDesc wraps desc to fit within width columns, indenting each
// continuation line by indent spaces so wrapped descriptions read as a
// hanging indent under the command name.
func wrapCommandDesc(desc string, width, indent int) string {
	if width <= 1 {
		return desc
	}
	pad := strings.Repeat(" ", indent)
	var lines []string
	var cur string
	for _, w := range strings.Fields(desc) {
		if cur == "" {
			cur = w
		} else if lipgloss.Width(cur)+1+lipgloss.Width(w) <= width {
			cur += " " + w
		} else {
			lines = append(lines, cur)
			cur = pad + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n")
}
