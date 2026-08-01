package main

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"
)

var (
	inactiveBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240"))

	activeBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63"))
)

type focusedPane int

const (
	inputFocus focusedPane = iota
	chatFocus
	logFocus
)

type agentTextMsg string
type agentLogMsg string
type agentDoneMsg struct{}

// StatusLogMsg represents a background status message sent to the side pane
type StatusLogMsg struct {
	Text string
}

type ChatMessage struct {
	Role    string
	Content string
}

type appModel struct {
	chatViewport viewport.Model
	logViewport  viewport.Model
	input        textinput.Model

	focus           focusedPane
	chatBufferSize  int

	chatHistory     []ChatMessage
	chatScrollOffset int

	r            *runner.Runner
	ctx          context.Context
	program      *tea.Program
	width        int
	height       int
	ready        bool
	isProcessing bool
}

func newModel(ctx context.Context, r *runner.Runner, chatBufferSize int) appModel {
	ti := textinput.New()
	ti.Placeholder = "Ask me anything and I will do it..."
	ti.Focus()

	// Default to 1000 lines if not configured
	if chatBufferSize <= 0 {
		chatBufferSize = 1000
	}

	return appModel{
		input:          ti,
		r:              r,
		ctx:            ctx,
		chatBufferSize: chatBufferSize,
		chatHistory:    make([]ChatMessage, 0),
	}
}

func (m *appModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m *appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			if m.input.Value() != "" && !m.isProcessing {
				prompt := m.input.Value()
				m.input.Reset()
				m.isProcessing = true

				m.chatHistory = append(m.chatHistory, ChatMessage{
					Role:    "user",
					Content: prompt,
				})
				m.chatScrollOffset = m.maxChatScrollOffset()
				m.renderChatViewport()

				go runAgentTask(m.ctx, m.r, m.program, prompt)
			}
		case "tab":
			// 🔄 Cycle through: inputFocus (0) -> chatFocus (1) -> logFocus (2) -> inputFocus (0)
			m.focus = (m.focus + 1) % 3

			if m.focus == inputFocus {
				m.input.Focus()
			} else {
				m.input.Blur()
			}
		case "up", "k":
			if m.focus == chatFocus {
				m.scrollChatUp(1)
			}
		case "down", "j":
			if m.focus == chatFocus {
				m.scrollChatDown(1)
			}
		case "pgup":
			if m.focus == chatFocus {
				m.scrollChatUp(m.chatViewport.Height())
			}
		case "pgdown":
			if m.focus == chatFocus {
				m.scrollChatDown(m.chatViewport.Height())
			}
		case "home":
			if m.focus == chatFocus {
				m.chatScrollOffset = m.maxChatScrollOffset()
				m.renderChatViewport()
			}
		case "end":
			if m.focus == chatFocus {
				m.chatScrollOffset = 0
				m.renderChatViewport()
			}
		}

	case tea.MouseWheelMsg:
		if m.focus == chatFocus {
			switch msg.Button {
			case tea.MouseWheelUp:
				// Terminal mouse wheel UP = scroll viewport UP = see content at higher Y = newer messages
				m.scrollChatDown(m.chatViewport.MouseWheelDelta)
			case tea.MouseWheelDown:
				// Terminal mouse wheel DOWN = scroll viewport DOWN = see content at lower Y = older messages
				m.scrollChatUp(m.chatViewport.MouseWheelDelta)
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		rightWidth := m.width / 4
		leftWidth := m.width - rightWidth - 4

		if !m.ready {
			m.chatViewport = viewport.New(
				viewport.WithWidth(leftWidth),
				viewport.WithHeight(m.height-5),
			)
			m.logViewport = viewport.New(
				viewport.WithWidth(rightWidth),
				viewport.WithHeight(m.height-2),
			)

			// Disable viewport's built-in mouse wheel - we handle it manually
			m.chatViewport.MouseWheelEnabled = false
			m.logViewport.MouseWheelEnabled = false

			m.ready = true
		} else {
			m.chatViewport.SetWidth(leftWidth)
			m.chatViewport.SetHeight(m.height - 5)
			m.logViewport.SetWidth(rightWidth)
			m.logViewport.SetHeight(m.height - 2)
		}

		m.input.SetWidth(leftWidth - 3)
		// Re-render chat viewport on resize
		m.renderChatViewport()

	case agentTextMsg:
		m.chatHistory = append(m.chatHistory, ChatMessage{
			Role:    "agent",
			Content: string(msg),
		})
		m.chatScrollOffset = m.maxChatScrollOffset()
		m.renderChatViewport()

	case agentLogMsg:
		m.logViewport.SetContent(m.logViewport.View() + "\n" + string(msg))
		m.logViewport.GotoBottom()

	case StatusLogMsg:
		m.logViewport.SetContent(m.logViewport.View() + "\n" + string(msg.Text))
		m.logViewport.GotoBottom()
	case agentDoneMsg:
		m.isProcessing = false
	}

	var vpCmd tea.Cmd
	if m.focus == chatFocus {
		m.chatViewport, vpCmd = m.chatViewport.Update(msg)
		cmds = append(cmds, vpCmd)
	} else if m.focus == logFocus {
		m.logViewport, vpCmd = m.logViewport.Update(msg)
		cmds = append(cmds, vpCmd)
	}

	var tiCmd tea.Cmd
	m.input, tiCmd = m.input.Update(msg)
	cmds = append(cmds, tiCmd)

	return m, tea.Batch(cmds...)
}

func (m *appModel) maxChatScrollOffset() int {
	if len(m.chatHistory) == 0 {
		return 0
	}
	totalLines := m.totalChatLines()
	viewportHeight := m.chatViewport.Height()
	if totalLines <= viewportHeight {
		return 0
	}
	return totalLines - viewportHeight
}

// totalChatLines calculates the total number of wrapped lines in chat history
func (m *appModel) totalChatLines() int {
	if len(m.chatHistory) == 0 {
		return 0
	}

	wrapWidth := m.chatViewport.Width()
	if wrapWidth <= 0 {
		wrapWidth = 80
	}

	total := 0
	for _, msg := range m.chatHistory {
		prefix := "🤖 Agent: "
		if msg.Role == "user" {
			prefix = "👤 User: "
		}
		lines := strings.Split(msg.Content, "\n")
		for _, line := range lines {
			wrapped := lipgloss.NewStyle().Width(wrapWidth).Render(prefix + line)
			total += strings.Count(wrapped, "\n") + 1
			prefix = ""
		}
		total += 1
	}
	return total
}

func (m *appModel) scrollChatUp(lines int) {
	maxOffset := m.maxChatScrollOffset()
	m.chatScrollOffset += lines
	if m.chatScrollOffset > maxOffset {
		m.chatScrollOffset = maxOffset
	}
	m.renderChatViewport()
}

func (m *appModel) scrollChatDown(lines int) {
	m.chatScrollOffset -= lines
	if m.chatScrollOffset < 0 {
		m.chatScrollOffset = 0
	}
	m.renderChatViewport()
}

// renderChatViewport renders the visible portion of chat history
func (m *appModel) renderChatViewport() {
	if !m.ready || len(m.chatHistory) == 0 {
		m.chatViewport.SetContent("")
		return
	}

	wrapWidth := m.chatViewport.Width()
	if wrapWidth <= 0 {
		wrapWidth = 80
	}

	var allLines []string
	for _, msg := range m.chatHistory {
		prefix := "🤖 Agent: "
		if msg.Role == "user" {
			prefix = "👤 User: "
		}
		lines := strings.Split(msg.Content, "\n")
		for i, line := range lines {
			var wrapped string
			if i == 0 {
				wrapped = lipgloss.NewStyle().Width(wrapWidth).Render(prefix + line)
			} else {
				wrapped = lipgloss.NewStyle().Width(wrapWidth).Render(line)
			}
			allLines = append(allLines, strings.Split(wrapped, "\n")...)
		}
		allLines = append(allLines, "")
	}

	if len(allLines) > 0 && allLines[len(allLines)-1] == "" {
		allLines = allLines[:len(allLines)-1]
	}

	viewportHeight := m.chatViewport.Height()
	start := m.chatScrollOffset
	end := start + viewportHeight
	if end > len(allLines) {
		end = len(allLines)
	}
	if start > len(allLines) {
		start = len(allLines)
	}

	var visibleLines []string
	if start < len(allLines) {
		visibleLines = allLines[start:end]
	}

	content := strings.Join(visibleLines, "\n")
	m.chatViewport.SetContent(content)
}

// Change return type to tea.View
func (m *appModel) View() tea.View {
	if !m.ready {
		return tea.NewView("Initializing TUI...")
	}

	chatStyle := inactiveBorder
	if m.focus == chatFocus {
		chatStyle = activeBorder
	}
	inputStyle := inactiveBorder
	if m.focus == inputFocus {
		inputStyle = activeBorder
	}
	logStyle := inactiveBorder
	if m.focus == logFocus {
		logStyle = activeBorder
	}

	leftCol := lipgloss.JoinVertical(
		lipgloss.Left,
		chatStyle.Render(m.chatViewport.View()),
		inputStyle.Render(m.input.View()),
	)

	content := lipgloss.JoinHorizontal(
		lipgloss.Top,
		leftCol,
		logStyle.Render(m.logViewport.View()),
	)

	v := tea.NewView(content)
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func runAgentTask(ctx context.Context, r *runner.Runner, p *tea.Program, prompt string) {
	msg := genai.NewContentFromText(prompt, genai.RoleUser)

	for ev, err := range r.Run(ctx, "user-1", "session-1", msg, agent.RunConfig{}) {
		if err != nil {
			if p != nil {
				p.Send(agentLogMsg(fmt.Sprintf("❌ Error: %v", err)))
			}
			break
		}
		if ev != nil && ev.Content != nil {
			for _, part := range ev.Content.Parts {
				if part.Text != "" && p != nil {
					p.Send(agentTextMsg(part.Text))
				}
				if part.FunctionCall != nil && p != nil {
					p.Send(
						agentLogMsg(
							fmt.Sprintf(
								"🛠️ Call: %s(%v)",
								part.FunctionCall.Name,
								part.FunctionCall.Args,
							),
						),
					)
				}
				if part.FunctionResponse != nil && p != nil {
					p.Send(agentLogMsg(fmt.Sprintf("📥 Response: %s", part.FunctionResponse.Name)))
				}
			}
		}
	}

	if p != nil {
		p.Send(agentDoneMsg{})
	}
}
