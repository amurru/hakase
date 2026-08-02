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

	hintBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Padding(0, 1)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Padding(0, 1)

	helpBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2)

	helpTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("63"))

	helpSectionStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("214"))

	helpKeyStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("252")).
			Width(22)

	helpDescStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	helpFooterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Italic(true)
)

type focusedPane int

const (
	inputFocus focusedPane = iota
	chatFocus
	logFocus
	taskFocus
)

type agentTextMsg string
type agentLogMsg string
type agentDoneMsg struct{}

type agentStreamMsg struct {
	Content  string
	Thinking string
}

// ModelInfoMsg carries the provider-reported model capabilities once the
// async model-info fetch completes.
type ModelInfoMsg struct {
	Info *ModelInfo
}

// UsageUpdateMsg carries the token usage of the most recent completed turn.
type UsageUpdateMsg struct {
	Usage *genai.GenerateContentResponseUsageMetadata
}

// StatusLogMsg represents a background status message sent to the side pane
type StatusLogMsg struct {
	Text string
}

// TaskUpdateMsg represents a task board update
type TaskUpdateMsg struct {
	Task   TaskMeta
	Action string // "created", "updated", "completed", "failed", "claimed"
}

// DelegationProgressMsg represents a delegation task progress update
// sent from a sub-agent to the orchestrator TUI.
type DelegationProgressMsg struct {
	TaskID  string
	Agent   string
	Status  string // "started", "completed", "failed"
	Message string
}

// TaskBoardMsg represents a full task board refresh
type TaskBoardMsg struct {
	Tasks []TaskMeta
}

type ChatMessage struct {
	Role     string
	Content  string
	Thinking string
}

type appModel struct {
	chatViewport viewport.Model
	logViewport  viewport.Model
	taskViewport viewport.Model
	input        textinput.Model

	focus          focusedPane
	chatBufferSize int

	chatHistory      []ChatMessage
	chatScrollOffset int
	renderedLines    []string // cached, fully rendered chat lines (styled + wrapped)
	lastMsgStart     int      // index into renderedLines where the last chatHistory message starts
	logLines         []string // cached log pane lines
	taskLines        []string // cached task board lines

	r            *runner.Runner
	ctx          context.Context
	program      *tea.Program
	width        int
	height       int
	ready        bool
	isProcessing bool
	showThinking bool
	showHelp     bool

	modelInfo     *ModelInfo
	modelName     string
	thinkingLevel string
	usage         *genai.GenerateContentResponseUsageMetadata
}

func newModel(
	ctx context.Context,
	r *runner.Runner,
	chatBufferSize int,
	showThinking bool,
	modelName string,
	thinkingLevel string,
) appModel {
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
		taskLines:      make([]string, 0),
		showThinking:   showThinking,
		modelName:      modelName,
		thinkingLevel:  thinkingLevel,
	}
}

func (m *appModel) Init() tea.Cmd {
	return textinput.Blink
}

// cycleFocus moves focus by dir (+1 forward, -1 backward) through the panes:
// input -> chat -> log -> task.
func (m *appModel) cycleFocus(dir int) {
	const paneCount = 4
	m.focus = focusedPane((int(m.focus) + dir + paneCount) % paneCount)

	if m.focus == inputFocus {
		m.input.Focus()
	} else {
		m.input.Blur()
	}
}

// appendLog appends a line to the log pane, keeping the view pinned to the
// bottom only when the user is already there so reading earlier logs is not
// disrupted by new entries.
func (m *appModel) appendLog(line string) {
	stick := m.logViewport.AtBottom()
	m.logLines = append(m.logLines, line)
	m.logViewport.SetContentLines(m.logLines)
	if stick {
		m.logViewport.GotoBottom()
	}
}

func (m *appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		key := msg.String()

		// While the help overlay is open, swallow all keys except close/quit.
		if m.showHelp {
			switch key {
			case "ctrl+c":
				return m, tea.Quit
			case "esc", "ctrl+/", "ctrl+_", "ctrl+?", "?":
				m.showHelp = false
			}
			return m, nil
		}

		switch key {
		// Esc only closes the help overlay (guard above) and is otherwise a
		// no-op: it never quits, and it is swallowed here so it cannot leak
		// into the input or viewport handlers below.
		case "esc":
			return m, nil
		case "ctrl+c":
			return m, tea.Quit
		// Ctrl+/ sends byte 0x1F, decoded as "ctrl+_" on standard terminals,
		// "ctrl+/" via the kitty protocol, and "ctrl+?" on some emulators.
		case "ctrl+/", "ctrl+_", "ctrl+?":
			m.showHelp = true
		case "?":
			if m.focus != inputFocus {
				m.showHelp = true
			}
		case "ctrl+t":
			m.showThinking = !m.showThinking
			m.rebuildRenderedLines()
			m.renderChatViewport()
		case "tab":
			m.cycleFocus(1)
		case "shift+tab":
			m.cycleFocus(-1)
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

				go runAgentTask(m.ctx, m.r, m.program, prompt, GenerateTaskID())
			}
		case "up", "k":
			if m.focus == chatFocus {
				m.scrollChatDown(1)
			}
		case "down", "j":
			if m.focus == chatFocus {
				m.scrollChatUp(1)
			}
		case "pgup", "b":
			if m.focus == chatFocus {
				m.scrollChatDown(m.chatViewport.Height())
			}
		case "pgdown", "f":
			if m.focus == chatFocus {
				m.scrollChatUp(m.chatViewport.Height())
			}
		case "u", "ctrl+u":
			if m.focus == chatFocus {
				m.scrollChatDown(m.chatViewport.Height() / 2)
			}
		case "d", "ctrl+d":
			if m.focus == chatFocus {
				m.scrollChatUp(m.chatViewport.Height() / 2)
			}
		// Home/end and g/G jump to the top/bottom of the focused pane.
		// log/task viewports handle their own pager keys; only the jumps are
		// routed here.
		case "home", "g":
			switch m.focus {
			case chatFocus:
				m.chatScrollOffset = 0
				m.renderChatViewport()
			case logFocus:
				m.logViewport.GotoTop()
			case taskFocus:
				m.taskViewport.GotoTop()
			}
		case "end", "G":
			switch m.focus {
			case chatFocus:
				m.chatScrollOffset = m.maxChatScrollOffset()
				m.renderChatViewport()
			case logFocus:
				m.logViewport.GotoBottom()
			case taskFocus:
				m.taskViewport.GotoBottom()
			}
		}

	case tea.MouseWheelMsg:
		switch m.focus {
		case chatFocus:
			switch msg.Button {
			case tea.MouseWheelUp:
				// Terminal mouse wheel UP = scroll toward older messages
				m.scrollChatDown(m.chatViewport.MouseWheelDelta)
			case tea.MouseWheelDown:
				// Terminal mouse wheel DOWN = scroll toward newer messages
				m.scrollChatUp(m.chatViewport.MouseWheelDelta)
			}
		case logFocus:
			if msg.Button == tea.MouseWheelUp {
				m.logViewport.ScrollUp(m.logViewport.MouseWheelDelta)
			} else if msg.Button == tea.MouseWheelDown {
				m.logViewport.ScrollDown(m.logViewport.MouseWheelDelta)
			}
		case taskFocus:
			if msg.Button == tea.MouseWheelUp {
				m.taskViewport.ScrollUp(m.taskViewport.MouseWheelDelta)
			} else if msg.Button == tea.MouseWheelDown {
				m.taskViewport.ScrollDown(m.taskViewport.MouseWheelDelta)
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
				viewport.WithHeight(m.height-7),
			)
			m.logViewport = viewport.New(
				viewport.WithWidth(rightWidth),
				viewport.WithHeight((m.height-7)*2/3),
			)
			m.taskViewport = viewport.New(
				viewport.WithWidth(rightWidth),
				viewport.WithHeight((m.height-7)/3+1),
			)

			// Disable viewport's built-in mouse wheel - we handle it manually
			m.chatViewport.MouseWheelEnabled = false
			m.logViewport.MouseWheelEnabled = false
			m.taskViewport.MouseWheelEnabled = false

			m.ready = true
		} else {
			m.chatViewport.SetWidth(leftWidth)
			m.chatViewport.SetHeight(m.height - 7)
			m.logViewport.SetWidth(rightWidth)
			m.logViewport.SetHeight((m.height - 7) * 2 / 3)
			m.taskViewport.SetWidth(rightWidth)
			m.taskViewport.SetHeight((m.height-7)/3 + 1)
		}

		m.input.SetWidth(leftWidth - 3)
		// Re-render chat viewport on resize
		m.renderChatViewport()
		m.refreshTaskBoard()

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
		m.appendLog(string(msg))

	case StatusLogMsg:
		m.appendLog(msg.Text)
	case DelegationProgressMsg:
		m.appendLog(fmt.Sprintf("🔀 [delegate %s] %s: %s", msg.TaskID, msg.Agent, msg.Message))
	case TaskUpdateMsg:
		m.refreshTaskBoard()
	case TaskBoardMsg:
		m.refreshTaskBoard()
	case agentDoneMsg:
		m.isProcessing = false
	case ModelInfoMsg:
		if msg.Info != nil {
			m.modelInfo = msg.Info
			if msg.Info.Name != "" {
				m.modelName = msg.Info.Name
			}
			if msg.Info.ThinkingLevel != "" {
				m.thinkingLevel = msg.Info.ThinkingLevel
			}
		}
	case UsageUpdateMsg:
		if msg.Usage != nil {
			m.usage = msg.Usage
		}
	}

	var vpCmd tea.Cmd
	if m.focus == chatFocus {
		m.chatViewport, vpCmd = m.chatViewport.Update(msg)
		cmds = append(cmds, vpCmd)
	} else if m.focus == logFocus {
		m.logViewport, vpCmd = m.logViewport.Update(msg)
		cmds = append(cmds, vpCmd)
	} else if m.focus == taskFocus {
		m.taskViewport, vpCmd = m.taskViewport.Update(msg)
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

func (m *appModel) refreshTaskBoard() {
	registry, err := loadTaskRegistry()
	if err != nil {
		m.taskLines = []string{"Error loading tasks: " + err.Error()}
		m.renderTaskViewport()
		return
	}

	var lines []string
	lines = append(lines, "📋 Task Board")
	lines = append(lines, strings.Repeat("─", 30))

	statusOrder := []TaskStatus{
		TaskStatusPending,
		TaskStatusInProgress,
		TaskStatusCompleted,
		TaskStatusFailed,
		TaskStatusCancelled,
		TaskStatusSkipped,
		TaskStatusBlocked,
	}
	statusSymbols := map[TaskStatus]string{
		TaskStatusPending:    "⏳",
		TaskStatusInProgress: "▶️",
		TaskStatusCompleted:  "✅",
		TaskStatusFailed:     "❌",
		TaskStatusCancelled:  "🚫",
		TaskStatusSkipped:    "⏭️",
		TaskStatusBlocked:    "🔒",
	}
	prioritySymbols := map[TaskPriority]string{
		TaskPriorityCritical: "🔴",
		TaskPriorityHigh:     "🟠",
		TaskPriorityMedium:   "🟡",
		TaskPriorityLow:      "🟢",
	}

	for _, status := range statusOrder {
		var statusTasks []TaskMeta
		for _, task := range registry.Tasks {
			if task.Status == status {
				statusTasks = append(statusTasks, task)
			}
		}

		if len(statusTasks) == 0 {
			continue
		}

		symbol := statusSymbols[status]
		lines = append(lines, fmt.Sprintf("%s %s (%d)", symbol, status, len(statusTasks)))

		for _, task := range statusTasks {
			priSymbol := prioritySymbols[task.Priority]
			depsStr := ""
			if len(task.BlockedBy) > 0 {
				depsStr = fmt.Sprintf(" 🔗%d", len(task.BlockedBy))
			}
			assigneeStr := ""
			if task.Assignee != "" {
				assigneeStr = fmt.Sprintf(" @%s", task.Assignee)
			}
			lines = append(
				lines,
				fmt.Sprintf("  %s %s%s%s", priSymbol, task.Title, depsStr, assigneeStr),
			)
		}
		lines = append(lines, "")
	}

	activeCount := 0
	for _, task := range registry.Tasks {
		if task.Status != TaskStatusArchived {
			activeCount++
		}
	}
	lines = append(lines, fmt.Sprintf("Total: %d tasks", activeCount))

	m.taskLines = lines
	m.renderTaskViewport()
}

func (m *appModel) renderTaskViewport() {
	if !m.ready {
		return
	}
	m.taskViewport.SetContent(strings.Join(m.taskLines, "\n"))
}

// Change return type to tea.View
func (m *appModel) View() tea.View {
	if !m.ready {
		return tea.NewView("Initializing TUI...")
	}

	if m.showHelp {
		return m.helpView()
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
	taskStyle := inactiveBorder
	if m.focus == taskFocus {
		taskStyle = activeBorder
	}

	leftCol := lipgloss.JoinVertical(
		lipgloss.Left,
		chatStyle.Render(m.chatViewport.View()),
		inputStyle.Render(m.input.View()),
	)

	rightCol := lipgloss.JoinVertical(
		lipgloss.Left,
		logStyle.Render(m.logViewport.View()),
		taskStyle.Render(m.taskViewport.View()),
	)

	content := lipgloss.JoinHorizontal(
		lipgloss.Top,
		leftCol,
		rightCol,
	)

	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, m.statusBar(), content, m.hintBar()))
	v.MouseMode = tea.MouseModeCellMotion
	v.AltScreen = true
	return v
}

// hintBar renders the single-line footer that surfaces the most commonly used
// shortcuts together with the current focus and status.
func (m *appModel) hintBar() string {
	focusNames := [...]string{"input", "chat", "log", "task"}
	status := "● " + focusNames[m.focus]
	if m.isProcessing {
		status += " ⏳ working"
	}
	if m.showThinking {
		status += " 💭 thinking"
	}
	hints := "ctrl+/ help · tab focus · ctrl+t thinking · enter send · ctrl+c quit"
	return hintBarStyle.Render(status + "  │  " + hints)
}

// statusBar renders the header line: model name, context window, usage, thinking level.
func (m *appModel) statusBar() string {
	parts := []string{"🧠 " + m.modelName}

	limit := int64(0)
	if m.modelInfo != nil {
		limit = m.modelInfo.ContextWindow
	}
	if limit > 0 {
		parts = append(parts, "ctx "+formatTokens(limit))
		pct, used := m.usagePercent()
		if used > 0 {
			parts = append(parts, fmt.Sprintf("%d%% %s", pct, usageBar(pct)))
		}
	}
	parts = append(parts, "thinking "+m.thinkingStatus())

	return statusBarStyle.Render(strings.Join(parts, "  │  "))
}

// usagePercent returns the context-window usage percentage and used tokens,
// falling back to prompt+candidates when the total is unavailable.
func (m *appModel) usagePercent() (int, int64) {
	limit := int64(0)
	if m.modelInfo != nil {
		limit = m.modelInfo.ContextWindow
	}
	if limit <= 0 || m.usage == nil {
		return 0, 0
	}
	used := int64(m.usage.TotalTokenCount)
	if used <= 0 {
		used = int64(m.usage.PromptTokenCount + m.usage.CandidatesTokenCount)
	}
	pct := int(used * 100 / limit)
	if pct > 100 {
		pct = 100
	}
	return pct, used
}

// thinkingStatus renders the thinking level as reported by the provider.
func (m *appModel) thinkingStatus() string {
	level := m.thinkingLevel
	if m.modelInfo != nil && m.modelInfo.ThinkingLevel != "" {
		level = m.modelInfo.ThinkingLevel
	}
	if m.modelInfo != nil && !m.modelInfo.ThinkingEnabled && level == "" {
		return "off"
	}
	switch {
	case level == "" || strings.EqualFold(level, "on"):
		return "on"
	case strings.EqualFold(level, "off"):
		return "off"
	default:
		return strings.ToLower(level)
	}
}

func formatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dK", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func usageBar(pct int) string {
	const cells = 10
	filled := pct * cells / 100
	if filled > cells {
		filled = cells
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", cells-filled)
}

// helpView renders the full-screen keyboard shortcut overlay.
func (m *appModel) helpView() tea.View {
	var b strings.Builder
	b.WriteString(helpTitleStyle.Render("⌨️  Keyboard Shortcuts"))
	b.WriteString("\n\n")

	sections := []struct {
		title   string
		entries []helpBinding
	}{
		{"Global", []helpBinding{
			{"ctrl+c", "Quit the application"},
			{"esc", "Close the help overlay"},
			{"ctrl+/", "Toggle this help screen"},
			{"?", "Toggle help (when not typing)"},
			{"tab / shift+tab", "Cycle focus between panes"},
			{"ctrl+t", "Toggle thinking display"},
		}},
		{"Input", []helpBinding{
			{"enter", "Send the message"},
			{"ctrl+a / ctrl+e", "Jump to line start / end"},
			{"left / right", "Move the cursor"},
			{"ctrl+u", "Clear the input"},
		}},
		{"Panels (chat / log / task)", []helpBinding{
			{"up / k", "Scroll toward older content"},
			{"down / j", "Scroll toward newer content"},
			{"pgup / b", "Page up"},
			{"pgdown / f", "Page down"},
			{"u / d", "Half page up / down"},
			{"home / g", "Jump to top"},
			{"end / G", "Jump to bottom"},
			{"mouse wheel", "Scroll the focused pane"},
		}},
	}

	for _, sec := range sections {
		b.WriteString(helpSectionStyle.Render(sec.title))
		b.WriteString("\n")
		for _, e := range sec.entries {
			b.WriteString("  ")
			b.WriteString(helpKeyStyle.Render(e.keys))
			b.WriteString(helpDescStyle.Render(e.desc))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(helpFooterStyle.Render("Press ctrl+/ or esc to close"))

	box := helpBoxStyle.Render(b.String())
	v := tea.NewView(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box))
	v.MouseMode = tea.MouseModeCellMotion
	v.AltScreen = true
	return v
}

type helpBinding struct {
	keys string
	desc string
}

func runAgentTask(ctx context.Context, r *runner.Runner, p *tea.Program, prompt string, taskID string) {
	msg := genai.NewContentFromText(prompt, genai.RoleUser)

	var lastUsage *genai.GenerateContentResponseUsageMetadata
	for ev, err := range r.Run(ctx, "user-1", taskID, msg, agent.RunConfig{}) {
		if err != nil {
			if p != nil {
				p.Send(agentLogMsg(fmt.Sprintf("❌ Error: %v", err)))
			}
			break
		}
		if ev == nil {
			continue
		}
		if ev.UsageMetadata != nil {
			lastUsage = ev.UsageMetadata
		}
		if ev.Content != nil {
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
		if lastUsage != nil {
			p.Send(UsageUpdateMsg{Usage: lastUsage})
		}
		p.Send(agentDoneMsg{})
	}
}
