package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// Markdown 在消息定稿后渲染，避免每个 token 都触发一次排版。
type Markdown struct {
	width    int
	renderer *glamour.TermRenderer
}

func NewMarkdown(width int) *Markdown {
	m := &Markdown{}
	m.SetWidth(width)
	return m
}

func (m *Markdown) SetWidth(width int) {
	if width < 20 {
		width = 20
	}
	if m.renderer != nil && m.width == width {
		return
	}
	m.width = width
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width-6),
	)
	if err == nil {
		m.renderer = renderer
	}
}

func (m *Markdown) Render(text string) string {
	if m.renderer == nil || strings.TrimSpace(text) == "" {
		return text
	}
	out, err := m.renderer.Render(text)
	if err != nil {
		return text
	}
	// Glamour 只在空格处断词，中文长句不会自动换行，会整行输出后被外框裁掉。
	// 这里再按终端列宽硬折行一次，保证中文段落完整显示。
	return wrapDisplay(strings.TrimRight(out, "\n"), m.width)
}
