package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"coding-agent/internal/llm"
)

const maxReadBytes = 30000

type ReadFile struct{ ws *Workspace }

func NewReadFile(ws *Workspace) *ReadFile { return &ReadFile{ws: ws} }

func (t *ReadFile) Definition() llm.Tool {
	return llm.Tool{
		Name: NameReadFile,
		Description: "读取 workspace 内的文本文件。默认一次返回完整内容（上限 30KB）；" +
			"若结果提示“已读取第 a-b 行”，用 offset 指定起始行（从 1 开始）继续读取即可，" +
			"不要用 run_shell 分段读取文件。",
		Parameters: objectSchema(map[string]any{
			"path":   stringProperty("相对 workspace 的文件路径"),
			"offset": integerProperty("起始行号，从 1 开始；默认 1"),
			"limit":  integerProperty("最多读取多少行；默认读到文件末尾或 30KB 上限"),
		}, "path"),
	}
}

// Execute 默认返回整个文件；文件超过 30KB 时返回一段并在末尾提示下一段 offset。
// 这样模型一次能拿到完整文件，而超大文件也能按行续读，不必依赖 run_shell。
func (t *ReadFile) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	path, err := t.ws.Resolve(args.Path)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	offset := args.Offset
	if offset < 1 {
		offset = 1
	}

	reader := bufio.NewReaderSize(file, 64*1024)
	var b strings.Builder
	lineNo, returned, used := 0, 0, 0
	truncated, firstLineCut := false, false

	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			lineNo++
			if lineNo >= offset {
				if args.Limit > 0 && returned >= args.Limit {
					truncated = true
					break
				}
				if used+len(line) > maxReadBytes {
					// 单行就超过上限时截断这一行；否则从下一行继续。
					if used == 0 {
						b.WriteString(truncateUTF8(line, maxReadBytes))
						returned++
						firstLineCut = true
					}
					truncated = true
					break
				}
				b.WriteString(line)
				used += len(line)
				returned++
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return "", readErr
			}
			break
		}
	}

	if lineNo == 0 {
		return "（空文件）", nil
	}
	if returned == 0 {
		return fmt.Sprintf("[文件共 %d 行，offset=%d 超出范围]", lineNo, offset), nil
	}

	last := offset + returned - 1
	var note string
	switch {
	case firstLineCut:
		note = fmt.Sprintf("\n[第 %d 行超过 %d 字节，已截断显示；请缩小范围或改用 run_shell 处理该行]", last, maxReadBytes)
	case truncated:
		note = fmt.Sprintf("\n[已读取第 %d-%d 行；继续请用 offset=%d]", offset, last, offset+returned)
	case offset > 1:
		note = fmt.Sprintf("\n[已读取第 %d-%d 行，文件结束（共 %d 行）]", offset, last, lineNo)
	}
	return b.String() + note, nil
}

// truncateUTF8 按字节截断，同时保证不切断 UTF-8 字符。
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

type WriteFile struct {
	ws    *Workspace
	scope *ProjectScope
}

func NewWriteFile(ws *Workspace, scope *ProjectScope) *WriteFile {
	return &WriteFile{ws: ws, scope: scope}
}

func (t *WriteFile) Definition() llm.Tool {
	return llm.Tool{
		Name:        NameWriteFile,
		Description: "在当前已确认的项目目录内创建或覆盖文件，自动创建父目录；覆盖已有文件必须已在 confirm_plan 中声明",
		Parameters: objectSchema(map[string]any{
			"path":    stringProperty("相对 workspace 的文件路径，必须在当前项目目录下"),
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
	if err := t.scope.CheckWrite(args["path"]); err != nil {
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
