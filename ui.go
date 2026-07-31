package main

import (
	"context"
	"fmt"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
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

type model struct {
	chatViewport viewport.Model
	logViewport  viewport.Model
	input        textinput.Model

	focus focusedPane

	r            *runner.Runner
	ctx          context.Context
	program      *tea.Program
	width        int
	height       int
	ready        bool
	isProcessing bool
}

func newModel(ctx context.Context, r *runner.Runner) model {
	ti := textinput.New()
	ti.Placeholder = "Ask agent to navigate or search..."
	ti.Focus()

	return model{
		input: ti,
		r:     r,
		ctx:   ctx,
	}
}

func (m *model) Init() tea.Cmd {
	return textinput.Blink
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

				m.chatViewport.SetContent(
					m.chatViewport.View() + fmt.Sprintf("\n\n👤 User: %s\n🤖 Agent: ", prompt),
				)
				m.chatViewport.GotoBottom()

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
			m.ready = true
		} else {
			m.chatViewport.SetWidth(leftWidth)
			m.chatViewport.SetHeight(m.height - 5)
			m.logViewport.SetWidth(rightWidth)
			m.logViewport.SetHeight(m.height - 2)
		}
		m.input.SetWidth(leftWidth - 3)

	case agentTextMsg:
		// soft-wrap incoming text to fit inside the viewport
		wrapStyle := lipgloss.NewStyle().Width(m.chatViewport.Width())
		wrappedText := wrapStyle.Render(string(msg))
		m.chatViewport.SetContent(m.chatViewport.View() + wrappedText)
		m.chatViewport.GotoBottom()

	case agentLogMsg:
		m.logViewport.SetContent(m.logViewport.View() + "\n" + string(msg))
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

// Change return type to tea.View
func (m *model) View() tea.View {
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

	// Return a tea.View object
	return tea.NewView(content)
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
