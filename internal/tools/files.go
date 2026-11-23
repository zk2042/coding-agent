package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"coding-agent/internal/llm"
)

const maxReadBytes = 30000

type ReadFile struct{ ws *Workspace }

func NewReadFile(ws *Workspace) *ReadFile { return &ReadFile{ws: ws} }

func (t *ReadFile) Definition() llm.Tool {
	return llm.Tool{
		Name:        NameReadFile,
		Description: "读取 workspace 内的文本文件；超过 30KB 会截断",
		Parameters:  objectSchema(map[string]any{"path": stringProperty("相对 workspace 的文件路径")}, "path"),
	}
}

func (t *ReadFile) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	args, err := decodeArgs(raw)
	if err != nil {
		return "", err
	}
	path, err := t.ws.Resolve(args["path"])
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// 只读取上限多一个字节，避免为了截断而把超大文件整体读进内存。
	data, err := io.ReadAll(io.LimitReader(file, maxReadBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxReadBytes {
		return string(data[:maxReadBytes]) + "\n[内容已截断]", nil
	}
	return string(data), nil
}

type WriteFile struct{ ws *Workspace }

func NewWriteFile(ws *Workspace) *WriteFile { return &WriteFile{ws: ws} }

func (t *WriteFile) Definition() llm.Tool {
	return llm.Tool{
		Name:        NameWriteFile,
		Description: "在 workspace 内创建或覆盖文件，自动创建父目录",
		Parameters: objectSchema(map[string]any{
			"path":    stringProperty("相对 workspace 的文件路径"),
			"content": stringProperty("文件完整内容"),
		}, "path", "content"),
	}
}

func (t *WriteFile) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	args, err := decodeArgs(raw)
	if err != nil {
		return "", err
	}
	path, err := t.ws.Resolve(args["path"])
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(args["content"]), 0o644); err != nil {
		return "", err
	}
	return "文件已写入：" + args["path"], nil
}

type ListFiles struct{ ws *Workspace }

func NewListFiles(ws *Workspace) *ListFiles { return &ListFiles{ws: ws} }

func (t *ListFiles) Definition() llm.Tool {
	return llm.Tool{
		Name:        NameListFiles,
		Description: "列出 workspace 内文件与目录，最多 300 条",
		Parameters:  objectSchema(map[string]any{"path": stringProperty("相对目录，默认 .")}),
	}
}

func (t *ListFiles) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	args, err := decodeArgs(raw)
	if err != nil {
		return "", err
	}
	root, err := t.ws.Resolve(args["path"])
	if err != nil {
		return "", err
	}

	var lines []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if len(lines) >= 300 {
			return filepath.SkipAll
		}
		rel, err := t.ws.Rel(path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			rel += "/"
		}
		lines = append(lines, rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "（目录为空）", nil
	}
	return strings.Join(lines, "\n"), nil
}

type WriteTempFile struct{ ws *Workspace }

func NewWriteTempFile(ws *Workspace) *WriteTempFile { return &WriteTempFile{ws: ws} }

func (t *WriteTempFile) Definition() llm.Tool {
	return llm.Tool{
		Name:        NameWriteTempFile,
		Description: "生成一次性检查脚本，写入 workspace/.agent-tmp，返回唯一相对路径",
		Parameters: objectSchema(map[string]any{
			"suffix":  stringProperty("文件扩展名，例如 .js"),
			"content": stringProperty("脚本内容"),
		}, "suffix", "content"),
	}
}

func (t *WriteTempFile) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	args, err := decodeArgs(raw)
	if err != nil {
		return "", err
	}
	suffix := args["suffix"]
	if !validSuffix(suffix) {
		return "", fmt.Errorf("无效的脚本扩展名: %q", suffix)
	}

	dir, err := t.ws.Resolve(".agent-tmp")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "check-*"+suffix)
	if err != nil {
		return "", err
	}
	if _, err = file.WriteString(args["content"]); err != nil {
		file.Close()
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	return t.ws.Rel(file.Name())
}

func validSuffix(suffix string) bool {
	if len(suffix) < 2 || len(suffix) > 12 || suffix[0] != '.' {
		return false
	}
	for _, r := range suffix[1:] {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
