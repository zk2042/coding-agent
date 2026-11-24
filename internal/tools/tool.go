// Package tools 提供 Agent 可调用的本地能力。
// 每个工具只关心自己的参数与边界，注册表负责查找和统一错误包装。
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"coding-agent/internal/llm"
)

// 工具名即模型看到的函数名，统一使用 snake_case。
const (
	NameReadFile      = "read_file"
	NameWriteFile     = "write_file"
	NameDeleteFile    = "delete_file"
	NameListFiles     = "list_files"
	NameWriteTempFile = "write_temp_file"
	NameShell         = "run_shell"
	NameLoadSkill     = "load_skill"
	NamePreviewSite   = "preview_site"
	NameConfirmPlan   = "confirm_plan"
)

// Tool 是所有本地能力的统一抽象。
// 新增工具只需实现这两个方法，并在组合根注册；Agent 不需要认识具体类型。
type Tool interface {
	Definition() llm.Tool
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// objectSchema 生成模型可读的 JSON Schema。
// required 为空时不写该字段，避免序列化成 null 被严格网关拒绝。
func objectSchema(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func stringArrayProperty(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": description,
	}
}

func integerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func booleanProperty(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

// decodeArgs 把模型给的 JSON 解析成字符串参数。
// 当前工具的参数都是字符串；需要复杂结构时单独定义参数类型。
func decodeArgs(raw json.RawMessage) (map[string]string, error) {
	var args map[string]string
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("参数必须是字符串对象: %w", err)
	}
	return args, nil
}
