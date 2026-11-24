package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/displaywidth"

	"coding-agent/internal/session"
)

// eastAsianWidth 按中文终端的习惯，把 East Asian Ambiguous 字符
// （例如 “ ” ‘ ’ · … ——）计为 2 列。
//
// Charm 系列库（uniseg / displaywidth 默认值）按 1 列计算，而中文终端实际
// 渲染为 2 列。只要某一行因此宽出终端，终端就会自动换行，Bubble Tea 记录的
// 行数随之错位，旧画面（状态栏、输入框）残留并不断堆叠。这里用与终端一致的
// 宽度做最终裁剪，保证渲染出来的每一行都不会触发自动换行。
var eastAsianWidth = displaywidth.Options{EastAsianWidth: true}

// displayWidth 返回按终端实际列宽计算的宽度。
//
// 中文终端会把 “ ” ‘ ’ · … 等 East Asian Ambiguous 字符渲染为 2 列，
// 所以用 EastAsianWidth:true 计宽；但框线（U+2500–U+257F）与块元素
// （U+2580–U+259F）虽然也属于 Ambiguous，终端实际只渲染 1 列。这里把它们
// 修正回 1 列，否则每条带边框的行都会被多算 2 列，最终被截断——表现为
// 边框缺角、右边框消失、文字右侧被切。
func displayWidth(text string) int {
	w := eastAsianWidth.String(text)
	for _, r := range text {
		if isNarrowBoxRune(r) {
			w--
		}
	}
	if w < 0 {
		return 0
	}
	return w
}

func isNarrowBoxRune(r rune) bool {
	return (r >= 0x2500 && r <= 0x257F) || (r >= 0x2580 && r <= 0x259F)
}

// displayWidthANSI 返回跳过 ANSI 转义序列后的显示宽度。
func displayWidthANSI(text string) int {
	parser := ansi.GetParser()
	defer ansi.PutParser(parser)

	var state byte
	width := 0
	for len(text) > 0 {
		seq, w, n, newState := ansi.DecodeSequence(text, state, parser)
		if n <= 0 {
			break
		}
		if w > 0 {
			width += displayWidth(seq)
		}
		text = text[n:]
		state = newState
	}
	return width
}

// truncateDisplay 按 CJK 终端宽度截断一行，保留 ANSI 转义序列。
// displaywidth 不认识 ANSI 序列，所以这里逐个序列累加可显示宽度。
func truncateDisplay(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	parser := ansi.GetParser()
	defer ansi.PutParser(parser)

	var b strings.Builder
	var state byte
	used := 0
	truncated := false
	for len(text) > 0 {
		seq, width, n, newState := ansi.DecodeSequence(text, state, parser)
		if n <= 0 {
			truncated = true
			break
		}
		if width > 0 {
			w := displayWidth(seq)
			if used+w > limit {
				truncated = true
				break
			}
			used += w
		}
		b.WriteString(seq)
		text = text[n:]
		state = newState
	}
	if truncated {
		// 截断处可能还有未闭合的 SGR 样式，补一个重置避免颜色串到下一行。
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// wrapDisplay 按显示宽度折行，能正确处理 ANSI 转义序列与非 ASCII 字符。
//
// Glamour 底层的 reflow 只在空格处断词，中文长句没有空格，会被整行输出，
// 最后被外框裁掉（右侧文字缺失）。这里按终端列宽硬折行，保证长中文段落
// 也能完整换行显示。已有的换行会保留。
func wrapDisplay(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	parser := ansi.GetParser()
	defer ansi.PutParser(parser)

	var b strings.Builder
	var state byte
	lineWidth := 0
	for len(text) > 0 {
		seq, width, n, newState := ansi.DecodeSequence(text, state, parser)
		if n <= 0 {
			b.WriteString(text)
			break
		}
		switch {
		case strings.Contains(seq, "\n"):
			b.WriteString(seq)
			lineWidth = 0
		case width > 0:
			w := displayWidth(seq)
			if lineWidth > 0 && lineWidth+w > limit {
				b.WriteString("\n")
				lineWidth = 0
			}
			b.WriteString(seq)
			lineWidth += w
		default:
			b.WriteString(seq)
		}
		text = text[n:]
		state = newState
	}
	return b.String()
}

// View 渲染一个固定的整体界面：一个外框包住全部内容，
// 头部的标题与底部的状态行/输入框固定不动，中间对话区随内容滚动。
func (m Model) View() string {
	if m.width == 0 {
		return "正在初始化界面…"
	}

	header := m.styles.Title.Render("> Go Coding Agent")
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
	out := lipgloss.NewStyle().Margin(1, 2).Render(box)

	// 最后一层保险：按 CJK 宽度把每一行裁到终端宽度以内。
	// 只要有一行宽出终端，终端就会自动换行并破坏 Bubble Tea 的行数计算。
	return clipLines(out, max(1, m.width-1))
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
	return strings.Join(parts, "  |  ")
}

// footerView 由“状态行 + 输入框”组成，状态行固定占一行、输入框固定占四行。
//
// 过程词（思考中、运行工具、等待审批）只出现在这一行里并原地刷新，
// 不再替换输入框、也不会写进对话区反复堆叠。
func (m Model) footerView() string {
	contentWidth := m.footerContentWidth()
	styleWidth := m.footerStyleWidth()

	var status string
	switch {
	case m.approval != nil:
		hint := "! 需要审批：" + m.approval.title + "（y 允许 | n 拒绝 | Ctrl+C 取消）"
		status = m.styles.Approve.Render(truncateDisplay(hint, contentWidth))
	case m.busy:
		status = m.spinner.View() + " " +
			m.styles.Meta.Render(truncateDisplay(m.status, max(10, contentWidth-2)))
	default:
		status = m.styles.Meta.Render(truncateDisplay(m.status, contentWidth))
	}

	return lipgloss.JoinVertical(lipgloss.Left, status,
		m.styles.Input.Width(styleWidth).Render(m.input.View()))
}

// innerWidth 是外框去掉左右 padding 后的可用宽度。
func (m Model) innerWidth() int {
	return m.contentWidth()
}

// footerContentWidth 是输入/状态框去掉边框与 padding 后的内容宽度。
func (m Model) footerContentWidth() int {
	return max(20, m.contentWidth()-4)
}

// footerStyleWidth 是输入框的 Width（含 padding，不含边框）。
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
		lines[i] = truncateDisplay(line, width)
	}
	return strings.Join(lines, "\n")
}

// clipLines 把每一行裁到给定宽度，行数保持不变。
func clipLines(text string, width int) string {
	if width <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = truncateDisplay(lines[i], width)
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
