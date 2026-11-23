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

// ConfirmPlan 让模型在写文件前把方案与文件清单交给用户确认。
// 它不修改任何文件，只通过 Approver 请求一次用户决策。
type ConfirmPlan struct{ approver Approver }

func NewConfirmPlan(approver Approver) *ConfirmPlan {
	return &ConfirmPlan{approver: approver}
}

func (t *ConfirmPlan) Definition() llm.Tool {
	return llm.Tool{
		Name:        NameConfirmPlan,
		Description: "创建或修改 workspace 文件前提交方案并请求用户确认；方案需包含目标和将创建/修改的文件清单，用户同意后才能写入",
		Parameters: objectSchema(map[string]any{
			"plan": stringProperty("给用户看的方案：目标 + 将创建/修改的文件清单，尽量控制在 6 行以内"),
		}, "plan"),
	}
}

func (t *ConfirmPlan) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	args, err := decodeArgs(raw)
	if err != nil {
		return "", err
	}
	plan := strings.TrimSpace(args["plan"])
	if plan == "" {
		return "", fmt.Errorf("方案不能为空")
	}
	if runes := []rune(plan); len(runes) > maxPlanRunes {
		plan = string(runes[:maxPlanRunes]) + "…"
	}
	if t.approver == nil {
		return "", fmt.Errorf("未配置审批器，已拒绝执行")
	}

	allowed, err := t.approver.Approve(ctx, Approval{Title: "执行方案", Detail: plan})
	if err != nil {
		return "", err
	}
	if !allowed {
		return "用户拒绝了方案。请先根据用户意见调整方案或询问需求，不要创建、修改或删除文件。", nil
	}
	return "用户已确认方案。请按方案执行；超出清单的文件改动需要重新调用 confirm_plan。", nil
}
