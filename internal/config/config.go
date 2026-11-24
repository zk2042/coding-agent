package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	// Root 是项目根目录：从当前工作目录向上找到的 go.mod 或 .env 所在目录。
	// 所有相对配置路径都基于它解析。
	Root string

	BaseURL, APIKey, Model                   string
	WorkspaceDir, SkillsDir, MCPConfig       string
	MemoryFile                               string
	MemoryEnabled                            bool
	MaxSteps                                 int
	RunTimeoutSeconds, RequestTimeoutSeconds int
	ShellTimeoutSeconds                      int
}

// Load 先定位项目根目录，再读取根目录下的 .env。
// 已存在的环境变量优先，便于临时覆盖配置；相对路径统一基于项目根解析，
// 因此从 cmd/ 等子目录启动也能读写同一套 workspace、skills 与记忆文件。
func Load() Config {
	root := findProjectRoot()
	values := loadDotEnv(filepath.Join(root, ".env"))

	return Config{
		Root:                  root,
		BaseURL:               setting(values, "SCNET_BASE_URL", "https://api.scnet.cn/api/llm/v1"),
		APIKey:                setting(values, "SCNET_API_KEY", ""),
		Model:                 setting(values, "SCNET_MODEL", "DeepSeek-V4.1-Flash-1017-Event"),
		WorkspaceDir:          resolve(root, setting(values, "WORKSPACE_DIR", "./workspace")),
		SkillsDir:             resolve(root, setting(values, "SKILLS_DIR", "./skills")),
		MCPConfig:             resolve(root, setting(values, "MCP_CONFIG", "./configs/mcp.json")),
		MemoryFile:            resolve(root, setting(values, "MEMORY_FILE", "./data/memory.json")),
		MemoryEnabled:         !strings.EqualFold(setting(values, "MEMORY_ENABLED", "true"), "false"),
		MaxSteps:              settingInt(values, "AGENT_MAX_STEPS", 12),
		RunTimeoutSeconds:     settingIntZero(values, "AGENT_TIMEOUT_SECONDS", 0),
		RequestTimeoutSeconds: settingInt(values, "MODEL_REQUEST_TIMEOUT_SECONDS", 120),
		ShellTimeoutSeconds:   settingInt(values, "SHELL_TIMEOUT_SECONDS", 60),
	}
}

func Validate(cfg Config) error {
	if strings.TrimSpace(cfg.APIKey) == "" {
		envPath := filepath.Join(cfg.Root, ".env")
		if _, err := os.Stat(envPath); err != nil {
			return fmt.Errorf("未配置 SCNET_API_KEY，且未找到 %s；请复制 .env.example 为 .env 并填写 Token Plan 专属 API Key", envPath)
		}
		return fmt.Errorf("未配置 SCNET_API_KEY；请在 %s 中填写 Token Plan 专属 API Key", envPath)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return errors.New("未配置 SCNET_MODEL")
	}
	return nil
}

// setting 遵循“环境变量 > .env > 默认值”的优先级；空字符串视为未配置。
func setting(values map[string]string, key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	if value := strings.TrimSpace(values[key]); value != "" {
		return value
	}
	return fallback
}

func settingInt(values map[string]string, key string, fallback int) int {
	value, err := strconv.Atoi(setting(values, key, ""))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

// settingIntZero 与 settingInt 相同，但允许 0；0 表示关闭该项限制。
func settingIntZero(values map[string]string, key string, fallback int) int {
	value, err := strconv.Atoi(setting(values, key, ""))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func resolve(root, path string) string {
	if path == "" {
		return root
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, path)
}

// findProjectRoot 优先使用最近的 go.mod，其次使用最近的 .env；
// 都找不到时退回当前工作目录，保持和以前一致的行为。
func findProjectRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	if root := searchUp(cwd, "go.mod"); root != "" {
		return root
	}
	if root := searchUp(cwd, ".env"); root != "" {
		return root
	}
	return cwd
}

func searchUp(start, name string) string {
	for dir := start; ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
	}
}

// loadDotEnv 只解析文件，不修改进程环境，便于测试和避免全局副作用。
// 配置文件很小，直接整体读取，避免 bufio.Scanner 的单行长度上限。
func loadDotEnv(path string) map[string]string {
	values := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return values
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		if key != "" {
			values[key] = value
		}
	}
	return values
}
