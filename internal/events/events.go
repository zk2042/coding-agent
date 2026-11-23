// Package events 提供 Agent 运行期间的事件总线。
// 它不依赖任何业务包，UI、日志或测试都可以作为订阅者接入。
package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Kind 标识事件类型。用字符串而不是 int，便于日志排查和测试断言。
type Kind string

const (
	// RunStarted 一次任务开始，Text 是用户输入。
	RunStarted Kind = "run_started"

	// Reasoning 模型在最终回答前输出的思考内容，仅用于展示，不写入会话。
	Reasoning Kind = "reasoning"

	// ToolStarted 模型决定调用某个工具，Args 是原始 JSON 参数。
	ToolStarted Kind = "tool_started"

	// ToolFinished 工具执行结束，Result 是返回内容或错误说明。
	ToolFinished Kind = "tool_finished"

	// AssistantDone 模型给出最终自然语言回答。
	AssistantDone Kind = "assistant_done"

	// Notice 面向用户的提示，不参与任务逻辑。
	Notice Kind = "notice"

	// Failed 本次任务不可恢复地失败，Err 是原因。
	Failed Kind = "failed"

	// RunFinished 本次任务结束，无论成功还是失败都会发送一次。
	RunFinished Kind = "run_finished"

	// ApprovalRequested 工具请求人工审批，UI 通过 ResolveApproval 回复。
	ApprovalRequested Kind = "approval_requested"
)

// Event 是 Agent 向外广播的统一事件。
// 不相关字段留空，订阅者只按 Kind 读取自己关心的部分。
type Event struct {
	Kind           Kind
	Time           time.Time
	Round          int // 第几轮模型调用，从 1 开始；0 表示与轮次无关
	Text           string
	Tool           string // 工具名
	Args           string // 工具参数（JSON 字符串）
	Result         string // 工具结果或错误说明
	Err            string
	ApprovalID     string
	ApprovalTitle  string
	ApprovalDetail string
}

// Decision 是用户对一次审批的回答。
type Decision struct {
	Allow bool
}

// Bus 是进程内的事件总线，支持多个订阅者和一次审批往返。
// 它不依赖任何业务包，TUI、日志和测试都可以接入。
type Bus struct {
	mu      sync.RWMutex
	subs    map[chan Event]struct{}
	pending map[string]chan Decision
	closed  bool
}

// NewBus 创建一条空总线；订阅与发布都是并发安全的。
func NewBus() *Bus {
	return &Bus{
		subs:    make(map[chan Event]struct{}),
		pending: make(map[string]chan Decision),
	}
}

// Subscribe 返回一个带缓冲的只读通道。订阅者退出时应调用 Unsubscribe。
func (b *Bus) Subscribe() <-chan Event {
	ch := make(chan Event, 256)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(ch)
		return ch
	}
	b.subs[ch] = struct{}{}
	return ch
}

// Unsubscribe 移除订阅并关闭通道。重复调用是安全的。
func (b *Bus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs {
		if sub == ch {
			delete(b.subs, sub)
			close(sub)
			return
		}
	}
}

// Publish 采用非阻塞发送：消费慢的订阅者只会丢失事件，不会拖住 Agent。
func (b *Bus) Publish(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for sub := range b.subs {
		select {
		case sub <- ev:
		default:
		}
	}
}

// RequestApproval 发布审批事件，并等待 UI 通过 ResolveApproval 给出决定。
func (b *Bus) RequestApproval(ctx context.Context, title, detail string) (Decision, error) {
	id := approvalID()
	reply := make(chan Decision, 1)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return Decision{}, errors.New("事件总线已关闭")
	}
	b.pending[id] = reply
	b.mu.Unlock()

	b.Publish(Event{
		Kind:           ApprovalRequested,
		ApprovalID:     id,
		ApprovalTitle:  title,
		ApprovalDetail: detail,
	})

	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	select {
	case d := <-reply:
		return d, nil
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	}
}

// ResolveApproval 返回 false 表示该审批已过期或被重复处理。
func (b *Bus) ResolveApproval(id string, d Decision) bool {
	b.mu.Lock()
	reply, ok := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case reply <- d:
	default:
	}
	return true
}

// Close 关闭所有订阅通道，并让等待中的审批立即返回拒绝。
func (b *Bus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	for sub := range b.subs {
		close(sub)
	}
	b.subs = map[chan Event]struct{}{}
	pending := b.pending
	b.pending = map[string]chan Decision{}
	b.mu.Unlock()

	for _, reply := range pending {
		select {
		case reply <- Decision{}:
		default:
		}
	}
}

func approvalID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
