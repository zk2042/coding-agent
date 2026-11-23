# Go Coding Agent

一个 Go 编写的终端 Coding Agent：BubbleTea 全屏 TUI、OpenAI Responses API 非流式思考与工具过程、Markdown 渲染、上下文自动压缩、单用户长期记忆、Skill 按需加载、MCP 工具接入，以及写文件/删除/Shell 的人机协作审批。

模型接入使用官方 `github.com/openai/openai-go/v3`，通过 OpenAI 兼容的 Responses API 指向 SCNet Token Plan；上下文完全由本地维护（`Store=false`）。

## 1. 能力概览

| 能力 | 说明 |
| --- | --- |
| 会话与压缩 | 滚动摘要 + 最近窗口 items 只存在内存；按条数、字符预算和工具输出体积自动压缩，进程退出即结束 |
| 长期记忆 | 跨任务保存稳定事实、偏好、项目约定与决策到 `data/memory.json`；不保存会话流水 |
| 人机协作 | 写文件前用 `confirm_plan` 确认方案与文件清单；删除只能走 `delete_file` 并逐次审批；Shell、MCP 保持各自审批 |
| MCP | 官方 MCP Go SDK（stdio），TUI 启动后后台连接并热注册工具，默认需要审批、可按 Server 关闭 |
| Shell | `run_shell` 在工作区执行，界面底部行内提示按 `y/n` 审批，带超时与输出上限 |
| Skill | 解析 `SKILL.md` frontmatter；系统提示只放索引，正文由 `load_skill` 按需加载 |
| 网页生成与展示 | `build-web-page` 负责流程与验收，`frontend-design` 负责视觉方向；`preview_site` 启动本地静态服务 |
| 浏览器自检 | Playwright MCP 打开预览地址，读快照、控制台并点击验证；发现不符即改代码再复验 |
| 交互 | 按轮展示思考与回答、工具调用进度、Markdown 定稿渲染、`/new` 新建会话 |

## 2. 环境要求

- Go 1.26+
- SCNet Token Plan 专属 API Key
- 可选：Node.js 18+ / npx（运行 MCP Server）
- 可选：Chrome（Playwright 用 `--browser chrome` 驱动本机 Chrome，不额外下载 Chromium）
- 交互式终端（BubbleTea 需要 TTY；不要重定向标准输入输出）

## 3. 配置

```bash
cp .env.example .env
```

程序从当前工作目录向上查找 `go.mod`（其次 `.env`）定位项目根，所有相对路径（`workspace`、`skills`、`configs`、`data`）都基于项目根解析，因此从项目根或 `cmd/` 等子目录启动都可以。

至少填写：

```env
SCNET_BASE_URL=https://api.scnet.cn/api/llm/v1
SCNET_API_KEY=你的TokenPlan专属Key
SCNET_MODEL=DeepSeek-V4.1-Flash-1017-Event
```

常用可选项：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `AGENT_MAX_STEPS` | 12 | 单次任务最多模型轮数 |
| `AGENT_TIMEOUT_SECONDS` | 900 | 单次任务总时限 |
| `MODEL_REQUEST_TIMEOUT_SECONDS` | 120 | 单次模型请求时限 |
| `SHELL_TIMEOUT_SECONDS` | 60 | Shell 命令执行时限 |
| `WORKSPACE_DIR` | `./workspace` | 唯一允许写文件的目录 |
| `SKILLS_DIR` | `./skills` | Skill 根目录 |
| `MCP_CONFIG` | `./configs/mcp.json` | MCP Server 配置 |
| `MEMORY_ENABLED` | `true` | 是否启用长期记忆 |
| `MEMORY_FILE` | `./data/memory.json` | 长期记忆文件 |

环境变量优先于 `.env`，空值视为未配置。不要把 `.env` 提交到版本库。

## 4. 编译与运行

```powershell
go test -count=1 ./...
go vet ./...
go build -o coding-agent.exe ./cmd/agent
.\coding-agent.exe
```

也可以直接 `go run ./cmd/agent`。每次启动都是全新会话；进程退出后上下文不保留，运行中可用 `/new` 重置。长期记忆从 `data/memory.json` 加载，与会话上下文分离。

## 5. TUI 操作

| 按键 / 命令 | 作用 |
| --- | --- |
| `Enter` | 发送需求 |
| `Alt+Enter` / `Ctrl+J` | 输入换行 |
| `PgUp` / `PgDn`、`Ctrl+U` / `Ctrl+D` | 滚动对话（半页） |
| 鼠标滚轮 | 滚动对话 |
| `Ctrl+C` | 任务中取消；空闲时退出 |
| `y` / `n` | 审批：允许 / 拒绝（方案、删除、Shell 共用底部行内提示） |
| `/help` | 命令帮助 |
| `/new` | 新建会话（清空上下文） |
| `/skills` | 查看 Skill 索引 |
| `/clear` | 清空当前界面 |
| `/exit` | 退出 |

界面启动后整个终端只展示 Agent：最外层是固定宽度的圆角外框，头部标题和底部输入区固定，中间对话区滚动。任务运行时底部切换为 spinner + 当前状态；思考与工具调用按轮次平铺展示（`──── 第 N 轮思考 ────` → `工具名/参数` → `结果`），不使用嵌套卡片。向上翻阅时不会被新内容拉回底部，滚回底部后自动恢复跟随。

请求采用非流式：每轮模型返回后，先展示整段思考，再展示工具调用与结果，最后用 glamour 渲染 Markdown 回答。

## 6. 演示流程

### 演示 A：生成网页并验证

> 做一个个人作品集网页，包含介绍、技能、项目经历和联系方式。深色简约风格，适配手机。先加载网页开发 Skill，然后创建文件并用 preview_site 展示，最后用浏览器工具检查效果。

预期：

1. 模型按需加载 `build-web-page`，需要视觉方向时再加载 `frontend-design`。
2. 列出页面结构、文件清单和视觉方向，调用 `confirm_plan`，按 `y` 后才写入。
3. 在 `workspace/portfolio/` 生成 `index.html`、`style.css`、`script.js`。
4. 调用 `preview_site` 启动本地服务。
5. 若 Playwright 已注册，用它打开页面、读快照、查控制台、点击关键交互；发现问题则修改后复验。
6. 最后说明访问地址、实际验证结果和遗留问题。

### 演示 B：方案确认

> 把 README 里的 TUI 操作表补一行，说明 Ctrl+C 的行为。

模型先调用 `confirm_plan`，对话区显示目标和文件清单；按 `n` 拒绝后模型只讨论方案，不写文件；按 `y` 后才执行修改。

### 演示 C：删除审批

> 删除 workspace 里已经没用的草稿文件。

模型调用 `delete_file`，审批框显示“删除文件 + 路径”；拒绝后文件保留，允许后才删除。目录、绝对路径和 `..` 会被拒绝。

### 演示 D：Shell 审批

> 列出刚才生成的项目目录，并检查 HTML 是否引用了 style.css。

`run_shell` 弹出审批，按 `n` 拒绝、`y` 执行；拒绝结果会作为工具结果返回模型。

### 演示 E：MCP（Playwright / filesystem）

`configs/mcp.json` 默认启用两个 Server：

| Server | 用途 | 审批 |
| --- | --- | --- |
| `playwright` | 固定 `0.0.86`，`--browser chrome`：用本机 Chrome 有头打开页面，导航和点击可见 | 关闭（只读浏览器操作） |
| `filesystem` | 官方参考实现，默认关闭；需要标准 MCP 文件能力时把 `enabled` 改成 `true` | 需要 |

- MCP 在 TUI 启动后后台连接，`npx` 首次下载不会卡住界面。
- 引导模型：`browser_navigate` 打开 URL，`browser_snapshot` 看结构，`browser_console_messages` 查报错，`browser_click` 测交互。
- 截图只作为证据占位，不会把 base64 塞进模型上下文。

## 7. 目录结构

```text
coding-agent/
├── cmd/agent/main.go            # 组合根：配置、对象组装、启动 TUI
├── internal/
│   ├── agent/                   # 工具循环、事件发布、压缩、长期记忆、重复调用保护
│   ├── config/                  # .env 与环境变量
│   ├── events/                  # 事件总线 + 审批通道
│   ├── llm/                     # 领域类型、openai-go/v3 适配器
│   ├── mcp/                     # MCP 配置、连接管理、工具适配
│   ├── memory/                  # 单用户长期记忆（结构化 JSON，不存会话流水）
│   ├── session/                 # 内存中的滚动摘要与最近窗口 items
│   ├── skills/                  # frontmatter 解析、索引、按需加载
│   ├── tools/                   # 文件、删除、Shell、方案确认、Skill、预览
│   └── tui/                     # BubbleTea Model/Update/View/Markdown
├── skills/build-web-page/       # 网页生成流程与验收
├── skills/frontend-design/      # 视觉方向（改编自 Anthropic 官方示例，附 LICENSE）
├── configs/mcp.json             # MCP Server 配置
├── data/memory.json             # 长期记忆（稳定事实与偏好）
└── workspace/                   # 唯一允许写文件的目录
```

依赖方向：

```text
llm / events / session / memory / skills
  → tools → mcp → agent → tui → cmd
```

- `llm`、`events` 不依赖业务包。
- `tools` 通过 `NoteWriter`、`SkillSource`、`Approver` 等小接口解耦。
- 审批由组合根把 `events.Bus` 适配成 `tools.Approver`，两侧互不知晓。
- TUI 通过 `Runner` 接口调用 Agent，测试时注入假实现。

## 8. 上下文压缩

模型上下文固定为四段：

```text
系统提示（全局规则 + Skill 索引）
+ 长期记忆（稳定事实与偏好，≤2000 字）
+ 滚动摘要（更早对话，≤600 字）
+ 最近窗口 items（原始对话与工具调用）
```

- 触发：最近窗口超过 40 条，或文本/参数/结果合计超过 20000 字符。
- 摘要压缩：保留最近 5 轮用户对话，更早部分交给模型合并进摘要；只在 user 消息处切分，保证 `function_call` 与 `function_call_output` 不被拆开。
- 失败：摘要调用失败时保留原文，下一轮再试，不会先删后总结。
- 单轮长任务：没有足够 user 消息可切时，把较旧工具输出替换为占位文本；最近 4 条工具输出保留，最新一条超过 4000 字会截断。调用与结果的结构不变。
- 硬上限：仍超过 80 条时，多轮会话按最旧 user 轮次丢弃；单轮任务按完整工具调用/结果对丢弃，不会留下孤立输出。
- 执行时机：每次 `Run` 开始时摘要压缩；每轮模型调用前再检查字符预算并降级旧工具输出。
- 不落盘：摘要与窗口只存在进程内存，进程退出或 `/new` 后即丢弃。

## 9. 长期记忆

- 定位：保存跨任务仍然成立的稳定事实、偏好、项目约定与已确认决策；不保存会话流水，不记录对话原文。
- 文件：`data/memory.json`，结构化 JSON 数组，可直接查看和手工编辑。
- 写入：每次 `Run` 正常结束后、`RunFinished` 之前调用一次模型抽取；只以用户输入为事实来源，助手回答仅用于理解上下文。抽取有 10 秒超时，失败不影响主任务。
- 过滤：置信度低于 0.6、空文本、疑似密钥/Token/密码的条目不写入；单次最多 8 条。
- 去重：相同 `key` 用新内容覆盖；不同 `key` 但规范化文本相同的条目也会去重。
- 读取：每次模型请求前注入最多 20 条、2000 字以内的记忆；偏好、约定、决策优先于普通事实，同类型再按更新时间取舍。
- 安全：记忆块声明“不是本轮指令”，与当前用户要求冲突时以当前要求为准。
- 关闭：`MEMORY_ENABLED=false`。

## 10. 人机协作与审批

| 操作 | 机制 | 是否强制 |
| --- | --- | --- |
| 创建/修改文件 | 先调用 `confirm_plan` 提交目标和文件清单，用户按 `y` 后执行 | 提示词约束 |
| 删除文件 | 只能调用 `delete_file`，每次删除单独审批；仅限 workspace 内的单个文件 | 工具强制 |
| Shell | 每次 `run_shell` 前审批；可在配置中保留审批 | 工具强制 |
| MCP | 按 Server 配置，缺省需要审批；`"approval_required": false` 才跳过 | 工具强制 |

方案确认是软约束：靠系统提示要求模型先调用；删除是硬约束：没有其他入口，且不允许用 Shell 代替删除。所有审批都是人工闸门，不是安全沙箱。

## 11. Skill

- 目录：`skills/<name>/SKILL.md`，frontmatter 支持 `name`、`description` 及 `>` / `|` 多行描述。
- 渐进式披露：系统提示只注入 `- 名称：描述` 的索引；正文只在模型调用 `load_skill` 时注入。
- 当前内置两个 Skill：
  - `build-web-page`：网页生成流程、方案确认、预览、浏览器自检与验收。
  - `frontend-design`：视觉方向、配色、字体、版式与避免模板化；改编自 Anthropic 官方示例并译为中文。
- 两者通过 description 分工，模型按任务自动选择；Skill 正文变化需要重启进程才会重新索引。

## 12. MCP

- 使用官方 `CommandTransport` 启动本地 stdio 子进程，自动处理握手与 `tools/list` 分页；每个子进程会收到 `AGENT_WORKSPACE` 环境变量。
- 启动顺序：TUI 先启动，MCP 后台连接；连接完成后把工具热注册进注册表，下一次 Agent 运行即可使用。
- 单个 Server 失败只提示并继续其他 Server，不影响本地工具；当前没有自动重连，恢复后需重启程序。
- 每次 MCP 工具调用有 45 秒超时；出错时返回错误和该 Server 的 stderr 尾部。
- 目前只支持本地 stdio，不包含远程 HTTP、OAuth 与通知订阅。

## 13. 失败处理与重试

| 场景 | 行为 |
| --- | --- |
| 模型瞬时错误 | SDK 自动重试 2 次（408、409、429、5xx、网络错误） |
| 401 / 403 / 404 / 400 | 立即失败，不重试；常见于 Key 无效、模型 ID 错误 |
| 模型返回空响应 | 按失败处理，提示“模型返回空响应，请重试”，不会假装成功 |
| 单次请求超时 | 受 `MODEL_REQUEST_TIMEOUT_SECONDS` 限制 |
| 任务总超时 | 受 `AGENT_TIMEOUT_SECONDS` 限制，中止并提示 |
| 工具报错 | 错误作为工具结果回传模型，任务继续 |
| 摘要失败 | 保留原文并提示，下一轮再试 |
| 记忆抽取失败 | 10 秒超时，只提示，不影响主任务 |
| MCP 连接失败 | 后台提示，不影响本地工具；正常退出时先取消连接再等待，避免卡住 |
| 达到最大轮数 | 中止并提示调整 `AGENT_MAX_STEPS` |

没有备用模型或自动降级；非致命失败都会回传模型或提示用户，不会静默失败。

## 14. 安全边界与限制

1. Shell 不是安全沙箱。审批只是人工闸门，命令仍可能访问工作区之外；不要在不可信目录运行。
2. workspace 工具拒绝绝对路径、`..` 与符号链接组件；Shell 不受此限制。
3. `delete_file` 只允许删除 workspace 内的单个文件，不支持目录和通配符。
4. MCP 仅支持本地 stdio；Playwright 会真实驱动本机浏览器。
5. 压缩依赖模型生成摘要；摘要失败时保留原文并重试，极端情况下按完整轮次或工具对丢弃最旧内容。
6. 预览服务器只监听 `127.0.0.1`，进程退出时统一关闭。
7. 模型 ID、工具调用支持和额度策略以 SCNet 控制台为准。
8. 浏览器自检的结论以工具返回的快照/截图/控制台为准；模型不得凭空声称通过。
9. 长期记忆由模型抽取，可能漏记或误记；文件可直接编辑。敏感信息有简单规则过滤，但不能保证完全识别，不要主动让 Agent 记录密钥。
10. `confirm_plan` 是提示词约束，模型仍可能跳过；删除与审批是工具级约束。

## 15. 测试

```powershell
gofmt -l cmd internal
go test -count=1 ./...
go vet ./...
```

覆盖的包：`agent`、`config`、`events`、`llm`、`mcp`、`memory`、`session`、`skills`、`tools`、`tui`。重点覆盖压缩与单轮长任务裁剪、长期记忆去重与注入优先级、方案/删除/Shell 审批、MCP 参数序列化、TUI 宽度与超长行截断。

真实模型、Playwright 浏览器和 MCP 多服务联调需要本地环境，单元测试不覆盖。

## 16. 常见问题

- **401 / Unauthorized**：确认使用 Token Plan 专属 Key，且完整复制。
- **404**：Base URL 应为 `https://api.scnet.cn/api/llm/v1`，不要追加 `/chat/completions` 或 `/responses`；SDK 会自动拼接。
- **模型不存在**：把 `SCNET_MODEL` 改成控制台当前支持的精确 ID。
- **模型不调用工具**：确认模型支持 OpenAI 兼容工具调用。
- **模型返回空响应**：可直接重试；连续出现时检查模型 ID 与额度。
- **MCP 启动失败**：检查 Node.js/npx 与网络；也可先关闭对应 Server。
- **Playwright 不需要 API Key**：它只通过 `npx` 驱动本机 Chrome，模型才需要 SCNet Key。
- **Playwright 报 `expected record, received string`**：旧版本把工具参数序列化成了字符串，已修复；重新编译即可。
- **Playwright 报 Chrome 不存在**：确认本机安装了 Chrome；也可把 `--browser chrome` 改成 `msedge`，或改用 `chromium` 并执行 `npx playwright install chromium`。
- **Playwright 报 profile 目录无权限**：默认会创建 `%LOCALAPPDATA%\ms-playwright-mcp`；删除该目录后重试，或在参数中加入 `--isolated` 使用临时 profile。
- **MCP 工具迟迟不出现**：`npx` 首次下载或网络慢，稍等看对话区提示；失败信息也会显示在对话区。
- **TUI 显示异常**：使用现代终端并保持窗口足够宽；不要通过管道运行。
