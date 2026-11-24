package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"coding-agent/internal/llm"
)

// DeleteFile 删除 workspace 内的单个文件，执行前必须获得用户审批。
// 不允许删除目录，避免一次审批删掉整棵子树。
type DeleteFile struct {
	ws       *Workspace
	approver Approver
	scope    *ProjectScope
}

func NewDeleteFile(ws *Workspace, approver Approver, scope *ProjectScope) *DeleteFile {
	return &DeleteFile{ws: ws, approver: approver, scope: scope}
}

func (t *DeleteFile) Definition() llm.Tool {
	return llm.Tool{
		Name:        NameDeleteFile,
		Description: "删除当前已确认项目内的单个文件，执行前必须获得用户审批；不能删除目录",
		Parameters:  objectSchema(map[string]any{"path": stringProperty("相对 workspace 的文件路径，必须在当前项目目录下")}, "path"),
	}
}

func (t *DeleteFile) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	args, err := decodeArgs(raw)
	if err != nil {
		return "", err
	}
	rel := strings.TrimSpace(args["path"])
	if rel == "" || rel == "." {
		return "", fmt.Errorf("请指定要删除的文件")
	}
	full, err := t.ws.Resolve(rel)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("不能删除目录: %q", rel)
	}
	if err := t.scope.CheckPath(rel); err != nil {
		return "", err
	}
	if t.approver == nil {
		return "", fmt.Errorf("未配置审批器，已拒绝执行")
	}

	allowed, err := t.approver.Approve(ctx, Approval{Title: "删除文件", Detail: rel})
	if err != nil {
		return "", err
	}
	if !allowed {
		return "用户拒绝了删除文件：" + rel, nil
	}
	if err := os.Remove(full); err != nil {
		return "", err
	}
	return "文件已删除：" + rel, nil
}
