// Package skills 负责 Skill 的索引与按需加载。
// 索引只保留名称和描述，正文只有在模型调用 load_skill 时才读取（渐进式披露）。
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

type Skill struct {
	Name        string
	Description string
	Path        string
}

type Catalog struct {
	root string

	mu     sync.Mutex
	loaded bool
	items  []Skill
	byName map[string]Skill
}

func NewCatalog(root string) *Catalog {
	return &Catalog{root: root}
}

// List 返回稳定的索引副本；目录扫描失败时返回空索引而不是阻断启动。
func (c *Catalog) List() []Skill {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scanLocked()

	out := make([]Skill, len(c.items))
	copy(out, c.items)
	return out
}

// Load 返回去掉 frontmatter 的正文，避免模型重复看到元数据。
func (c *Catalog) Load(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return "", fmt.Errorf("非法 Skill 名称: %q", name)
	}

	c.mu.Lock()
	c.scanLocked()
	skill, ok := c.byName[name]
	c.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("未找到 Skill: %s", name)
	}

	data, err := os.ReadFile(skill.Path)
	if err != nil {
		return "", err
	}
	_, body := splitFrontmatter(string(data))
	if body = strings.TrimSpace(body); body == "" {
		return fmt.Sprintf("（Skill %q 只有元数据，没有正文内容）", name), nil
	}
	return body, nil
}

func (c *Catalog) scanLocked() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.byName = make(map[string]Skill)

	entries, err := os.ReadDir(c.root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(c.root, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		meta, body := splitFrontmatter(string(data))

		name := strings.TrimSpace(meta["name"])
		if name == "" {
			name = entry.Name()
		}
		description := strings.TrimSpace(meta["description"])
		if description == "" {
			description = firstSentence(body)
		}

		skill := Skill{Name: name, Description: description, Path: path}
		c.items = append(c.items, skill)
		c.byName[name] = skill
	}
}

func validName(name string) bool {
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

// splitFrontmatter 解析 --- 包裹的简单 YAML 头部，支持 ">" 与 "|" 多行值。
func splitFrontmatter(text string) (map[string]string, string) {
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, text
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, text
	}

	meta := make(map[string]string)
	var key string
	var parts []string
	flush := func() {
		if key == "" {
			return
		}
		meta[key] = strings.TrimSpace(strings.Join(parts, " "))
		key, parts = "", nil
	}

	for _, raw := range lines[1:end] {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if key != "" {
				parts = append(parts, strings.TrimSpace(line))
			}
			continue
		}
		flush()

		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch v {
		case ">", "|", ">-", "|-":
			// 多行值：后续缩进行由下面的分支收集。
		default:
			meta[key] = strings.Trim(v, `"'`)
			key = ""
		}
	}
	flush()

	return meta, strings.Join(lines[end+1:], "\n")
}

func firstSentence(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		if line != "" {
			return truncate(line, 200)
		}
	}
	return ""
}

func truncate(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "…"
}
