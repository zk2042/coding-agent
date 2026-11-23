package tui

import "coding-agent/internal/events"

// eventMsg 把总线事件包装成 BubbleTea 消息。
type eventMsg struct {
	Event events.Event
}

// runDoneMsg 表示一次 Agent 调用结束；错误在 Update 中统一展示。
type runDoneMsg struct {
	Err error
}
