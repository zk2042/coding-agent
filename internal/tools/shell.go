package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"coding-agent/internal/llm"
)

type Approval struct {
	Title  string
	Detail string
}

// Approver 是 Shell 与 MCP 共用的审批端口。TUI 或测试可以实现它。
type Approver interface {
	Approve(ctx context.Context, req Approval) (bool, error)
}

type Shell struct {
	ws       *Workspace
	approver Approver
	timeout  time.Duration
}

func NewShell(ws *Workspace, approver Approver, timeout time.Duration) *Shell {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Shell{ws: ws, approver: approver, timeout: timeout}
}

func (s *Shell) Definition() llm.Tool {
	return llm.Tool{
		Name:        NameShell,
		Description: "在 workspace 根目录执行 shell 命令，路径一律使用 workspace 相对路径；执行前必须获得用户审批",
		Parameters:  objectSchema(map[string]any{"command": stringProperty("要执行的 shell 命令")}, "command"),
	}
}

func (s *Shell) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	args, err := decodeArgs(raw)
	if err != nil {
		return "", err
	}
	command := strings.TrimSpace(args["command"])
	if command == "" {
		return "", fmt.Errorf("命令不能为空")
	}

	if s.approver == nil {
		return "", fmt.Errorf("未配置审批器，已拒绝执行")
	}
	allowed, err := s.approver.Approve(ctx, Approval{Title: "Shell 命令", Detail: command})
	if err != nil {
		return "", err
	}
	if !allowed {
		return "用户拒绝了 Shell 执行", nil
	}

	runCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(runCtx, "cmd.exe", "/C", command)
	} else {
		cmd = exec.CommandContext(runCtx, "sh", "-lc", command)
	}
	// 一律在 workspace 根目录执行：write_temp_file 返回的 .agent-tmp/... 以及
	// 模型写的 portfolio/... 都是相对 workspace 根目录的路径。
	dir := s.ws.Root()
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PWD="+dir)
	cmd.WaitDelay = 2 * time.Second

	var output cappedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	runErr := cmd.Run()

	if runCtx.Err() == context.DeadlineExceeded {
		return output.String(), fmt.Errorf("命令超时（%s）", s.timeout)
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return output.String(), fmt.Errorf("命令执行失败: %w", runErr)
		}
		// 进程已正常启动并结束，只是退出码非 0。很多命令用非 0 退出码表达
		// “没有匹配/没有差异”（如 findstr、grep），并不是执行失败。这里把退出码
		// 随结果一起返回，交给模型判断，避免它误以为失败而反复换参数重试。
		return commandResult(output.String(), exitErr.ExitCode(), command), nil
	}
	if strings.TrimSpace(output.String()) == "" {
		return "（命令执行成功，无输出）", nil
	}
	return output.String(), nil
}

// commandResult 把非 0 退出码作为正常结果返回，并对常见“非错误”退出码加注说明。
func commandResult(out string, code int, command string) string {
	if strings.TrimSpace(out) == "" {
		out = "（无输出）"
	}
	if code == 1 && strings.Contains(strings.ToLower(command), "findstr") {
		return out + "\n[退出码 1：findstr 未找到匹配，属正常结果]"
	}
	return out + fmt.Sprintf("\n[退出码 %d]", code)
}

// cappedOutput 丢弃超限字节但继续排空管道，避免大量输出占满内存或阻塞子进程。
type cappedOutput struct {
	buf       bytes.Buffer
	truncated bool
}

func (o *cappedOutput) Write(p []byte) (int, error) {
	n := min(len(p), 20000-o.buf.Len())
	_, _ = o.buf.Write(p[:n])
	if n < len(p) {
		o.truncated = true
	}
	return len(p), nil
}

func (o *cappedOutput) String() string {
	if o.truncated {
		return o.buf.String() + "\n[命令输出截断]"
	}
	return o.buf.String()
}

func ShellGuide() string {
	if runtime.GOOS == "windows" {
		return "Windows 使用 cmd.exe /C，不是 PowerShell；当前目录是 workspace 根目录，路径一律写 workspace 相对路径（例如 portfolio\\index.html）。不要使用 ls、grep、cat。查看文件内容优先用 read_file；非 0 退出码会作为结果返回（findstr 退出码 1 只表示没有匹配），不要为此反复换参数重试；复杂检查用 write_temp_file 写一次性脚本。"
	}
	return runtime.GOOS + " 使用 sh -lc；当前目录是 workspace 根目录，路径使用 workspace 相对路径；优先使用内置文件工具。"
}
