package main

// 入口文件：只负责组装依赖，不含业务逻辑。
//
// 组装流程：
//  1. 读配置，创建本次进程的会话并加载长期记忆
//  2. 注册本地工具
//  3. 后台连接 MCP Server，工具就绪后热注册
//  4. 创建 Agent 与 TUI，进入事件循环

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"coding-agent/internal/agent"
	"coding-agent/internal/config"
	"coding-agent/internal/events"
	"coding-agent/internal/llm"
	"coding-agent/internal/mcp"
	"coding-agent/internal/memory"
	"coding-agent/internal/session"
	"coding-agent/internal/skills"
	"coding-agent/internal/tools"
	"coding-agent/internal/tui"
)

// busApprover 把事件总线接到工具的审批端口，业务层因此不需要认识 TUI。
type busApprover struct {
	bus *events.Bus
}

func (a busApprover) Approve(ctx context.Context, req tools.Approval) (bool, error) {
	// 审批是在等用户操作，不应该消耗任务的总时限。
	// 这里只继承“主动取消”（Ctrl+C / 退出），忽略父 ctx 的 deadline。
	approvalCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				cancel()
			}
		case <-done:
		}
	}()

	decision, err := a.bus.RequestApproval(approvalCtx, req.Title, req.Detail)
	if err != nil {
		return false, err
	}
	return decision.Allow, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败：", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	if err := config.Validate(cfg); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bus := events.NewBus()
	defer bus.Close()

	sess := session.New()

	var startupNotices []string
	var mem *memory.Store
	if cfg.MemoryEnabled {
		store, memErr := memory.Open(cfg.MemoryFile)
		if memErr != nil {
			startupNotices = append(startupNotices, "长期记忆加载失败，本次不启用："+memErr.Error())
		} else {
			mem = store
			if n := len(store.Entries()); n > 0 {
				startupNotices = append(startupNotices, fmt.Sprintf("长期记忆已加载 %d 条", n))
			}
		}
	}

	client := llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL: cfg.BaseURL,
		APIKey:  cfg.APIKey,
		Model:   cfg.Model,
		Timeout: seconds(cfg.RequestTimeoutSeconds),
	})

	catalog := skills.NewCatalog(cfg.SkillsDir)
	workspace, err := tools.NewWorkspace(cfg.WorkspaceDir)
	if err != nil {
		return err
	}

	approver := busApprover{bus}
	scope := tools.NewProjectScope(workspace.Root())
	registry, preview := buildRegistry(workspace, catalog, approver, seconds(cfg.ShellTimeoutSeconds), scope)
	defer preview.Close()

	notices, stopMCP := startMCP(ctx, bus, registry, workspace.Root(), approver, cfg.MCPConfig)
	startupNotices = append(startupNotices, notices...)
	if stopMCP != nil {
		defer stopMCP()
	}

	bot := agent.New(agent.Deps{
		LLM:    client,
		Tools:  registry,
		Skills: catalog,
		Bus:    bus,
		Memory: mem,
		Scope:  scope,
		Options: agent.Options{
			MaxSteps:      cfg.MaxSteps,
			WorkspaceRoot: workspace.Root(),
			Timeout:       seconds(cfg.RunTimeoutSeconds),
		},
	})

	model := tui.New(tui.Deps{
		Runner:     bot,
		Bus:        bus,
		Session:    sess,
		Model:      cfg.Model,
		Workspace:  workspace.Root(),
		Skills:     catalog.List,
		RunTimeout: seconds(cfg.RunTimeoutSeconds),
	})

	// 先创建 TUI（已完成订阅），再发送启动期提示，避免事件丢失。
	for _, notice := range startupNotices {
		bus.Publish(events.Event{Kind: events.Notice, Text: notice})
	}

	// 使用 AltScreen：启动后整个终端只展示 Agent 界面，退出时恢复原终端内容。
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	if _, err := program.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func seconds(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// buildRegistry 注册全部本地工具。新增本地能力时只改这里，
// Agent 和 TUI 都不需要知道具体工具类型。
func buildRegistry(
	workspace *tools.Workspace,
	catalog *skills.Catalog,
	approver tools.Approver,
	shellTimeout time.Duration,
	scope *tools.ProjectScope,
) (*tools.Registry, *tools.PreviewSite) {
	registry := tools.NewRegistry()
	registry.Add(tools.NewReadFile(workspace))
	registry.Add(tools.NewWriteFile(workspace, scope))
	registry.Add(tools.NewDeleteFile(workspace, approver, scope))
	registry.Add(tools.NewListFiles(workspace))
	registry.Add(tools.NewWriteTempFile(workspace))
	registry.Add(tools.NewShell(workspace, approver, shellTimeout))
	registry.Add(tools.NewConfirmPlan(approver, scope))
	registry.Add(tools.NewLoadSkill(catalog))

	preview := tools.NewPreviewSite(workspace)
	registry.Add(preview)
	return registry, preview
}

// startMCP 在后台连接 MCP Server，并把发现的工具热注册到 registry。
//
// 设计要点：
//  1. MCP 首次启动可能触发 npx 下载，不能阻塞 TUI
//  2. 单个 Server 失败只发 Notice，本地工具照常可用
//  3. 返回的 stop 会等待连接结束并关闭子进程
func startMCP(
	ctx context.Context,
	bus *events.Bus,
	registry *tools.Registry,
	workspaceRoot string,
	approver tools.Approver,
	configPath string,
) ([]string, func()) {
	configs, err := mcp.LoadConfigs(configPath)
	if err != nil {
		return []string{"MCP 配置读取失败：" + err.Error()}, nil
	}
	if len(configs) == 0 {
		return nil, nil
	}

	manager := mcp.NewManager(workspaceRoot, approver)
	notices := []string{
		fmt.Sprintf("MCP：正在后台连接 %d 个 Server，稍候再提交需要浏览器的任务…", len(configs)),
	}

	// 独立的 MCP 上下文：退出时先取消，让阻塞中的连接/发现尽快返回，
	// 避免 wg.Wait 一直等到 45 秒连接超时。
	mcpCtx, cancelMCP := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, failure := range manager.Start(mcpCtx, configs) {
			bus.Publish(events.Event{Kind: events.Notice, Text: "MCP：" + failure.Error()})
		}
		items := manager.Tools()
		for _, tool := range items {
			registry.Add(tool)
		}
		bus.Publish(events.Event{
			Kind: events.Notice,
			Text: fmt.Sprintf("MCP：连接完成，注册 %d 个工具。", len(items)),
		})
	}()

	return notices, func() {
		cancelMCP()
		wg.Wait()
		manager.Close()
	}
}
