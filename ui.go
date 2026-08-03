package main

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"
)

// inputPadV is the vertical padding (rows) added inside the input pane border
// to enlarge the prompt's click target. reservedRows must stay in sync:
// status(1) + chat borders(2) + input(border 2 + 2*inputPadV + content 1) + hint(1).
const inputPadV = 1

// reservedRows is the screen-row budget consumed by everything except the
// chat / log / task viewports.
const reservedRows = 1 + 2 + (2 + 2*inputPadV + 1) + 1

var (
	inactiveBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240"))

	activeBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63"))

	// Input pane uses extra vertical padding so the prompt is a larger, easier
	// click target and visually roomier than the single-line editor alone.
	inputInactive = inactiveBorder.Padding(inputPadV, 0)
	inputActive   = activeBorder.Padding(inputPadV, 0)

	// highlightStyle is the background color applied to selected lines
	// in output panes so the user can visually distinguish the selection.
	highlightStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("238"))

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

// Pane layer identifiers used by the lipgloss Compositor for mouse
// hit-testing. Each on-screen pane is rendered as a Layer carrying one of
// these IDs; clicks are resolved via compositor.Hit(x,y).ID() instead of
// hand-computed coordinate thresholds.
const (
	paneStatus = "status"
	paneChat   = "chat"
	paneLog    = "log"
	paneInput  = "input"
	paneTask   = "task"
	paneHint   = "hint"
)

// paneIDToFocus maps a hit-tested pane layer ID to its focus value.
// ok is false for the status/hint bars and for clicks outside any pane.
func paneIDToFocus(id string) (focusedPane, bool) {
	switch id {
	case paneChat:
		return chatFocus, true
	case paneLog:
		return logFocus, true
	case paneTask:
		return taskFocus, true
	case paneInput:
		return inputFocus, true
	}
	return 0, false
}

// mousePane resolves the layer ID of the pane under the given screen
// coordinate using the compositor from the last View() render. Returns ""
// when no pane is hit (or before the first render).
func (m *appModel) mousePane(x, y int) string {
	if m.compositor == nil {
		return ""
	}
	hit := m.compositor.Hit(x, y)
	if hit.Empty() {
		return ""
	}
	return hit.ID()
}

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

	// Text selection state for auto-copy on selection end.
	selectionStartLine int
	selectionEndLine   int
	selectionActive    bool
	selectionPane      focusedPane

	r            *runner.Runner
	ctx          context.Context
	program      *tea.Program
	width        int
	height       int
	ready        bool
	isProcessing bool
	showThinking bool
	showHelp     bool

	// compositor holds the layer tree from the most recent View() render,
	// used to resolve mouse clicks to a pane by layer ID. Kept on the model
	// so Update can hit-test synchronously instead of routing through the
	// async OnMouse path (which made drag-selection lag the pointer).
	compositor *lipgloss.Compositor

	modelInfo     *ModelInfo
	modelName     string
	thinkingLevel string
	usage         *genai.GenerateContentResponseUsageMetadata

	// Session management
	sessionService      *SessionService
	showSessionList     bool
	sessionListIndex    int
	sessionListFilter   string
	sessionListSessions []SessionSummary
	sessionListFiltered []SessionSummary
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
		sessionService: initSessionService(),
	}
}

// initSessionService initializes the session service and attempts
// to restore the most recently updated non-archived session.
func initSessionService() *SessionService {
	store, err := NewSessionStore(sessionsDir)
	if err != nil {
		return nil
	}
	svc, err := NewSessionService(store)
	if err != nil {
		return nil
	}
	return svc
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
	m.logViewport.SetContentLines(m.highlightLines(m.logLines, 0))
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
		// Ctrl+Shift+C copies the focused pane's content to clipboard.
		case "ctrl+shift+c":
			m.copyFocusedPaneContent()

		// Ctrl+T toggles thinking display.
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
		// Session management keybindings.
		case "ctrl+n":
			return m, m.newSession()
		case "ctrl+s":
			return m, m.toggleSessionList()
		}

		// Handle session list modal keybindings when the modal is open.
		if m.showSessionList {
			return m, m.handleSessionListKey(key)
		}

	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			break
		}
		// Clicking a pane moves focus to it (like Tab), including the input
		// pane so the user can click back into the prompt.
		if fp, ok := paneIDToFocus(m.mousePane(msg.X, msg.Y)); ok {
			m.focus = fp
			if fp == inputFocus {
				m.input.Focus()
			} else {
				m.input.Blur()
			}
		}
		m.selectionActive = true
		m.selectionPane = m.focus
		m.selectionStartLine = m.mouseYToContentLine(msg.Y)
		m.selectionEndLine = m.selectionStartLine
		m.renderSelectionPane()

	case tea.MouseMotionMsg:
		// CellMotion mode only reports motion while a button is held, so
		// selectionActive alone is a reliable guard for an active drag.
		if m.selectionActive {
			m.selectionEndLine = m.mouseYToContentLine(msg.Y)
			m.renderSelectionPane()
		}

	case tea.MouseReleaseMsg:
		if m.selectionActive {
			m.selectionEndLine = m.mouseYToContentLine(msg.Y)
			m.copySelection()
			m.selectionActive = false
			m.renderSelectionPane()
		}

	case tea.MouseWheelMsg:
		// Wheel scrolls the focused pane (preserves prior behaviour).
		switch m.focus {
		case chatFocus:
			switch msg.Button {
			case tea.MouseWheelUp:
				m.scrollChatDown(m.chatViewport.MouseWheelDelta)
			case tea.MouseWheelDown:
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
		avail := m.height - reservedRows

		if !m.ready {
			m.chatViewport = viewport.New(
				viewport.WithWidth(leftWidth),
				viewport.WithHeight(avail),
			)
			m.logViewport = viewport.New(
				viewport.WithWidth(rightWidth),
				viewport.WithHeight(avail*2/3),
			)
			m.taskViewport = viewport.New(
				viewport.WithWidth(rightWidth),
				viewport.WithHeight(avail/3+1),
			)

			// Disable viewport's built-in mouse wheel - we handle it manually
			m.chatViewport.MouseWheelEnabled = false
			m.logViewport.MouseWheelEnabled = false
			m.taskViewport.MouseWheelEnabled = false

			m.ready = true
		} else {
			m.chatViewport.SetWidth(leftWidth)
			m.chatViewport.SetHeight(avail)
			m.logViewport.SetWidth(rightWidth)
			m.logViewport.SetHeight(avail * 2 / 3)
			m.taskViewport.SetWidth(rightWidth)
			m.taskViewport.SetHeight(avail/3 + 1)
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
		// Save the final agent message to the session.
		if m.sessionService != nil && len(m.chatHistory) > 0 {
			last := m.chatHistory[len(m.chatHistory)-1]
			if last.Role == "agent" {
				_ = m.sessionService.AddMessage("agent", last.Content, last.Thinking)
			}
		}
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

	visibleLines := m.renderedLines[m.chatScrollOffset:end]
	m.chatViewport.SetContent(strings.Join(m.highlightLines(visibleLines, m.chatScrollOffset), "\n"))
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
	m.taskViewport.SetContent(strings.Join(m.highlightLines(m.taskLines, 0), "\n"))
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
	inputStyle := inputInactive
	if m.focus == inputFocus {
		inputStyle = inputActive
	}
	logStyle := inactiveBorder
	if m.focus == logFocus {
		logStyle = activeBorder
	}
	taskStyle := inactiveBorder
	if m.focus == taskFocus {
		taskStyle = activeBorder
	}

	chatRender := chatStyle.Render(m.chatViewport.View())
	inputRender := inputStyle.Render(m.input.View())
	logRender := logStyle.Render(m.logViewport.View())
	taskRender := taskStyle.Render(m.taskViewport.View())

	// Session-list modal is keyboard-driven, so compositor click-focusing is
	// intentionally inactive while it is open.
	if m.showSessionList {
		leftCol := lipgloss.JoinVertical(lipgloss.Left, chatRender, inputRender)
		rightCol := lipgloss.JoinVertical(lipgloss.Left, logRender, taskRender)
		content := lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol)
		return tea.NewView(lipgloss.JoinVertical(lipgloss.Left, m.statusBar(), content, m.sessionListView(), m.hintBar()))
	}

	// Main layout: each pane is a named Layer positioned at its exact screen
	// coordinates. The Compositor renders the whole screen and resolves mouse
	// clicks by layer ID, so hit-zones always match the drawn panes (borders
	// and gaps included) without any manual coordinate thresholds.
	m.compositor = m.buildCompositor(chatRender, logRender, inputRender, taskRender)

	v := tea.NewView(m.compositor.Render())
	v.MouseMode = tea.MouseModeCellMotion
	v.AltScreen = true
	return v
}

// buildCompositor assembles the named, positioned Layers for every on-screen
// pane and returns a Compositor that both renders the screen and answers
// mouse hit-tests by layer ID. Extracted from View so the geometry can be
// exercised directly in tests.
func (m *appModel) buildCompositor(chatRender, logRender, inputRender, taskRender string) *lipgloss.Compositor {
	rightWidth := m.width / 4
	leftWidth := m.width - rightWidth - 4
	rightColStart := leftWidth + 2 // chat pane width including its border

	chatH := m.chatViewport.Height()
	logH := m.logViewport.Height()

	layers := []*lipgloss.Layer{
		lipgloss.NewLayer(m.statusBar()).ID(paneStatus).X(0).Y(0),
		lipgloss.NewLayer(chatRender).ID(paneChat).X(0).Y(1),
		lipgloss.NewLayer(logRender).ID(paneLog).X(rightColStart).Y(1),
		lipgloss.NewLayer(inputRender).ID(paneInput).X(0).Y(1 + chatH + 2),
		lipgloss.NewLayer(taskRender).ID(paneTask).X(rightColStart).Y(1 + logH + 2),
		lipgloss.NewLayer(m.hintBar()).ID(paneHint).X(0).Y(m.height - 1),
	}
	return lipgloss.NewCompositor(layers...)
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
	hints := "ctrl+/ help · click pane to focus · tab focus · ctrl+t thinking · enter send · ctrl+shift+c copy · ctrl+c quit"
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
			{"click pane", "Focus a pane (chat / log / task)"},
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
			{"ctrl+shift+c", "Copy focused pane to clipboard"},
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

// newSession clears the active session and starts fresh.
func (m *appModel) newSession() tea.Cmd {
	if m.sessionService == nil {
		return nil
	}
	m.sessionService.ClearActiveSession()
	m.chatHistory = make([]ChatMessage, 0)
	m.rebuildRenderedLines()
	m.chatScrollOffset = 0
	m.renderChatViewport()
	m.appendLog("New session started")
	return nil
}

// toggleSessionList opens or closes the session list modal.
func (m *appModel) toggleSessionList() tea.Cmd {
	if m.showSessionList {
		m.showSessionList = false
		m.sessionListFilter = ""
		return nil
	}
	m.showSessionList = true
	m.sessionListIndex = 0
	m.sessionListFilter = ""
	if m.sessionService != nil {
		summaries, err := m.sessionService.ListSessions()
		if err == nil {
			m.sessionListSessions = summaries
		}
	}
	m.filterSessionList()
	return nil
}

// filterSessionList filters the session list by the current filter text.
func (m *appModel) filterSessionList() {
	filter := strings.ToLower(m.sessionListFilter)
	var filtered []SessionSummary
	for _, s := range m.sessionListSessions {
		if filter == "" || strings.Contains(strings.ToLower(s.Title), filter) {
			filtered = append(filtered, s)
		}
	}
	m.sessionListFiltered = filtered
	if m.sessionListIndex >= len(filtered) {
		m.sessionListIndex = len(filtered) - 1
		if m.sessionListIndex < 0 {
			m.sessionListIndex = 0
		}
	}
}

// handleSessionListKey handles key presses within the session list modal.
func (m *appModel) handleSessionListKey(key string) tea.Cmd {
	switch key {
	case "esc", "q":
		m.showSessionList = false
		m.sessionListFilter = ""
		return nil
	case "enter":
		if len(m.sessionListFiltered) > 0 && m.sessionListIndex < len(m.sessionListFiltered) {
			id := m.sessionListFiltered[m.sessionListIndex].ID
			m.showSessionList = false
			m.sessionListFilter = ""
			return m.switchToSession(id)
		}
		return nil
	case "up", "k":
		if m.sessionListIndex > 0 {
			m.sessionListIndex--
		}
		return nil
	case "down", "j":
		if m.sessionListIndex < len(m.sessionListFiltered)-1 {
			m.sessionListIndex++
		}
		return nil
	case "d":
		if len(m.sessionListFiltered) > 0 && m.sessionListIndex < len(m.sessionListFiltered) {
			id := m.sessionListFiltered[m.sessionListIndex].ID
			m.showSessionList = false
			m.sessionListFilter = ""
			return m.deleteSessionConfirm(id)
		}
		return nil
	case "a":
		if len(m.sessionListFiltered) > 0 && m.sessionListIndex < len(m.sessionListFiltered) {
			id := m.sessionListFiltered[m.sessionListIndex].ID
			m.showSessionList = false
			m.sessionListFilter = ""
			return m.archiveSessionToggle(id)
		}
		return nil
	}
	if len(key) == 1 && !strings.HasPrefix(key, "ctrl+") {
		m.sessionListFilter += key
		m.sessionListIndex = 0
		m.filterSessionList()
		return nil
	}
	if key == "backspace" || key == "ctrl+h" {
		if len(m.sessionListFilter) > 0 {
			m.sessionListFilter = m.sessionListFilter[:len(m.sessionListFilter)-1]
			m.sessionListIndex = 0
			m.filterSessionList()
		}
		return nil
	}
	return nil
}

// switchToSession switches the active session to the one with the given ID.
func (m *appModel) switchToSession(id string) tea.Cmd {
	if m.sessionService == nil {
		return nil
	}
	if err := m.sessionService.SetActiveSession(id); err != nil {
		m.appendLog("Error switching session: " + err.Error())
		return nil
	}
	session, err := m.sessionService.GetActiveSession()
	if err != nil {
		m.appendLog("Error loading session: " + err.Error())
		return nil
	}
	if session == nil {
		return nil
	}
	m.chatHistory = make([]ChatMessage, 0, len(session.Messages))
	for _, msg := range session.Messages {
		m.chatHistory = append(m.chatHistory, ChatMessage{
			Role:     msg.Role,
			Content:  msg.Content,
			Thinking: msg.Thinking,
		})
	}
	m.rebuildRenderedLines()
	m.chatScrollOffset = m.maxChatScrollOffset()
	m.renderChatViewport()
	m.appendLog("Resumed session: " + session.Title)
	return nil
}

// deleteSessionConfirm deletes a session by ID.
func (m *appModel) deleteSessionConfirm(id string) tea.Cmd {
	if m.sessionService == nil {
		return nil
	}
	summaries, err := m.sessionService.ListSessions()
	if err == nil {
		for _, s := range summaries {
			if s.ID == id {
				if err := m.sessionService.DeleteSession(id); err != nil {
					m.appendLog("Error deleting session: " + err.Error())
				} else {
					m.appendLog("Deleted session: " + s.Title)
					m.refreshSessionList()
				}
				return nil
			}
		}
	}
	m.appendLog("Session not found for deletion.")
	return nil
}

// archiveSessionToggle archives or unarchives a session.
func (m *appModel) archiveSessionToggle(id string) tea.Cmd {
	if m.sessionService == nil {
		return nil
	}
	summaries, err := m.sessionService.ListSessions()
	if err == nil {
		for _, s := range summaries {
			if s.ID == id {
				if err := m.sessionService.ArchiveSession(id); err != nil {
					m.appendLog("Error archiving session: " + err.Error())
				} else {
					m.appendLog("Archived session: " + s.Title)
					m.refreshSessionList()
				}
				return nil
			}
		}
	}
	archived, err := m.sessionService.ListArchivedSessions()
	if err == nil {
		for _, s := range archived {
			if s.ID == id {
				if err := m.sessionService.UnarchiveSession(id); err != nil {
					m.appendLog("Error unarchiving session: " + err.Error())
				} else {
					m.appendLog("Unarchived session: " + s.Title)
					m.refreshSessionList()
				}
				return nil
			}
		}
	}
	m.appendLog("Session not found for archive toggle.")
	return nil
}

// refreshSessionList reloads the session list from the store.
func (m *appModel) refreshSessionList() {
	if m.sessionService == nil {
		return
	}
	summaries, err := m.sessionService.ListSessions()
	if err == nil {
		m.sessionListSessions = summaries
		m.filterSessionList()
	}
}

// sessionListView renders the session list modal as an overlay.
func (m *appModel) sessionListView() string {
	if len(m.sessionListFiltered) == 0 {
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2).
			Render("  (no sessions)  ")
	}

	var b strings.Builder
	b.WriteString("┌─ Sessions ──────────────────────────────────────────┐\n")
	b.WriteString("│ Esc/q: close  │ Enter: open  │ d: delete  │ a: archive │\n")
	b.WriteString("│ Type to filter by title                              │\n")
	b.WriteString("├──────────────────────────────────────────────────────┤\n")

	maxLines := m.chatViewport.Height() - 6
	if maxLines < 1 {
		maxLines = 1
	}

	for i, s := range m.sessionListFiltered {
		if i >= maxLines {
			b.WriteString("│  ... (more)                                          │\n")
			break
		}
		indicator := " "
		if s.ID == m.sessionService.activeSessionID {
			indicator = ">"
		}
		archivedMark := ""
		if s.Archived {
			archivedMark = " [archived]"
		}
		line := fmt.Sprintf("│ %s %s%s", indicator, s.Title, archivedMark)
		// Pad to fill the box width
		for len(line) < 58 {
			line += " "
		}
		line += "│"
		b.WriteString(line + "\n")
	}

	b.WriteString("└──────────────────────────────────────────────────────┘")
	return b.String()
}

// renderSelectionPane re-renders the currently selected pane so the selection
// highlight reflects the latest start/end lines. Called on click, drag, and
// release to keep the highlight live and to clear it once copying is done.
func (m *appModel) renderSelectionPane() {
	switch m.selectionPane {
	case chatFocus:
		m.renderChatViewport()
	case logFocus:
		m.logViewport.SetContentLines(m.highlightLines(m.logLines, 0))
	case taskFocus:
		m.taskViewport.SetContent(strings.Join(m.highlightLines(m.taskLines, 0), "\n"))
	}
}

// highlightLines wraps lines within the active selection range with
// a highlight background style. The visibleStart parameter is the
// content index of the first visible line so the method can map
// viewport-relative indices to content indices.
func (m *appModel) highlightLines(lines []string, visibleStart int) []string {
	if !m.selectionActive || m.selectionStartLine == m.selectionEndLine {
		return lines
	}

	start, end := m.selectionStartLine, m.selectionEndLine
	if start > end {
		start, end = end, start
	}

	result := make([]string, len(lines))
	for i, line := range lines {
		contentIdx := visibleStart + i
		if contentIdx >= start && contentIdx <= end {
			result[i] = highlightStyle.Render(line)
		} else {
			result[i] = line
		}
	}
	return result
}

// mouseYToContentLine maps a screen Y coordinate to a line index within the
// selected pane's content. Returns -1 if the coordinate is above the pane.
// Dragging past the bottom edge clamps to the last line so a drag-to-select
// that overshoots still selects through the end.
//
// Screen layout (rows, 0-indexed):
//
//	0            status bar
//	1            chat/log top border
//	2 ..         chat/log content
//	logH+2       log bottom border
//	logH+3       task top border
//	logH+4 ..    task content
func (m *appModel) mouseYToContentLine(y int) int {
	switch m.selectionPane {
	case chatFocus:
		row := y - 2 // skip status bar + chat top border
		if row < 0 {
			return -1
		}
		if row >= m.chatViewport.Height() {
			row = m.chatViewport.Height() - 1
		}
		return clampLine(row+m.chatScrollOffset, len(m.renderedLines))
	case logFocus:
		row := y - 2
		if row < 0 {
			return -1
		}
		if row >= m.logViewport.Height() {
			row = m.logViewport.Height() - 1
		}
		return clampLine(row, len(m.logLines))
	case taskFocus:
		taskContentStart := m.logViewport.Height() + 4 // status + log pane + task top border
		row := y - taskContentStart
		if row < 0 {
			return -1
		}
		if row >= m.taskViewport.Height() {
			row = m.taskViewport.Height() - 1
		}
		return clampLine(row, len(m.taskLines))
	default:
		return -1
	}
}

// clampLine clamps idx into [0, n-1], returning -1 when there is no content.
func clampLine(idx, n int) int {
	if n <= 0 {
		return -1
	}
	if idx < 0 {
		return 0
	}
	if idx >= n {
		return n - 1
	}
	return idx
}

// copySelection copies the currently selected lines in the focused pane
// to the system clipboard, stripping ANSI escape sequences.
func (m *appModel) copySelection() {
	if m.selectionStartLine < 0 || m.selectionEndLine < 0 {
		return
	}
	if m.selectionStartLine == m.selectionEndLine {
		return
	}

	start, end := m.selectionStartLine, m.selectionEndLine
	if start > end {
		start, end = end, start
	}

	var lines []string
	switch m.selectionPane {
	case chatFocus:
		if start < 0 {
			start = 0
		}
		if end >= len(m.renderedLines) {
			end = len(m.renderedLines) - 1
		}
		if start > end {
			return
		}
		lines = m.renderedLines[start : end+1]
	case logFocus:
		if start < 0 {
			start = 0
		}
		if end >= len(m.logLines) {
			end = len(m.logLines) - 1
		}
		if start > end {
			return
		}
		lines = m.logLines[start : end+1]
	case taskFocus:
		if start < 0 {
			start = 0
		}
		if end >= len(m.taskLines) {
			end = len(m.taskLines) - 1
		}
		if start > end {
			return
		}
		lines = m.taskLines[start : end+1]
	default:
		return
	}

	text := strings.Join(lines, "\n")
	text = ansi.Strip(text)
	if text == "" {
		return
	}
	_ = copyToClipboard(text)
}
