package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Name             string
	Command          string
	Args             []string
	Env              map[string]string
	Enabled          bool
	ApprovalRequired bool
}

type configFile struct {
	Servers []struct {
		Name             string            `json:"name"`
		Command          string            `json:"command"`
		Args             []string          `json:"args"`
		Env              map[string]string `json:"env"`
		Enabled          bool              `json:"enabled"`
		ApprovalRequired *bool             `json:"approval_required"`
	} `json:"servers"`
}

// LoadConfigs 读取 mcp.json；文件不存在时返回空列表，便于在无 MCP 环境下直接启动。
func LoadConfigs(path string) ([]Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var file configFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}

	var configs []Config
	for _, server := range file.Servers {
		name := strings.TrimSpace(server.Name)
		command := strings.TrimSpace(server.Command)
		if !server.Enabled || name == "" || command == "" {
			continue
		}
		// 默认需要审批；只有显式写成 false 才跳过，避免漏配导致静默执行。
		approval := true
		if server.ApprovalRequired != nil {
			approval = *server.ApprovalRequired
		}
		configs = append(configs, Config{
			Name:             name,
			Command:          command,
			Args:             server.Args,
			Env:              server.Env,
			Enabled:          true,
			ApprovalRequired: approval,
		})
	}
	return configs, nil
}
