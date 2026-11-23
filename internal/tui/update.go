package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"coding-agent/internal/events"
	"coding-agent/internal/session"
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.layout(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		return m, m.handleKey(msg)

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		m.follow = m.viewport.AtBottom()
		if m.follow {
			m.newOutput = false
		}
		return m, cmd

	case eventMsg:
		return m.handleEvent(msg.Event)

	case runDoneMsg:
		m.busy = false
		m.approval = nil
		m.pendingTool = -1
		switch {
		case msg.Err == nil:
			m.status = "就绪"
		case errors.Is(msg.Err, context.Canceled):
			m.status = "已取消"
		case msg.Err.Error() != m.lastError:
			m.appendBlock(blockError, "错误", msg.Err.Error())
			m.status = "任务结束（有错误）"
		default:
			m.status = "任务结束（有错误）"
		}
		m.input.Focus()
		m.syncViewport()
		return m, nil

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	// 非按键消息（如光标闪烁）只在空闲时交给输入框，运行中不打扰输入区。
	if m.busy {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	// 滚动与任务状态无关：运行中、审批时都可以翻阅对话。
	switch msg.String() {
	case "pgup":
		m.follow = false
		m.viewport.PageUp()
		return nil
	case "pgdown":
		m.viewport.PageDown()
		m.follow = m.viewport.AtBottom()
		if m.follow {
			m.newOutput = false
		}
		return nil
	case "ctrl+u":
		m.follow = false
		m.viewport.HalfPageUp()
		return nil
	case "ctrl+d":
		m.viewport.HalfPageDown()
		m.follow = m.viewport.AtBottom()
		if m.follow {
			m.newOutput = false
		}
		return nil
	}

	if m.approval != nil {
		switch msg.String() {
		case "y", "Y":
			m.resolveApproval(true)
		case "n", "N", "esc":
			m.resolveApproval(false)
		case "ctrl+c":
			// 审批期间也要能退出等待：先拒绝，再取消当前任务。
			m.resolveApproval(false)
			m.cancelTask()
			m.status = "已取消"
		}
		return nil
	}

	switch msg.String() {
	case "ctrl+c":
		if m.busy {
			m.cancelTask()
			m.status = "已请求取消…"
			return nil
		}
		return tea.Quit
	case "esc":
		if m.busy {
			m.cancelTask()
			m.status = "已请求取消…"
			return nil
		}
	}

	// 运行中只允许滚动与取消，避免按键进入输入框；底部此时展示 spinner 而不是输入框。
	if m.busy {
		return nil
	}

	if msg.String() == "enter" {
		return m.submit()
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

func (m *Model) cancelTask() {
	if m.cancelRun != nil {
		m.cancelRun()
	}
}

func (m *Model) submit() tea.Cmd {
	input := strings.TrimSpace(m.input.Value())
	if input == "" {
		return nil
	}
	if strings.HasPrefix(input, "/") {
		m.input.Reset()
		return m.command(input)
	}
	if m.deps.Runner == nil {
		m.input.Reset()
		m.appendBlock(blockError, "错误", "运行器未配置")
		m.syncViewport()
		return nil
	}

	m.input.Reset()
	m.input.Blur()
	m.appendBlock(blockUser, "你", input)
	m.pendingTool = -1
	m.lastError = ""
	m.follow = true
	m.newOutput = false
	m.busy = true
	m.status = "第 1 轮 · 思考中…"
	m.syncViewport()

	parent := m.deps.Context
	if parent == nil {
		parent = context.Background()
	}
	var (
		runCtx context.Context
		cancel context.CancelFunc
	)
	if m.deps.RunTimeout > 0 {
		runCtx, cancel = context.WithTimeout(parent, m.deps.RunTimeout)
	} else {
		runCtx, cancel = context.WithCancel(parent)
	}
	m.cancelRun = cancel

	runner := m.deps.Runner
	sess := m.deps.Session
	run := func() tea.Msg {
		defer cancel()
		return runDoneMsg{Err: runner.Run(runCtx, sess, input)}
	}
	return tea.Batch(run, m.spinner.Tick)
}

func (m *Model) command(input string) tea.Cmd {
	fields := strings.Fields(input)
	name := fields[0]

	switch name {
	case "/help":
		m.appendBlock(blockNotice, "查看命令", `查看命令：
/help    查看帮助
/new     新建会话（清空上下文）
/skills  查看 Skill 索引
/clear   清空对话区
/exit    退出

快捷键：Enter 发送，Alt+Enter 或 Ctrl+J 换行，PgUp/PgDn 或鼠标滚轮滚动，Ctrl+C 取消任务/退出。`)

	case "/new":
		sess := session.New()
		m.deps.Session = sess
		m.blocks = nil
		m.pendingTool = -1
		m.lastError = ""
		m.follow = true
		m.appendBlock(blockNotice, "提示", "已新建会话；本次进程退出后上下文不保留。")

	case "/skills":
		if m.deps.Skills == nil {
			m.appendBlock(blockNotice, "Skill", "（未配置 Skill 目录）")
			break
		}
		list := m.deps.Skills()
		if len(list) == 0 {
			m.appendBlock(blockNotice, "Skill", "（skills 目录为空）")
			break
		}
		var builder strings.Builder
		for _, skill := range list {
			fmt.Fprintf(&builder, "- %s：%s\n", skill.Name, skill.Description)
		}
		m.appendBlock(blockNotice, "Skill", strings.TrimRight(builder.String(), "\n"))

	case "/clear":
		m.blocks = nil
		m.pendingTool = -1
		m.newOutput = false
		m.follow = true

	case "/exit", "/quit":
		return tea.Quit

	default:
		m.appendBlock(blockNotice, "提示", "未知命令："+name+"。输入 /help 查看帮助。")
	}

	m.syncViewport()
	return nil
}

func (m *Model) resolveApproval(allow bool) {
	if m.approval == nil {
		return
	}
	title := m.approval.title
	if m.deps.Bus != nil {
		m.deps.Bus.ResolveApproval(m.approval.id, events.Decision{Allow: allow})
	}
	if allow {
		m.status = "已允许，继续执行…"
		m.appendBlock(blockNotice, "审批", "已允许："+title)
	} else {
		m.status = "已拒绝"
		m.appendBlock(blockNotice, "审批", "已拒绝："+title)
	}
	m.approval = nil
	m.syncViewport()
}

func roundStatus(round int, text string) string {
	if round > 0 {
		return fmt.Sprintf("第 %d 轮 · %s", round, text)
	}
	return text
}

func (m *Model) handleEvent(event events.Event) (tea.Model, tea.Cmd) {
	switch event.Kind {
	case events.RunStarted:
		m.busy = true
		m.pendingTool = -1
		m.lastError = ""
		m.status = roundStatus(event.Round, "思考中…")
		m.syncViewport()
		return m, tea.Batch(m.waitCommand(), m.spinner.Tick)

	case events.Reasoning:
		text := strings.TrimSpace(event.Text)
		if text == "" {
			return m, m.waitCommand()
		}
		m.blocks = append(m.blocks, block{
			kind:  blockThinking,
			round: event.Round,
			text:  text,
		})
		m.markOutput()
		m.status = roundStatus(event.Round, "思考完成")
		m.syncViewport()
		return m, m.waitCommand()

	case events.ToolStarted:
		m.blocks = append(m.blocks, block{
			kind:    blockTool,
			round:   event.Round,
			label:   event.Tool,
			text:    compactArgs(event.Args),
			running: true,
		})
		m.pendingTool = len(m.blocks) - 1
		m.markOutput()
		m.status = roundStatus(event.Round, "运行工具："+event.Tool)
		m.syncViewport()
		return m, m.waitCommand()

	case events.ToolFinished:
		result := firstLines(event.Result, 6)
		failed := strings.HasPrefix(event.Result, "工具执行失败") || strings.HasPrefix(event.Result, "用户拒绝")
		if m.pendingTool >= 0 && m.pendingTool < len(m.blocks) && m.blocks[m.pendingTool].kind == blockTool {
			m.blocks[m.pendingTool].result = result
			m.blocks[m.pendingTool].failed = failed
			m.blocks[m.pendingTool].running = false
		} else {
			m.blocks = append(m.blocks, block{
				kind:   blockTool,
				round:  event.Round,
				label:  event.Tool,
				result: result,
				failed: failed,
			})
		}
		m.pendingTool = -1
		m.markOutput()
		m.status = roundStatus(event.Round, "思考中…")
		m.syncViewport()
		return m, m.waitCommand()

	case events.AssistantDone:
		if strings.TrimSpace(event.Text) != "" {
			m.blocks = append(m.blocks, block{
				kind:     blockAssistant,
				round:    event.Round,
				label:    "Agent",
				text:     event.Text,
				rendered: m.md.Render(event.Text),
			})
		}
		m.status = "整理回答…"
		m.syncViewport()
		return m, m.waitCommand()

	case events.Notice:
		m.appendBlock(blockNotice, "提示", event.Text)
		m.syncViewport()
		return m, m.waitCommand()

	case events.Failed:
		m.lastError = event.Err
		m.appendBlock(blockError, "错误", event.Err)
		m.status = "任务失败"
		m.syncViewport()
		return m, m.waitCommand()

	case events.RunFinished:
		if m.pendingTool >= 0 && m.pendingTool < len(m.blocks) && m.blocks[m.pendingTool].kind == blockTool {
			m.blocks[m.pendingTool].running = false
			if m.blocks[m.pendingTool].result == "" {
				m.blocks[m.pendingTool].result = "任务已结束"
			}
		}
		m.pendingTool = -1
		m.busy = false
		m.approval = nil
		m.status = "就绪"
		m.input.Focus()
		m.syncViewport()
		return m, m.waitCommand()

	case events.ApprovalRequested:
		m.approval = &approvalState{
			id:     event.ApprovalID,
			title:  event.ApprovalTitle,
			detail: event.ApprovalDetail,
		}
		text := event.ApprovalTitle
		if detail := strings.TrimSpace(event.ApprovalDetail); detail != "" {
			text += "\n" + firstLines(ansi.Hardwrap(detail, m.textWidth(), false), 8)
		}
		text += "\n按 y 允许，n 拒绝。"
		m.appendBlock(blockNotice, "等待审批", text)
		m.status = "等待审批"
		m.syncViewport()
		return m, m.waitCommand()
	}

	m.syncViewport()
	return m, m.waitCommand()
}
