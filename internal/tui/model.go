package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"coding-agent/internal/events"
	"coding-agent/internal/session"
	"coding-agent/internal/skills"
)

type Runner interface {
	Run(ctx context.Context, sess *session.Session, input string) error
}

type Deps struct {
	Runner     Runner
	Bus        *events.Bus
	Session    *session.Session
	Model      string
	Workspace  string
	Skills     func() []skills.Skill
	Context    context.Context
	RunTimeout time.Duration
}

type blockKind int

const (
	blockUser blockKind = iota
	blockAssistant
	blockTool
	blockThinking
	blockNotice
	blockError
)

// block 是对话区的一条记录。工具块在 ToolStarted 时创建，
// ToolFinished 时原地补上结果，保证“思考 → 工具 → 结果”按顺序排列。
type block struct {
	kind     blockKind
	round    int
	label    string
	text     string
	rendered string
	result   string
	running  bool
	failed   bool
}

type approvalState struct {
	id     string
	title  string
	detail string
}

type Model struct {
	deps   Deps
	styles Styles
	md     *Markdown

	input    textarea.Model
	viewport viewport.Model
	spinner  spinner.Model

	ch        <-chan events.Event
	cancelRun context.CancelFunc

	width, height int
	blocks        []block
	pendingTool   int
	follow        bool
	newOutput     bool
	lastError     string

	busy     bool
	status   string
	approval *approvalState
}

// New 组装初始界面。返回指针是因为 Update 需要就地修改视口和消息流，
// 复制整个 Model 会让滚动位置与状态出现分叉。
func New(deps Deps) *Model {
	if deps.Context == nil {
		deps.Context = context.Background()
	}

	m := &Model{
		deps:        deps,
		styles:      NewStyles(),
		md:          NewMarkdown(80),
		input:       textarea.New(),
		viewport:    viewport.New(80, 20),
		spinner:     spinner.New(spinner.WithSpinner(spinner.Dot)),
		pendingTool: -1,
		follow:      true,
		status:      "就绪",
		width:       100,
		height:      30,
	}
	m.spinner.Style = m.styles.Meta

	// 多行输入时 textarea 会在每一行重复 Prompt，因此把提示符放进 Placeholder。
	m.input.Placeholder = "输入需求，Enter 发送（/help 查看命令）"
	m.input.CharLimit = 0
	m.input.Prompt = ""
	m.input.ShowLineNumbers = false
	m.input.SetHeight(2)
	// Ctrl+Enter 与 Enter 在终端里是同一个字节，退而求其次支持 Alt+Enter 和 Ctrl+J。
	m.input.KeyMap.InsertNewline.SetKeys("alt+enter", "ctrl+j")
	m.input.Focus()

	m.viewport.MouseWheelEnabled = true
	m.viewport.MouseWheelDelta = 3

	if deps.Bus != nil {
		m.ch = deps.Bus.Subscribe()
	}
	m.layout(100, 30)
	// 历史消息只作为模型上下文存在，不在界面回放。
	m.appendBlock(blockNotice, "提示", "输入需求开始；输入 /help 查看命令。")
	m.syncViewport()
	return m
}

func (m Model) Init() tea.Cmd {
	commands := []tea.Cmd{m.input.Focus(), tea.SetWindowTitle("Go Coding Agent")}
	if m.ch != nil {
		commands = append(commands, waitForEvent(m.ch))
	}
	return tea.Batch(commands...)
}

// waitForEvent 每次只取一个事件，处理完后再重新挂起，避免多个 goroutine 争抢同一通道。
func waitForEvent(ch <-chan events.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg{Event: event}
	}
}

func (m *Model) waitCommand() tea.Cmd {
	if m.ch == nil {
		return nil
	}
	return waitForEvent(m.ch)
}

func (m *Model) appendBlock(kind blockKind, label, text string) {
	m.blocks = append(m.blocks, block{
		kind:     kind,
		label:    label,
		text:     text,
		rendered: m.render(kind, text),
	})
}

func (m *Model) render(kind blockKind, text string) string {
	if kind == blockAssistant {
		return m.md.Render(text)
	}
	return text
}

// markOutput 记录“用户正在看历史时又来了新内容”，用于标题栏提示。
func (m *Model) markOutput() {
	if !m.follow {
		m.newOutput = true
	}
}

// layout 按财务助手的分区计算尺寸：整体一个外框，内部依次是固定的标题、
// 可滚动的对话区、固定的底部输入框。所有尺寸都围绕外框倒推。
func (m *Model) layout(width, height int) {
	if width < 50 {
		width = 50
	}
	if height < 18 {
		height = 18
	}
	m.width, m.height = width, height

	m.viewport.Width = max(20, m.contentWidth())
	m.viewport.Height = m.bodyHeight()
	m.md.SetWidth(max(20, m.viewport.Width-4))
	m.input.SetWidth(m.footerContentWidth())
	m.input.SetHeight(2)

	m.reflow()
}

// contentWidth 是外框内部可用的宽度：终端宽减去左右留白(4)、边框(2)、内边距(2)。
func (m *Model) contentWidth() int {
	return max(30, m.width-8)
}

// contentHeight 是外框内部可用的高度：终端高减去上下留白(2)和边框(2)。
func (m *Model) contentHeight() int {
	return max(10, m.height-4)
}

// bodyHeight 是中间对话区高度：内部高度减去标题(1)、空行(1)、输入框(4)。
func (m *Model) bodyHeight() int {
	return max(5, m.contentHeight()-6)
}

// textWidth 是正文换行宽度；对话框有边框和内边距，再预留缩进。
func (m *Model) textWidth() int {
	return max(20, m.viewport.Width-8)
}

func (m *Model) reflow() {
	for i := range m.blocks {
		if m.blocks[i].kind == blockAssistant {
			m.blocks[i].rendered = m.md.Render(m.blocks[i].text)
		}
	}
	m.syncViewport()
}

func (m *Model) syncViewport() {
	m.viewport.SetContent(m.buildContent())
	if m.follow {
		m.viewport.GotoBottom()
	}
}

func (m *Model) buildContent() string {
	var builder strings.Builder
	for i, item := range m.blocks {
		if i > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(m.renderBlock(item))
	}
	return strings.TrimRight(builder.String(), "\n")
}

func (m *Model) renderBlock(item block) string {
	switch item.kind {
	case blockUser:
		label := m.styles.User.Render("你：")
		width := max(10, m.textWidth()-lipgloss.Width(label))
		return label + m.wrap(item.text, width)

	case blockAssistant:
		body := item.rendered
		if body == "" {
			body = item.text
		}
		return m.styles.Assistant.Render("助手：") + "\n" + indent(body, 4)

	case blockTool:
		return m.renderTool(item)

	case blockThinking:
		return m.renderThinking(item.round, "思考", item.text)

	case blockError:
		return m.styles.Error.Render("✗ " + item.text)

	default:
		label := item.label
		if label == "" {
			label = "提示"
		}
		return m.styles.Notice.Render("• "+label) + "\n" + m.wrap(item.text, m.textWidth())
	}
}

func (m *Model) renderTool(item block) string {
	name := item.label
	if name == "" {
		name = "tool"
	}

	line := m.styles.Tool.Render("⚙ " + name + " " + compactArgs(item.text))
	if item.running {
		line += m.styles.Meta.Render("  运行中…")
	} else if item.failed {
		line += m.styles.Error.Render("  失败")
	}

	var builder strings.Builder
	builder.WriteString(m.wrap(line, m.textWidth()))
	if item.result != "" {
		builder.WriteString("\n")
		result := m.styles.Result.Render("  ↳ " + truncateLine(item.result, 160))
		builder.WriteString(m.wrap(result, m.textWidth()))
	}
	return builder.String()
}

func (m *Model) renderThinking(round int, label, text string) string {
	title := "──── 🤔 "
	if round > 0 {
		title += fmt.Sprintf("第 %d 轮", round)
	}
	title += label + " ────"

	body := m.styles.Thinking.Width(m.textWidth()).PaddingLeft(4).Render(firstLines(text, 20))
	return m.styles.ThinkMark.Render(title) + "\n" + body
}

// wrap 在指定宽度内折行，避免长行被终端二次换行后撑破布局。
func (m *Model) wrap(text string, width int) string {
	return lipgloss.NewStyle().Width(max(10, width)).Render(text)
}

func indent(text string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func compactArgs(args string) string {
	args = strings.TrimSpace(strings.ReplaceAll(args, "\n", " "))
	return clip(args, 100)
}

func truncateLine(text string, limit int) string {
	text = strings.ReplaceAll(strings.TrimSpace(text), "\n", " ")
	return clip(text, limit)
}

func firstLines(text string, limit int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > limit {
		lines = append(lines[:limit], "…")
	}
	return strings.Join(lines, "\n")
}
