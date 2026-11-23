package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"coding-agent/internal/llm"
)

// PreviewSite 在 127.0.0.1 上为工作区内的静态目录启动临时服务。
// 默认只启动服务，由 Playwright 在有头 Chrome 中打开并验证；
// 传 open=true 时才额外用系统默认浏览器打开。
type PreviewSite struct {
	ws *Workspace

	mu          sync.Mutex
	servers     []*http.Server
	openBrowser func(string) error
}

func NewPreviewSite(ws *Workspace) *PreviewSite {
	return &PreviewSite{ws: ws, openBrowser: openInBrowser}
}

func (p *PreviewSite) Definition() llm.Tool {
	return llm.Tool{
		Name:        NamePreviewSite,
		Description: "为 workspace 内的网页目录启动本地预览服务并返回地址；默认不弹窗，由 Playwright 打开并验证",
		Parameters: objectSchema(map[string]any{
			"path": stringProperty("相对 workspace 的目录，默认 ."),
			"port": integerProperty("端口，默认 0 表示自动分配"),
			"open": booleanProperty("是否额外用系统默认浏览器打开，默认 false"),
		}),
	}
}

type previewArgs struct {
	Path string `json:"path"`
	Port int    `json:"port"`
	Open bool   `json:"open"`
}

func (p *PreviewSite) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args previewArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("参数解析失败: %w", err)
		}
	}
	dir, err := p.ws.Resolve(args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s 不是目录", args.Path)
	}

	listener, err := listen(args.Port)
	if err != nil {
		return "", err
	}
	server := &http.Server{
		Handler:           http.FileServer(http.Dir(dir)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = server.Serve(listener)
	}()

	p.mu.Lock()
	p.servers = append(p.servers, server)
	p.mu.Unlock()

	url := fmt.Sprintf("http://127.0.0.1:%d/", listener.Addr().(*net.TCPAddr).Port)
	note := ""
	if args.Open && p.openBrowser != nil {
		if err := p.openBrowser(url); err != nil {
			note = "\n（自动打开浏览器失败，请手动访问）"
		}
	}
	rel, _ := p.ws.Rel(dir)
	return fmt.Sprintf("预览已启动：%s\n目录：%s%s", url, rel, note), nil
}

func (p *PreviewSite) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.mu.Lock()
	servers := p.servers
	p.servers = nil
	p.mu.Unlock()

	for _, server := range servers {
		_ = server.Shutdown(ctx)
	}
}

func listen(port int) (net.Listener, error) {
	if port > 0 {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return listener, nil
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("启动预览服务失败: %w", err)
	}
	return listener, nil
}

func openInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
