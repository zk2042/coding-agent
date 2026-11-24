package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"coding-agent/internal/llm"
)

// maxPlanRunes 限制方案长度，避免把整段设计文档塞进审批框。
const maxPlanRunes = 4000

// ConfirmPlan 让模型在写文件前把项目、文件清单与方案交给用户确认。
// 用户同意后，ProjectScope 才会允许 write_file / delete_file 操作这些文件。
type ConfirmPlan struct {
	approver Approver
	scope    *ProjectScope
}

func NewConfirmPlan(approver Approver, scope *ProjectScope) *ConfirmPlan {
	return &ConfirmPlan{approver: approver, scope: scope}
}

func (t *ConfirmPlan) Definition() llm.Tool {
	return llm.Tool{
		Name:        NameConfirmPlan,
		Description: "创建或修改文件前提交方案并请求用户确认；必须指定项目目录、模式和文件清单，用户同意后才能写入。修改 workspace 根目录的已有文件（如 README.md）时 project 用 \".\"、mode 用 \"modify\"",
		Parameters: objectSchema(map[string]any{
			"project": stringProperty("项目目录名，单层，例如 game-2048；所有文件都必须放在这个目录下。修改根目录已有文件时用 \".\""),
			"mode":    stringProperty("create 表示新建项目，modify 表示修改已有项目"),
			"files":   stringArrayProperty("workspace 相对文件路径清单，必须全部位于 project/ 下；已存在的文件会作为覆盖项展示给用户"),
			"plan":    stringProperty("给用户看的方案：目标 + 页面/功能结构，尽量控制在 6 行以内"),
		}, "project", "mode", "files", "plan"),
	}
}

func (t *ConfirmPlan) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Project string   `json:"project"`
		Mode    string   `json:"mode"`
		Files   []string `json:"files"`
		Plan    string   `json:"plan"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	plan := strings.TrimSpace(args.Plan)
	if plan == "" {
		return "", fmt.Errorf("方案不能为空")
	}
	if t.scope == nil {
		return "", fmt.Errorf("项目策略未配置，已拒绝执行")
	}
	info, err := t.scope.ValidatePlan(args.Project, args.Mode, args.Files)
	if err != nil {
		return "", err
	}
	if runes := []rune(plan); len(runes) > maxPlanRunes {
		plan = string(runes[:maxPlanRunes]) + "…"
	}
	if t.approver == nil {
		return "", fmt.Errorf("未配置审批器，已拒绝执行")
	}

	allowed, err := t.approver.Approve(ctx, Approval{Title: "执行方案", Detail: formatPlan(info, plan)})
	if err != nil {
		return "", err
	}
	if !allowed {
		return "用户拒绝了方案。请先根据用户意见调整方案或询问需求，不要创建、修改或删除文件。", nil
	}
	t.scope.Commit(info)
	return "用户已确认方案。当前只能写入 " + planProjectLabel(info.Project) + " 下的以下文件：\n- " +
		strings.Join(info.Files, "\n- ") + "\n超出清单的文件改动需要重新调用 confirm_plan。", nil
}

func planProjectLabel(project string) string {
	if project == "." {
		return "workspace 根目录"
	}
	return "项目 " + project
}

func formatPlan(info PlanInfo, plan string) string {
	var b strings.Builder
	modeLabel := "新建"
	if info.Mode == "modify" {
		modeLabel = "修改"
	}
	fmt.Fprintf(&b, "%s（%s）\n", planProjectLabel(info.Project), modeLabel)

	if len(info.Overwrite) > 0 {
		fmt.Fprintf(&b, "将覆盖已有文件（%d）：\n", len(info.Overwrite))
		for _, file := range info.Overwrite {
			b.WriteString("- " + file + "\n")
		}
	} else {
		b.WriteString("覆盖：无（不会改动已有文件）\n")
	}
	if len(info.Create) > 0 {
		fmt.Fprintf(&b, "将新建文件（%d）：\n", len(info.Create))
		for _, file := range info.Create {
			b.WriteString("- " + file + "\n")
		}
	}
	b.WriteString("方案：\n" + plan)
	return strings.TrimSpace(b.String())
}
