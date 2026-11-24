package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"coding-agent/internal/events"
	"coding-agent/internal/llm"
	"coding-agent/internal/session"
)

// 上下文压缩策略：
//   - 条数或字符预算任一超限就触发；
//   - 保留最近 keepRecentRounds 轮用户对话，更早的合并进滚动摘要；
//   - 摘要失败时保留原文，不丢数据；
//   - 仍超过硬上限时按整轮丢弃，作为最后兜底。
const (
	compressMinItems   = 40
	compressCharBudget = 60000
	keepRecentRounds   = 5
	// 减少单轮任务中途丢弃历史工具结果的次数。
	maxContextItems      = 120
	trimKeepRounds       = 3
	maxSummaryRunes      = 600
	maxSummaryInputRunes = 12000
	itemRenderLimit      = 600

	// 单轮长任务没有足够的 user 消息可切，改用工具输出降级来兜底。
	// keepRecentToolOutputs 保留最近几条工具输出；retainedToolOutputs 必须不小于
	// 单条工具输出上限（read_file 30KB ≈ 30000 字符），否则刚读进来的完整文件会在
	// 下一轮被截成前几千字，模型误以为文件被截断，于是反复分段重读、耗尽轮数。
	keepRecentToolOutputs = 4
	retainedToolOutputs   = 30000
	toolOutputPlaceholder = "[历史工具输出已省略，原始内容未保留]"
	toolOutputTruncated   = "\n[工具输出已截断]"
)

const summaryInstruction = `你负责维护一段对话的滚动摘要。
请把"新增对话"合并进"已有摘要"，输出更新后的摘要正文。
要求：
1. 只依据"已有摘要"和"新增对话"，不要补充未出现的信息，也不要编造执行结果。
2. 保留：用户目标、已完成事项、关键文件路径、命令与结果、用户偏好、失败原因、已排除的方案、未完成事项。
3. 忽略工具调用的原始 JSON 和冗长输出，只保留结论。
4. 使用中文，不超过 600 字。
5. 只输出摘要正文，不要标题、不要解释、不要 Markdown 标记。`

// maybeCompress 在每次任务开始、写入新输入之前调用。
func (a *Agent) maybeCompress(ctx context.Context, sess *session.Session) {
	if sess == nil || len(sess.Items) == 0 {
		return
	}
	if len(sess.Items) <= compressMinItems && itemChars(sess.Items) <= compressCharBudget {
		return
	}
	cut := findCutIndex(sess.Items, keepRecentRounds)
	if cut > 0 {
		older := sess.Items[:cut]
		a.emit(events.Event{Kind: events.Notice, Text: fmt.Sprintf("历史较长，正在压缩 %d 条消息…", len(older))})

		summary, err := a.summarize(ctx, sess.Summary, older)
		if err != nil {
			// 压缩失败不能先删原文，下一轮再试。
			a.emit(events.Event{Kind: events.Notice, Text: "历史压缩失败，本轮保留原文：" + err.Error()})
			return
		}

		sess.Summary = summary
		sess.Items = append([]llm.Item(nil), sess.Items[cut:]...)
		a.emit(events.Event{
			Kind: events.Notice,
			Text: fmt.Sprintf("历史已压缩：%d 条 → 摘要 + 最近 %d 条", len(older), len(sess.Items)),
		})
	}

	// 单轮任务找不到 user 切分点时，仍然要控制字符预算：
	// 把较旧的工具输出原地替换为占位文本，保留 call/output 配对。
	if itemChars(sess.Items) > compressCharBudget {
		if changed := a.shrinkToolOutputs(sess); changed > 0 {
			a.emit(events.Event{
				Kind: events.Notice,
				Text: fmt.Sprintf("历史工具输出较长，已省略 %d 条旧结果（保留调用结构）", changed),
			})
		}
	}
}

// shrinkToolOutputsIfNeeded 在每轮模型调用前兜底，避免单轮任务中途撑爆上下文。
func (a *Agent) shrinkToolOutputsIfNeeded(sess *session.Session) {
	if sess == nil || itemChars(sess.Items) <= compressCharBudget {
		return
	}
	a.shrinkToolOutputs(sess)
}

// shrinkToolOutputs 保留最近几条工具输出并限制单条长度，较旧的替换为占位文本。
func (a *Agent) shrinkToolOutputs(sess *session.Session) int {
	if sess == nil {
		return 0
	}
	seen, changed := 0, 0
	for i := len(sess.Items) - 1; i >= 0; i-- {
		item := &sess.Items[i]
		if item.Type != llm.ItemFunctionCallOutput {
			continue
		}
		seen++
		if seen > keepRecentToolOutputs {
			if item.Output == toolOutputPlaceholder {
				continue
			}
			item.Output = toolOutputPlaceholder
			changed++
			continue
		}
		if len([]rune(item.Output)) <= retainedToolOutputs || strings.HasSuffix(item.Output, toolOutputTruncated) {
			continue
		}
		item.Output = truncateRunes(item.Output, retainedToolOutputs) + toolOutputTruncated
		changed++
	}
	return changed
}

// trimIfNeeded 是压缩连续失败或单轮长任务时的硬上限兜底。
// 单轮任务没有 user 切分点：先把要丢弃的最旧工具对摘要进滚动摘要，
// 摘要成功再删；只有摘要失败时才直接丢弃。
func (a *Agent) trimIfNeeded(ctx context.Context, sess *session.Session) {
	if sess == nil || len(sess.Items) <= maxContextItems {
		return
	}
	cut := findCutIndex(sess.Items, trimKeepRounds)
	if cut > 0 {
		dropped := cut
		sess.Items = append([]llm.Item(nil), sess.Items[cut:]...)
		a.emit(events.Event{
			Kind: events.Notice,
			Text: fmt.Sprintf("上下文超过上限，已丢弃最旧的 %d 条（未摘要）", dropped),
		})
		return
	}

	// 单轮任务没有 user 切分点：按完整工具调用/结果对挑出最旧的内容。
	kept, removed := dropOldestToolPairs(sess.Items, maxContextItems)
	if len(removed) == 0 {
		return
	}

	summary, err := a.summarize(ctx, sess.Summary, removed)
	if err == nil && strings.TrimSpace(summary) != "" {
		sess.Summary = summary
		sess.Items = kept
		a.emit(events.Event{
			Kind: events.Notice,
			Text: fmt.Sprintf("上下文超过上限，已将最旧的 %d 条工具调用与结果压缩进摘要", len(removed)),
		})
		return
	}

	sess.Items = kept
	a.emit(events.Event{
		Kind: events.Notice,
		Text: fmt.Sprintf("上下文超过上限，已丢弃最旧的 %d 条工具调用与结果（未摘要）", len(removed)),
	})
}

// dropOldestToolPairs 从最旧处成对移除 function_call 与 function_call_output，
// 直到条数回到上限；不会留下没有调用的输出。removed 按原顺序返回，用于摘要。
func dropOldestToolPairs(items []llm.Item, limit int) (kept, removed []llm.Item) {
	if len(items) <= limit {
		return items, nil
	}
	outputAt := make(map[string]int)
	for i, item := range items {
		if item.Type == llm.ItemFunctionCallOutput {
			outputAt[item.CallID] = i
		}
	}

	drop := make(map[int]bool)
	for i, item := range items {
		if len(items)-len(drop) <= limit {
			break
		}
		if item.Type != llm.ItemFunctionCall {
			continue
		}
		out, ok := outputAt[item.CallID]
		if !ok || out <= i {
			continue
		}
		drop[i] = true
		drop[out] = true
	}
	if len(drop) == 0 {
		return items, nil
	}

	kept = make([]llm.Item, 0, len(items)-len(drop))
	removed = make([]llm.Item, 0, len(drop))
	for i, item := range items {
		if drop[i] {
			removed = append(removed, item)
			continue
		}
		kept = append(kept, item)
	}
	return kept, removed
}

func (a *Agent) summarize(ctx context.Context, previous string, items []llm.Item) (string, error) {
	previous = strings.TrimSpace(previous)
	if previous == "" {
		previous = "（无）"
	}
	resp, err := a.deps.LLM.Complete(ctx, llm.Request{Items: []llm.Item{
		llm.System(summaryInstruction),
		llm.User("已有摘要：\n" + previous + "\n\n新增对话：\n" + renderItems(items)),
	}})
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		return "", errors.New("模型返回空摘要")
	}
	return truncateRunes(summary, maxSummaryRunes), nil
}

// findCutIndex 返回"保留最近 keep 轮"的切分点。
// 只在 user 消息处切，保证 function_call 与 function_call_output 不会被拆开。
func findCutIndex(items []llm.Item, keep int) int {
	users := 0
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Type == llm.ItemMessage && items[i].Role == llm.RoleUser {
			users++
			if users == keep {
				return i
			}
		}
	}
	return 0
}

func itemChars(items []llm.Item) int {
	total := 0
	for _, item := range items {
		total += len([]rune(item.Text)) + len([]rune(item.Arguments)) + len([]rune(item.Output))
	}
	return total
}

func renderItems(items []llm.Item) string {
	var builder strings.Builder
	for _, item := range items {
		var line string
		switch item.Type {
		case llm.ItemMessage:
			switch item.Role {
			case llm.RoleUser:
				line = "用户：" + item.Text
			case llm.RoleAssistant:
				line = "助手：" + item.Text
			default:
				line = "系统：" + item.Text
			}
		case llm.ItemFunctionCall:
			line = "工具调用 " + item.Name + " 参数 " + item.Arguments
		case llm.ItemFunctionCallOutput:
			line = "工具结果 " + item.CallID + "：" + item.Output
		default:
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		builder.WriteString(truncateRunes(line, itemRenderLimit))
		builder.WriteString("\n")
		if len([]rune(builder.String())) > maxSummaryInputRunes {
			break
		}
	}
	return builder.String()
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
