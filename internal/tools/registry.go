package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"coding-agent/internal/llm"
)

// Registry 保存工具定义并按名字调度。
// 它支持运行期热注册，供 MCP 在后台连接完成后追加工具。
type Registry struct {
	mu     sync.RWMutex
	order  []string
	byName map[string]Tool
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Tool)}
}

// Add 注册或覆盖同名工具；同名时保留首次出现的顺序。
func (r *Registry) Add(tool Tool) {
	if tool == nil {
		return
	}
	name := tool.Definition().Name
	if name == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[name]; !exists {
		r.order = append(r.order, name)
	}
	r.byName[name] = tool
}

// Definitions 按注册顺序返回工具定义，可直接交给模型。
func (r *Registry) Definitions() []llm.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]llm.Tool, 0, len(r.order))
	for _, name := range r.order {
		defs = append(defs, r.byName[name].Definition())
	}
	return defs
}

// Names 返回注册顺序的工具名，主要用于提示和调试。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, len(r.order))
	copy(names, r.order)
	return names
}

// Execute 按名字调用工具。参数为空时按空对象处理，错误会带上工具名。
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	r.mu.RLock()
	tool, ok := r.byName[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("未知工具: %s", name)
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}

	out, err := tool.Execute(ctx, args)
	if err != nil {
		return out, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}
