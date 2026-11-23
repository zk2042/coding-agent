package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"coding-agent/internal/session"
)

// View 渲染一个固定的整体界面：一个外框包住全部内容，
// 头部的标题与底部的输入框固定不动，中间对话区随内容滚动。
func (m Model) View() string {
	if m.width == 0 {
		return "正在初始化界面…"
	}

	header := m.styles.Title.Render("◆ Go Coding Agent")
	if meta := m.metaText(); meta != "" {
		width := max(10, m.innerWidth()-lipgloss.Width(header)-4)
		header += "  " + m.styles.Meta.Render(ansi.Truncate(meta, width, "…"))
	}

	inner := lipgloss.JoinVertical(lipgloss.Left,
		header,
		"",
		m.viewport.View(),
		m.footerView(),
	)
	// 兜底：任何一行超宽都会把外框挤变形，这里按显示宽度截断。
	inner = fitLines(inner, m.innerWidth())

	box := m.styles.Frame.
		Width(m.contentWidth() + 2).
		Height(m.contentHeight()).
		Render(inner)

	// 四周留白：宽度减 4、高度减 2 已在 layout 中预留，这里只负责居中留白。
	return lipgloss.NewStyle().Margin(1, 2).Render(box)
}

func (m Model) metaText() string {
	parts := make([]string, 0, 3)
	if m.deps.Model != "" {
		parts = append(parts, m.deps.Model)
	}
	if m.deps.Workspace != "" {
		parts = append(parts, clip(m.deps.Workspace, 36))
	}
	if m.deps.Session != nil {
		parts = append(parts, "会话 "+clip(sessionTitle(m.deps.Session), 20))
	}
	return strings.Join(parts, "  ·  ")
}

// footerView 始终占两行内容（加边框共四行）：
// 空闲时是输入框，运行中或审批时是同尺寸的状态框，底部不会跳动。
func (m Model) footerView() string {
	contentWidth := m.footerContentWidth()
	styleWidth := m.footerStyleWidth()

	if m.approval != nil {
		hint := m.styles.Approve.Render(ansi.Truncate("⚠ 需要审批："+m.approval.title, contentWidth, "…"))
		keys := m.styles.Meta.Render(ansi.Truncate("y 允许 · n 拒绝 · Ctrl+C 取消", contentWidth, "…"))
		return m.styles.StatusBox.Width(styleWidth).Render(lipgloss.JoinVertical(lipgloss.Left, hint, keys))
	}
	if m.busy {
		line := m.spinner.View() + " " + m.styles.Meta.Render(ansi.Truncate(m.status, max(10, contentWidth-2), "…"))
		return m.styles.StatusBox.Width(styleWidth).Render(lipgloss.JoinVertical(lipgloss.Left, line, ""))
	}
	return m.styles.Input.Width(styleWidth).Render(m.input.View())
}

// innerWidth 是外框去掉左右 padding 后的可用宽度。
func (m Model) innerWidth() int {
	return m.contentWidth()
}

// footerContentWidth 是输入/状态框去掉边框与 padding 后的内容宽度。
func (m Model) footerContentWidth() int {
	return max(20, m.contentWidth()-4)
}

// footerStyleWidth 是输入/状态框的 Width（含 padding，不含边框）。
func (m Model) footerStyleWidth() int {
	return max(20, m.contentWidth()-2)
}

// fitLines 按显示宽度截断每一行，确保长 URL、代码片段不会撑破外框。
func fitLines(text string, width int) string {
	if width <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

func sessionTitle(sess *session.Session) string {
	title := strings.TrimSpace(sess.Title)
	if title == "" {
		title = sess.ID
	}
	return title
}
