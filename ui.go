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

	thinkingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Italic(true).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("238")).
			Padding(0, 1)

	agentLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("63"))
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

type agentStreamMsg struct {
	Content  string
	Thinking string
}

// StatusLogMsg represents a background status message sent to the side pane
type StatusLogMsg struct {
	Text string
}

type ChatMessage struct {
	Role     string
	Content  string
	Thinking string
}

type appModel struct {
	chatViewport viewport.Model
	logViewport  viewport.Model
	input        textinput.Model

	focus          focusedPane
	chatBufferSize int

	chatHistory      []ChatMessage
	chatScrollOffset int
	renderedLines    []string // cached, fully rendered chat lines (styled + wrapped)
	lastMsgStart     int      // index into renderedLines where the last chatHistory message starts
	logLines         []string // cached log pane lines

	r            *runner.Runner
	ctx          context.Context
	program      *tea.Program
	width        int
	height       int
	ready        bool
	isProcessing bool
	showThinking bool
}

func newModel(ctx context.Context, r *runner.Runner, chatBufferSize int, showThinking bool) appModel {
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
		logLines:       make([]string, 0),
		showThinking:   showThinking,
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
			case "ctrl+t":
				m.showThinking = !m.showThinking
				m.rebuildRenderedLines()
				m.renderChatViewport()
			case "enter":
			if m.input.Value() != "" && !m.isProcessing {
				prompt := m.input.Value()
				m.input.Reset()
				m.isProcessing = true

				m.chatHistory = append(m.chatHistory, ChatMessage{
					Role:    "user",
					Content: prompt,
				})
				m.rebuildRenderedLines()
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
				m.scrollChatDown(1)
			}
		case "down", "j":
			if m.focus == chatFocus {
				m.scrollChatUp(1)
			}
		case "pgup":
			if m.focus == chatFocus {
				m.scrollChatDown(m.chatViewport.Height())
			}
		case "pgdown":
			if m.focus == chatFocus {
				m.scrollChatUp(m.chatViewport.Height())
			}
		case "home":
			if m.focus == chatFocus {
				m.chatScrollOffset = 0
				m.renderChatViewport()
			}
		case "end":
			if m.focus == chatFocus {
				m.chatScrollOffset = m.maxChatScrollOffset()
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
		content, thinking := extractThinking(string(msg))
		m.chatHistory = append(m.chatHistory, ChatMessage{
			Role:     "agent",
			Content:  content,
			Thinking: thinking,
		})
		m.rebuildRenderedLines()
		m.chatScrollOffset = m.maxChatScrollOffset()
		m.renderChatViewport()

	case agentStreamMsg:
		if msg.Content == "" && msg.Thinking == "" {
			m.renderChatViewport()
			break
		}
		wasAtBottom := m.atBottom()
		if len(m.chatHistory) > 0 && m.chatHistory[len(m.chatHistory)-1].Role == "agent" {
			last := &m.chatHistory[len(m.chatHistory)-1]
			last.Content += msg.Content
			last.Thinking += msg.Thinking
		} else {
			m.chatHistory = append(m.chatHistory, ChatMessage{
				Role:     "agent",
				Content:  msg.Content,
				Thinking: msg.Thinking,
			})
			m.lastMsgStart = len(m.renderedLines)
		}
		m.refreshLastMessage()
		if wasAtBottom {
			m.chatScrollOffset = m.maxChatScrollOffset()
		}
		m.renderChatViewport()

	case agentLogMsg:
		m.logLines = append(m.logLines, string(msg))
		m.logViewport.SetContentLines(m.logLines)
		m.logViewport.GotoBottom()

	case StatusLogMsg:
		m.logLines = append(m.logLines, msg.Text)
		m.logViewport.SetContentLines(m.logLines)
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

var thinkingSeparator = "__THINKING_SEPARATOR__"

func extractThinking(content string) (string, string) {
	idx := strings.Index(content, thinkingSeparator)
	if idx >= 0 {
		cleanContent := strings.TrimSpace(content[:idx])
		thinking := strings.TrimSpace(content[idx+len(thinkingSeparator):])
		return cleanContent, thinking
	}
	return content, ""
}

func (m *appModel) maxChatScrollOffset() int {
	if len(m.renderedLines) == 0 {
		return 0
	}
	viewportHeight := m.chatViewport.Height()
	if viewportHeight <= 0 {
		viewportHeight = 1
	}
	totalLines := len(m.renderedLines)
	if totalLines <= viewportHeight {
		return 0
	}
	return totalLines - viewportHeight
}

// atBottom reports whether the chat view is currently pinned to the newest
// lines. Streaming only auto-scrolls when the user is already at the bottom so
// reading earlier history is not disrupted.
func (m *appModel) atBottom() bool {
	return m.chatScrollOffset >= m.maxChatScrollOffset()
}

// renderMsgLines renders a single chat message into styled, width-wrapped
// lines. The thinking text is rendered as ONE bordered block so empty interior
// lines (paragraph breaks) do not show up as separate empty boxes.
func (m *appModel) renderMsgLines(msg ChatMessage, wrapWidth int) []string {
	prefix := "🤖 Agent: "
	if msg.Role == "user" {
		prefix = "👤 User: "
	}

	var lines []string

	if m.showThinking && strings.TrimSpace(msg.Thinking) != "" {
		block := thinkingStyle.Width(wrapWidth).Render("💭 " + strings.TrimSpace(msg.Thinking))
		lines = append(lines, strings.Split(block, "\n")...)
		lines = append(lines, "")
	}

	if strings.TrimSpace(msg.Content) != "" {
		if msg.Role == "agent" {
			lines = append(lines, agentLabelStyle.Render(prefix))
			mdLines := strings.Split(renderMarkdown(msg.Content, wrapWidth), "\n")
			lines = append(lines, mdLines...)
		} else {
			contentLines := strings.Split(msg.Content, "\n")
			for i, line := range contentLines {
				var wrapped string
				if i == 0 {
					wrapped = lipgloss.NewStyle().Width(wrapWidth).Render(prefix + line)
				} else {
					wrapped = lipgloss.NewStyle().Width(wrapWidth).Render(line)
				}
				lines = append(lines, strings.Split(wrapped, "\n")...)
			}
		}
		lines = append(lines, "")
	}

	return lines
}

// rebuildRenderedLines re-renders the entire chat history. Only needed for
// rare events: window resize, thinking toggle, and new user messages.
func (m *appModel) rebuildRenderedLines() {
	wrapWidth := m.chatViewport.Width()
	if wrapWidth <= 0 {
		wrapWidth = 80
	}

	m.renderedLines = m.renderedLines[:0]
	lastCount := 0
	for _, msg := range m.chatHistory {
		msgLines := m.renderMsgLines(msg, wrapWidth)
		lastCount = len(msgLines)
		m.renderedLines = append(m.renderedLines, msgLines...)
	}
	m.lastMsgStart = len(m.renderedLines) - lastCount
}

// refreshLastMessage re-renders only the last chat message in place. This is
// the hot path: it runs for every streamed chunk and must stay O(chunk), not
// O(history), to keep the UI responsive.
func (m *appModel) refreshLastMessage() {
	if len(m.chatHistory) == 0 {
		m.renderedLines = m.renderedLines[:0]
		m.lastMsgStart = 0
		return
	}
	wrapWidth := m.chatViewport.Width()
	if wrapWidth <= 0 {
		wrapWidth = 80
	}
	msgLines := m.renderMsgLines(m.chatHistory[len(m.chatHistory)-1], wrapWidth)
	m.renderedLines = append(m.renderedLines[:m.lastMsgStart], msgLines...)
	m.lastMsgStart = len(m.renderedLines) - len(msgLines)
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

func (m *appModel) renderChatViewport() {
	if !m.ready || len(m.renderedLines) == 0 {
		m.chatViewport.SetContent("")
		m.chatScrollOffset = 0
		return
	}

	viewportHeight := m.chatViewport.Height()
	if viewportHeight <= 0 {
		viewportHeight = 1
	}
	if m.chatScrollOffset > m.maxChatScrollOffset() {
		m.chatScrollOffset = m.maxChatScrollOffset()
	}
	if m.chatScrollOffset < 0 {
		m.chatScrollOffset = 0
	}

	end := m.chatScrollOffset + viewportHeight
	if end > len(m.renderedLines) {
		end = len(m.renderedLines)
	}

	m.chatViewport.SetContent(strings.Join(m.renderedLines[m.chatScrollOffset:end], "\n"))
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
	v.AltScreen = true
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
					if part.Thought {
						trimmed := strings.TrimSpace(part.Text)
						if trimmed != "" {
							p.Send(agentStreamMsg{Thinking: trimmed})
						}
					} else {
						p.Send(agentStreamMsg{Content: part.Text})
					}
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
		p.Send(agentStreamMsg{})
		p.Send(agentDoneMsg{})
	}
}
