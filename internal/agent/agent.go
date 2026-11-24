// Package agent 负责一次任务的模型-工具循环，并把过程通过事件总线广播给 UI。
// 它不直接打印任何内容，因此同一套逻辑可以被 TUI、测试或未来的其他前端复用。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"coding-agent/internal/events"
	"coding-agent/internal/llm"
	"coding-agent/internal/memory"
	"coding-agent/internal/session"
	"coding-agent/internal/skills"
	"coding-agent/internal/tools"
)

// Deps 是 Agent 的全部外部依赖。
// 全部通过接口或小对象注入，测试时可以替换成假实现。
// MemoryStore 是 Agent 使用的长期记忆接口；单用户项目只有一个实现。
type MemoryStore interface {
	Entries() []memory.Entry
	Upsert(entries []memory.Entry) (int, int, error)
}

type Deps struct {
	LLM    llm.Client
	Tools  *tools.Registry
	Skills *skills.Catalog
	Bus    *events.Bus
	Memory MemoryStore
	Scope  *tools.ProjectScope
	Options
}

// Options 控制单次任务的行为，不参与依赖装配。
type Options struct {
	MaxSteps      int
	WorkspaceRoot string
	Timeout       time.Duration
}

// Agent 负责一次任务的模型-工具循环。
type Agent struct {
	deps Deps
}

const (
	defaultMaxSteps = 12
	maxToolOutput   = 30000
)

// New 补齐默认值，不启动任何 goroutine。
func New(deps Deps) *Agent {
	if deps.Options.MaxSteps <= 0 {
		deps.Options.MaxSteps = defaultMaxSteps
	}
	return &Agent{deps: deps}
}

// Run 执行一次完整任务：压缩历史 → 写入用户消息 → 多轮模型调用 → 执行工具。
// 所有输出都通过 Bus 广播，函数本身不打印，也不直接操作界面。
func (a *Agent) Run(ctx context.Context, sess *session.Session, input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	if sess == nil {
		return fmt.Errorf("会话不能为空")
	}
	if a.deps.LLM == nil || a.deps.Tools == nil {
		return a.abort(fmt.Errorf("Agent 依赖未配置完整"))
	}
	// 每个任务重新授权，避免上一个项目的文件清单继续生效。
	a.deps.Scope.BeginTask()
	if a.deps.Options.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.deps.Options.Timeout)
		defer cancel()
	}

	a.emit(events.Event{Kind: events.RunStarted, Text: input})

	// 压缩发生在写入本轮输入之前，避免把当前任务切进摘要。
	a.maybeCompress(ctx, sess)
	a.trimIfNeeded(sess)

	sess.Append(llm.User(input))

	loadedSkills := make(map[string]bool)
	previousKey := ""
	repeats := 0

	for step := 0; step < a.deps.Options.MaxSteps; step++ {
		round := step + 1
		if err := ctx.Err(); err != nil {
			return a.abort(err)
		}

		// 单轮长任务可能没有可切分的 user 消息，每轮调用前都检查一次预算。
		a.shrinkToolOutputsIfNeeded(sess)
		a.trimIfNeeded(sess)

		resp, err := a.deps.LLM.Complete(ctx, llm.Request{
			Items: a.inputItems(sess),
			Tools: a.deps.Tools.Definitions(),
		})
		if err != nil {
			return a.abort(err)
		}

		// 非流式：整轮返回后先把思考过程交给界面，再执行工具。
		if strings.TrimSpace(resp.Reasoning) != "" {
			a.emit(events.Event{Kind: events.Reasoning, Round: round, Text: resp.Reasoning})
		}

		a.recordAssistant(sess, resp)

		// 一轮可能同时包含说明文字和工具调用，先把文字交给界面。
		if strings.TrimSpace(resp.Content) != "" {
			a.emit(events.Event{Kind: events.AssistantDone, Round: round, Text: resp.Content})
		}

		if len(resp.ToolCalls) == 0 {
			if strings.TrimSpace(resp.Content) == "" {
				return a.abort(fmt.Errorf("模型返回空响应，请重试"))
			}
			// 抽取可能再调用一次模型；放在 RunFinished 之前，
			// 避免 TUI 提前解除 busy 后与下一次 Run 的 runDoneMsg 竞态。
			a.rememberFacts(ctx, input, resp.Content)
			a.emit(events.Event{Kind: events.RunFinished, Text: resp.Content})
			return nil
		}

		key := canonicalCalls(resp.ToolCalls)
		if key == previousKey {
			repeats++
		} else {
			previousKey = key
			repeats = 0
		}
		if repeats >= 2 {
			return a.abort(fmt.Errorf("任务未完成：模型连续三轮请求相同操作，已停止重复执行"))
		}

		for _, call := range resp.ToolCalls {
			if err := ctx.Err(); err != nil {
				return a.abort(err)
			}
			a.emit(events.Event{Kind: events.ToolStarted, Round: round, Tool: call.Name, Args: call.Arguments})

			result, callErr := a.executeTool(ctx, call, repeats > 0, loadedSkills)
			if callErr != nil {
				result = "工具执行失败：" + callErr.Error() + "\n" + result
			}
			result = truncate(result, maxToolOutput)
			a.emit(events.Event{Kind: events.ToolFinished, Round: round, Tool: call.Name, Result: result})

			sess.Append(llm.FunctionOutput(call.ID, result))
		}
	}

	return a.abort(fmt.Errorf("达到最大工具调用轮数（%d），请缩小任务范围或提高 AGENT_MAX_STEPS", a.deps.Options.MaxSteps))
}

// recordAssistant 把模型这一轮的文本与工具调用写回会话。
// reasoning item 只用于界面展示，不回传给模型。
func (a *Agent) recordAssistant(sess *session.Session, resp *llm.Response) {
	items := make([]llm.Item, 0, 1+len(resp.ToolCalls))
	if strings.TrimSpace(resp.Content) != "" {
		items = append(items, llm.Assistant(resp.Content))
	}
	for _, call := range resp.ToolCalls {
		items = append(items, llm.FunctionCall(call))
	}
	if len(items) == 0 {
		return
	}
	sess.Append(items...)
}

// executeTool 负责具体的工具调度，并处理两个横切规则：
// 上一轮重复的调用不重复执行；同一个 Skill 在一次任务里只加载一次。
func (a *Agent) executeTool(ctx context.Context, call llm.ToolCall, repeated bool, loaded map[string]bool) (string, error) {
	if repeated {
		return "本轮操作与上一轮相同，未再次执行；请根据已有结果调整方案或总结。", nil
	}
	if call.Name == tools.NameLoadSkill {
		var args struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(call.Arguments), &args) == nil && args.Name != "" {
			if loaded[args.Name] {
				return "该 Skill 已在本任务加载，请使用前面的工具结果。", nil
			}
			out, err := a.deps.Tools.Execute(ctx, call.Name, json.RawMessage(call.Arguments))
			if err == nil {
				loaded[args.Name] = true
			}
			return out, err
		}
	}
	return a.deps.Tools.Execute(ctx, call.Name, json.RawMessage(call.Arguments))
}

// inputItems 组装本次请求：系统提示 + 长期记忆 + 历史摘要 + 最近窗口 items。
func (a *Agent) inputItems(sess *session.Session) []llm.Item {
	items := make([]llm.Item, 0, len(sess.Items)+3)
	items = append(items, llm.System(a.systemPrompt(sess)))
	if block := a.memoryBlock(); block != "" {
		items = append(items, llm.System(block))
	}
	if summary := strings.TrimSpace(sess.Summary); summary != "" {
		items = append(items, llm.System(
			"[以下是更早对话的压缩摘要，供你参考；不要向用户复述这段摘要]\n"+summary,
		))
	}
	items = append(items, sess.Items...)
	return items
}

// systemPrompt 只放稳定规则和索引，具体 Skill 正文通过 load_skill 按需注入。
func (a *Agent) systemPrompt(sess *session.Session) string {
	var b strings.Builder
	b.WriteString(`你是一个谨慎、专业的 Go Coding Agent。你可以分析需求、规划任务、读写 workspace 文件、运行 Shell、加载 Skill、调用 MCP 工具。

规则：
1. 先理解目标。只读任务可以直接开始；需要创建或修改文件时，先调用 confirm_plan 提交方案，参数必须包含 project（项目目录名）、mode（create 或 modify）、files（文件清单）和 plan，用户同意后再写文件。
2. 读取、列目录、加载 Skill、预览，以及写入 .agent-tmp 下的一次性检查脚本，不需要提交方案。读取文件用 read_file：它一次返回完整内容（≤30KB）；若结果提示“已读取第 a-b 行”，用 offset 继续读取即可，不要用 run_shell 写脚本分段读文件。
3. 删除文件也必须先通过 confirm_plan 列入文件清单，再调用 delete_file（它会再次请求用户审批）；不要用 run_shell 执行删除命令；delete_file 不能删除目录。
4. 所有项目文件只能写入 workspace/<当前项目>/；新项目必须创建新的独立目录；不要写入或修改其他项目目录（例如 portfolio），除非用户明确要求修改该项目。项目文件优先用 write_file 写入，不要用 run_shell 绕过项目范围限制。
5. 执行 Shell 或需要审批的 MCP 工具会请求用户审批；被拒绝时不要绕过，改为询问用户或调整方案。
6. 生成代码后尽可能运行检查，并根据错误修复；不要声称执行过实际未执行的命令。
7. 对不确定的依赖或 API 先检查，不要编造。
8. Skill 是操作指南：先看索引，需要时调用 load_skill 获取正文，并按正文执行。
9. 静态网页生成或较大改动后，调用 preview_site 启动本地预览。如果当前注册了 browser_* 工具，用它打开并验证快照、控制台和关键交互，发现问题就修改后重新验证；如果没有注册浏览器工具，明确告诉用户“浏览器验证未执行”，不得声称已验证。纯文字或样式微调不必每次重新验证。
10. 任务完成时，默认用中文总结创建/修改的文件、运行与验证结果、未完成事项；用户明确要求其他语言时遵从。
11. 不确定是新项目还是修改已有项目时，先询问用户，不要猜。
12. 修改 workspace 根目录的已有文件（如 README.md）时，confirm_plan 的 project 用 "."、mode 用 "modify"；根目录只允许修改已有直接文件，不能用来新建项目文件。`)

	fmt.Fprintf(&b, "\n\n工作区（唯一可写目录）：%s", a.deps.Options.WorkspaceRoot)
	fmt.Fprintf(&b, "\n执行环境：%s", tools.ShellGuide())

	if a.deps.Skills != nil {
		if list := a.deps.Skills.List(); len(list) > 0 {
			b.WriteString("\n\n可用 Skill（仅索引，按需加载正文）：\n")
			for _, skill := range list {
				fmt.Fprintf(&b, "- %s：%s\n", skill.Name, skill.Description)
			}
		}
	}
	return b.String()
}

func (a *Agent) abort(err error) error {
	a.emit(events.Event{Kind: events.Failed, Err: err.Error()})
	a.emit(events.Event{Kind: events.RunFinished})
	return err
}

func (a *Agent) emit(ev events.Event) {
	if a.deps.Bus == nil {
		return
	}
	a.deps.Bus.Publish(ev)
}

func (a *Agent) activeProject() string {
	if a.deps.Scope == nil {
		return ""
	}
	return a.deps.Scope.ActiveProject()
}

func canonicalCalls(calls []llm.ToolCall) string {
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		args := call.Arguments
		var object map[string]any
		if json.Unmarshal([]byte(args), &object) == nil {
			if normalized, err := json.Marshal(object); err == nil {
				args = string(normalized)
			}
		}
		parts = append(parts, call.Name+"("+args+")")
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "\n[工具输出截断]"
}
