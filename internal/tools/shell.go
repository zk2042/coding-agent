package tools

import (
	"bytes"
	"context"
	"encoding/json"
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
		Description: "在 workspace 目录执行 shell 命令；执行前必须获得用户审批",
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
	cmd.Dir = s.ws.Root()
	cmd.Env = append(os.Environ(), "PWD="+s.ws.Root())
	cmd.WaitDelay = 2 * time.Second

	var output cappedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	runErr := cmd.Run()

	if runCtx.Err() == context.DeadlineExceeded {
		return output.String(), fmt.Errorf("命令超时（%s）", s.timeout)
	}
	if runErr != nil {
		return output.String(), fmt.Errorf("命令执行失败: %w", runErr)
	}
	if strings.TrimSpace(output.String()) == "" {
		return "（命令执行成功，无输出）", nil
	}
	return output.String(), nil
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
		return "Windows 使用 cmd.exe /C，不是 PowerShell；不要使用 ls、grep、cat。目录和文本优先用内置工具，复杂检查用 write_temp_file。"
	}
	return runtime.GOOS + " 使用 sh -lc；优先使用内置文件工具。"
}
