// Package session 维护一次进程运行期间的对话上下文。
// 会话只存在于内存：Summary 是更早对话的压缩摘要，Items 是最近窗口的原始 item。
// 进程退出后会话即结束，不读写磁盘。
package session

import (
	"strings"
	"time"

	"coding-agent/internal/llm"
)

// Session 是运行时上下文，模型只会看到 Summary + Items。
type Session struct {
	ID      string
	Title   string
	Summary string
	Items   []llm.Item
}

// New 创建一个空会话。
func New() *Session {
	return &Session{ID: time.Now().Format("20060102-150405")}
}

// Append 把新的 item 追加到最近窗口；标题取首条有效的用户消息。
func (s *Session) Append(items ...llm.Item) {
	if s == nil || len(items) == 0 {
		return
	}
	s.Items = append(s.Items, items...)
	if s.Title == "" {
		for _, item := range items {
			if item.Type == llm.ItemMessage && item.Role == llm.RoleUser && strings.TrimSpace(item.Text) != "" {
				s.Title = excerpt(firstLine(item.Text), 40)
				break
			}
		}
	}
}

func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func excerpt(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}
