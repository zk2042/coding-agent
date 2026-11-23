package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"coding-agent/internal/llm"
	"coding-agent/internal/tools"
)

const requestTimeout = 45 * time.Second

type server struct {
	name    string
	session *mcp.ClientSession
	logs    *logBuffer
}

// Manager 包装官方 MCP SDK，把远程工具适配成本地 tools.Tool。
type Manager struct {
	root     string
	approver tools.Approver

	mu      sync.Mutex
	servers []*server
	items   []tools.Tool
}

func NewManager(workspaceRoot string, approver tools.Approver) *Manager {
	return &Manager{root: workspaceRoot, approver: approver}
}

// Start 逐个连接配置中的 Server；单个失败不影响其他 Server。
func (m *Manager) Start(ctx context.Context, configs []Config) []error {
	var errs []error
	for _, cfg := range configs {
		srv, err := startServer(ctx, cfg, m.root)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s 启动失败: %w", cfg.Name, err))
			continue
		}
		items, err := srv.discover(ctx, m.approver, cfg.ApprovalRequired)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s 工具发现失败: %w", cfg.Name, err))
			srv.Close()
			continue
		}

		m.mu.Lock()
		m.servers = append(m.servers, srv)
		m.items = append(m.items, items...)
		m.mu.Unlock()
	}
	return errs
}

func (m *Manager) Tools() []tools.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]tools.Tool, len(m.items))
	copy(out, m.items)
	return out
}

func (m *Manager) Close() {
	m.mu.Lock()
	servers := m.servers
	m.servers = nil
	m.items = nil
	m.mu.Unlock()

	for _, srv := range servers {
		srv.Close()
	}
}

func startServer(ctx context.Context, cfg Config, workspaceRoot string) (*server, error) {
	// 子进程 stderr 不能直连终端，否则会破坏 BubbleTea 的全屏渲染。
	logs := &logBuffer{}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Env = append(os.Environ(), envPairs(cfg.Env)...)
	if workspaceRoot != "" {
		cmd.Env = append(cmd.Env, "AGENT_WORKSPACE="+workspaceRoot)
	}
	cmd.Stderr = logs

	client := mcp.NewClient(&mcp.Implementation{Name: "coding-agent", Version: "0.1.0"}, nil)
	connectCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	session, err := client.Connect(connectCtx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		// 握手失败时子进程可能已经拉起，主动回收，避免留下孤儿进程。
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, fmt.Errorf("握手失败: %w%s", err, stderrSuffix(logs.Tail()))
	}
	return &server{name: cfg.Name, session: session, logs: logs}, nil
}

func (s *server) discover(ctx context.Context, approver tools.Approver, requireApproval bool) ([]tools.Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var items []tools.Tool
	for tool, err := range s.session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("工具 %q 的输入 schema: %w", tool.Name, err)
		}
		schema, err := normalizeSchema(raw)
		if err != nil {
			return nil, fmt.Errorf("工具 %q 的输入 schema: %w", tool.Name, err)
		}
		items = append(items, &remoteTool{
			server:          s,
			original:        tool.Name,
			approver:        approver,
			requireApproval: requireApproval,
			definition: llm.Tool{
				Name:        toolName(s.name, tool.Name),
				Description: tool.Description,
				Parameters:  schema,
			},
		})
	}
	return items, nil
}

func (s *server) call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	params, err := callParams(name, args)
	if err != nil {
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	result, err := s.session.CallTool(callCtx, params)
	if err != nil {
		return "", fmt.Errorf("tools/call %q: %w%s", name, err, stderrSuffix(s.logs.Tail()))
	}
	text := renderResult(result)
	if result.IsError {
		return text, fmt.Errorf("MCP 工具 %q 返回错误", name)
	}
	return text, nil
}

// callParams 把模型给出的 JSON 参数转成 MCP 调用参数。
// 必须解码成 map：如果直接把 []byte 赋给 Arguments（any），json 会把它
// 编码成 base64 字符串，服务端会报 "expected record, received string"。
func callParams(name string, args json.RawMessage) (*mcp.CallToolParams, error) {
	trimmed := bytes.TrimSpace(args)
	params := &mcp.CallToolParams{Name: name}
	if len(trimmed) == 0 {
		return params, nil
	}
	if trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, fmt.Errorf("MCP 工具参数必须是 JSON 对象")
	}
	var object map[string]any
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, fmt.Errorf("MCP 工具参数必须是 JSON 对象: %w", err)
	}
	params.Arguments = object
	return params, nil
}

// renderResult 把 MCP 内容转成模型可读的文本。
// 图片（如截图）只保留占位说明，避免 base64 撑爆上下文。
func renderResult(result *mcp.CallToolResult) string {
	var builder strings.Builder
	for _, item := range result.Content {
		switch content := item.(type) {
		case *mcp.TextContent:
			builder.WriteString(content.Text)
		case *mcp.ImageContent:
			fmt.Fprintf(&builder, "[图片 %s，%d 字节，已省略]", content.MIMEType, len(content.Data))
		default:
			if data, err := json.Marshal(content); err == nil {
				builder.Write(data)
			}
		}
		builder.WriteString("\n")
	}
	if result.StructuredContent != nil {
		if data, err := json.MarshalIndent(result.StructuredContent, "", "  "); err == nil {
			builder.Write(data)
			builder.WriteString("\n")
		}
	}
	text := strings.TrimSpace(builder.String())
	if text == "" {
		return "（工具没有返回内容）"
	}
	return text
}

func (s *server) Close() {
	_ = s.session.Close()
}

// normalizeSchema 保证没有声明输入的工具也能得到合法的对象 schema。
func normalizeSchema(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return emptySchema(), nil
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	if schema == nil {
		return emptySchema(), nil
	}
	return schema, nil
}

func emptySchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// toolName 生成模型可接受的函数名：只保留字母、数字、下划线和短横线，并限制在 64 字符内。
func toolName(server, tool string) string {
	raw := "mcp__" + server + "__" + tool
	var builder strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	name := builder.String()
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

type remoteTool struct {
	server          *server
	original        string
	definition      llm.Tool
	approver        tools.Approver
	requireApproval bool
}

func (t *remoteTool) Definition() llm.Tool {
	return t.definition
}

func (t *remoteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.requireApproval {
		if t.approver == nil {
			return "", fmt.Errorf("未配置审批器，已拒绝 MCP 调用")
		}
		allowed, err := t.approver.Approve(ctx, tools.Approval{
			Title:  "MCP 工具调用：" + t.definition.Name,
			Detail: string(args),
		})
		if err != nil {
			return "", err
		}
		if !allowed {
			return "用户拒绝了 MCP 调用", nil
		}
	}
	return t.server.call(ctx, t.original, args)
}

func envPairs(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	pairs := make([]string, 0, len(env))
	for key, value := range env {
		pairs = append(pairs, strings.TrimSpace(key)+"="+value)
	}
	return pairs
}

// logBuffer 只保留最近一段 stderr，用于启动失败时诊断，同时避免无限增长。
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

const maxLogBytes = 4096

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, _ = b.buf.Write(p)
	if b.buf.Len() > maxLogBytes {
		data := b.buf.Bytes()
		b.buf.Reset()
		_, _ = b.buf.Write(data[len(data)-maxLogBytes:])
	}
	return len(p), nil
}

func (b *logBuffer) Tail() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.buf.String())
}

func stderrSuffix(logs string) string {
	if logs == "" {
		return ""
	}
	return "\nstderr:\n" + logs
}
