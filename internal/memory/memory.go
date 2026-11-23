// Package memory 保存跨任务仍然成立的长期记忆。
// 只存结构化的事实、偏好、项目约定与决策，不保存会话流水。
package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const maxTextRunes = 300

var allowedKinds = map[string]bool{
	"preference": true,
	"fact":       true,
	"convention": true,
	"decision":   true,
}

// Entry 是一条长期记忆。
type Entry struct {
	Key       string    `json:"key"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store 是单用户的长期记忆文件。所有条目保存在一个 JSON 数组里。
type Store struct {
	path    string
	mu      sync.RWMutex
	entries map[string]Entry
}

// Open 加载记忆文件；文件不存在时返回空仓库。
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("长期记忆文件路径为空")
	}
	s := &Store{path: path, entries: make(map[string]Entry)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取长期记忆失败: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return s, nil
	}

	var list []Entry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("解析长期记忆失败: %w", err)
	}
	for _, entry := range list {
		entry = clean(entry)
		if entry.Key == "" || entry.Text == "" {
			continue
		}
		if entry.CreatedAt.IsZero() {
			entry.CreatedAt = time.Now()
		}
		if entry.UpdatedAt.IsZero() {
			entry.UpdatedAt = entry.CreatedAt
		}
		s.entries[entry.Key] = entry
	}
	return s, nil
}

// Entries 返回按更新时间倒序排列的副本。
func (s *Store) Entries() []Entry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		list = append(list, entry)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	return list
}

// Upsert 按 Key 合并条目；相同 Key 以新内容覆盖，并返回新增与更新数量。
// 不同 Key 但规范化文本相同的条目会被去重，避免同一事实重复注入。
func (s *Store) Upsert(entries []Entry) (int, int, error) {
	if s == nil {
		return 0, 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	textOwners := make(map[string]string, len(s.entries))
	for key, entry := range s.entries {
		if normalized := normalizeText(entry.Text); normalized != "" {
			textOwners[normalized] = key
		}
	}

	now := time.Now()
	added, updated := 0, 0
	for _, raw := range entries {
		entry := clean(raw)
		if entry.Key == "" || entry.Text == "" {
			continue
		}
		normalized := normalizeText(entry.Text)

		if old, ok := s.entries[entry.Key]; ok {
			if old.Text == entry.Text && old.Kind == entry.Kind {
				continue
			}
			if oldNormalized := normalizeText(old.Text); oldNormalized != "" && textOwners[oldNormalized] == entry.Key {
				delete(textOwners, oldNormalized)
			}
			if normalized != "" {
				if owner, ok := textOwners[normalized]; ok && owner != entry.Key {
					// 同一内容已挂在另一个 key 下，删除旧条目。
					delete(s.entries, owner)
				}
			}
			entry.CreatedAt = old.CreatedAt
			entry.UpdatedAt = now
			s.entries[entry.Key] = entry
			if normalized != "" {
				textOwners[normalized] = entry.Key
			}
			updated++
			continue
		}

		if normalized != "" {
			if _, ok := textOwners[normalized]; ok {
				continue
			}
		}
		entry.CreatedAt = now
		entry.UpdatedAt = now
		s.entries[entry.Key] = entry
		if normalized != "" {
			textOwners[normalized] = entry.Key
		}
		added++
	}
	if added+updated == 0 {
		return 0, 0, nil
	}
	if err := s.saveLocked(); err != nil {
		return 0, 0, err
	}
	return added, updated, nil
}

func (s *Store) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建记忆目录失败: %w", err)
	}

	list := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		list = append(list, entry)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Key < list[j].Key
	})

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("编码长期记忆失败: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".memory-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时记忆文件失败: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("写入临时记忆文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("关闭临时记忆文件失败: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		// Windows 上目标已存在时个别文件系统会拒绝直接重命名。
		if removeErr := os.Remove(s.path); removeErr == nil {
			err = os.Rename(name, s.path)
		}
		if err != nil {
			os.Remove(name)
			return fmt.Errorf("替换记忆文件失败: %w", err)
		}
	}
	return nil
}

func clean(entry Entry) Entry {
	entry.Text = truncateRunes(strings.TrimSpace(entry.Text), maxTextRunes)
	entry.Kind = strings.ToLower(strings.TrimSpace(entry.Kind))
	if !allowedKinds[entry.Kind] {
		entry.Kind = "fact"
	}
	entry.Key = keyFor(entry.Key, entry.Text)
	return entry
}

func keyFor(key, text string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.Join(strings.Fields(key), "_")
	key = strings.Trim(key, "_:")
	if key != "" {
		return truncateRunes(key, 80)
	}
	normalized := normalizeText(text)
	if normalized == "" {
		return ""
	}
	return "auto:" + truncateRunes(normalized, 80)
}

func normalizeText(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
