package tui

import "github.com/charmbracelet/lipgloss"

// Styles 集中管理外观，配色和边框参考财务助手：标题 + 一个对话框 + 底部输入框。
type Styles struct {
	Title     lipgloss.Style
	Meta      lipgloss.Style
	User      lipgloss.Style
	Assistant lipgloss.Style
	Thinking  lipgloss.Style
	ThinkMark lipgloss.Style
	Tool      lipgloss.Style
	Result    lipgloss.Style
	Notice    lipgloss.Style
	Error     lipgloss.Style
	Frame     lipgloss.Style
	Input     lipgloss.Style
	StatusBox lipgloss.Style
	Approve   lipgloss.Style
}

func NewStyles() Styles {
	return Styles{
		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFA500")),
		Meta: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666")),
		User: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#04B575")),
		Assistant: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7D56F4")),
		Thinking: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			Italic(true),
		ThinkMark: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#BB88FF")),
		Tool: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3C9AE8")),
		Result: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666")),
		Notice: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFA500")),
		Error: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FF5F5F")),
		// Frame 是包住整个界面的唯一外框，固定不变。
		Frame: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#3C3C3C")).
			Padding(0, 1),
		Input: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(0, 1),
		// StatusBox 在运行/审批时占据与输入框相同的高度，保证底部不跳动。
		StatusBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(0, 1),
		Approve: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFA500")),
	}
}
