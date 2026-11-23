package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Workspace 是唯一允许 Agent 读写的目录。所有路径都经过 Resolve 校验。
type Workspace struct {
	root string
}

func NewWorkspace(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &Workspace{root: abs}, nil
}

func (w *Workspace) Root() string {
	return w.root
}

// Resolve 拒绝绝对路径、路径穿越和已有的符号链接组件。
func (w *Workspace) Resolve(rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("只允许工作区相对路径: %q", rel)
	}

	full := filepath.Clean(filepath.Join(w.root, rel))
	back, err := filepath.Rel(w.root, full)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径越界: %q", rel)
	}

	for path := full; path != w.root; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("不允许访问符号链接: %q", rel)
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	return full, nil
}

func (w *Workspace) Rel(full string) (string, error) {
	return filepath.Rel(w.root, full)
}
