package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"coding-agent/internal/llm"
)

// SkillSource 只暴露按名称读取 Skill 正文的能力。
type SkillSource interface {
	Load(name string) (string, error)
}

type LoadSkill struct {
	skills SkillSource
}

func NewLoadSkill(skills SkillSource) *LoadSkill {
	return &LoadSkill{skills: skills}
}

func (t *LoadSkill) Definition() llm.Tool {
	return llm.Tool{
		Name:        NameLoadSkill,
		Description: "按名称加载 skills 目录中的 SKILL.md 正文；同一次任务内只需加载一次",
		Parameters:  objectSchema(map[string]any{"name": stringProperty("Skill 名称")}, "name"),
	}
}

func (t *LoadSkill) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	args, err := decodeArgs(raw)
	if err != nil {
		return "", err
	}
	if t.skills == nil {
		return "", fmt.Errorf("Skill 目录未配置")
	}
	return t.skills.Load(args["name"])
}
