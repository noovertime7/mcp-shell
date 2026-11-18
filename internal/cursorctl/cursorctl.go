// Package cursorctl 控制正在运行的 Cursor：列出 Agent、读对话、发消息、开新 Agent。
//
// 做法是打开 Cursor 主进程的 Node inspector，在主进程里操作窗口，不用重启 Cursor。
// 权限要不低于 Cursor：Cursor 以管理员启动时，本服务也要以管理员启动。
package cursorctl

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

//go:embed page.js
var pageJS string

// DefaultPort 是 Node inspector 的默认端口。
const DefaultPort = 9229

// Agent 是 Cursor 左侧列表的一行。
type Agent struct {
	Index    int    `json:"i"`
	Selected bool   `json:"selected"`
	Busy     bool   `json:"busy"`
	Title    string `json:"title"`
	Sub      string `json:"sub"`
	Text     string `json:"text,omitempty"`
}

// Client 连接一个 Cursor 实例。
type Client struct {
	Port int
	// open 打开 inspector，测试时替换。
	open func(ctx context.Context, port int) error
}

// New 返回连接本机 Cursor 的 Client。
func New() *Client {
	return &Client{Port: DefaultPort, open: openInspector}
}

type request struct {
	Cmd   string `json:"cmd"`
	Agent string `json:"agent,omitempty"`
	Text  string `json:"text,omitempty"`
	Tail  int    `json:"tail,omitempty"`
	Mode  string `json:"mode,omitempty"`
}

// Agents 列出 Agent，顺序会随活动变化。
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var out []Agent
	err := c.run(ctx, request{Cmd: "agents"}, &out)
	return out, err
}

// Read 返回一个 Agent 当前可见的对话文字，最多 tail 个字符。
func (c *Client) Read(ctx context.Context, agent string, tail int) (Agent, error) {
	var out Agent
	err := c.run(ctx, request{Cmd: "read", Agent: agent, Tail: tail}, &out)
	return out, err
}

// 发给运行中 Agent 的方式。
const (
	ModeQueue = "queue" // 等当前任务结束后再发
	ModeNow   = "now"   // 立即送达，相当于在 Cursor 里按 Ctrl+Enter
)

// Send 给一个 Agent 发消息；Agent 在运行时必须指定 mode，否则拒绝。
func (c *Client) Send(ctx context.Context, agent, text, mode string) (Agent, error) {
	var out Agent
	if strings.TrimSpace(text) == "" {
		return out, errors.New("text is required")
	}
	if mode != "" && mode != ModeQueue && mode != ModeNow {
		return out, fmt.Errorf("mode must be %s or %s, got %q", ModeQueue, ModeNow, mode)
	}
	err := c.run(ctx, request{Cmd: "send", Agent: agent, Text: text, Mode: mode}, &out)
	return out, err
}

// NewAgent 新建 Agent 并发第一条消息，返回发送后的列表。
func (c *Client) NewAgent(ctx context.Context, text string) ([]Agent, error) {
	var out []Agent
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("text is required")
	}
	err := c.run(ctx, request{Cmd: "new", Text: text}, &out)
	return out, err
}

func (c *Client) run(ctx context.Context, req request, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	wsURL, err := c.target(ctx)
	if err != nil {
		return err
	}
	args, err := json.Marshal(req)
	if err != nil {
		return err
	}
	value, err := evaluate(ctx, wsURL, "("+pageJS+")("+string(args)+")")
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(value), out)
}

// target 返回 inspector 的 WebSocket 地址，没打开就先打开。
func (c *Client) target(ctx context.Context) (string, error) {
	if u, err := listTarget(ctx, c.Port); err == nil {
		return u, nil
	}
	if err := c.open(ctx, c.Port); err != nil {
		return "", err
	}
	var last error
	for i := 0; i < 20; i++ {
		u, err := listTarget(ctx, c.Port)
		if err == nil {
			return u, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("Cursor inspector did not open on port %d: %w", c.Port, last)
}

func listTarget(ctx context.Context, port int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", port), nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var list []struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", err
	}
	if len(list) == 0 || list[0].URL == "" {
		return "", errors.New("no inspector target")
	}
	return list[0].URL, nil
}

type evalResponse struct {
	ID     int `json:"id"`
	Result struct {
		// 脚本抛错时 value 是错误对象，不是字符串
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func evaluate(ctx context.Context, wsURL, expr string) (string, error) {
	cfg, err := websocket.NewConfig(wsURL, "http://127.0.0.1/")
	if err != nil {
		return "", err
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", cfg.Location.Host)
	if err != nil {
		return "", fmt.Errorf("connect to Cursor inspector: %w", err)
	}
	defer raw.Close()
	if d, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(d)
	}
	conn, err := websocket.NewClient(cfg, raw)
	if err != nil {
		return "", fmt.Errorf("connect to Cursor inspector: %w", err)
	}
	msg, err := json.Marshal(map[string]any{
		"id": 1, "method": "Runtime.evaluate",
		"params": map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true, "includeCommandLineAPI": true},
	})
	if err != nil {
		return "", err
	}
	if err := websocket.Message.Send(conn, string(msg)); err != nil {
		return "", err
	}
	for {
		var frame string
		if err := websocket.Message.Receive(conn, &frame); err != nil {
			return "", fmt.Errorf("read from Cursor inspector: %w", err)
		}
		var r evalResponse
		if err := json.Unmarshal([]byte(frame), &r); err != nil {
			return "", err
		}
		if r.ID != 1 {
			continue
		}
		if r.Error != nil {
			return "", errors.New(r.Error.Message)
		}
		if e := r.Result.ExceptionDetails; e != nil {
			line, _, _ := strings.Cut(e.Exception.Description, "\n")
			return "", errors.New(strings.TrimPrefix(line, "Error: "))
		}
		var value string
		if err := json.Unmarshal(r.Result.Result.Value, &value); err != nil {
			return "", fmt.Errorf("unexpected result from Cursor: %w", err)
		}
		return value, nil
	}
}

// openInspector 让 Cursor 主进程打开 inspector：Cursor.exe 以 Node 方式运行一次，发调试信号。
func openInspector(ctx context.Context, _ int) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("Cursor control is only supported on Windows (host is %s)", runtime.GOOS)
	}
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Get-Process Cursor -ErrorAction SilentlyContinue | Where-Object { $_.MainWindowTitle } | Select-Object -First 1 | ForEach-Object { "$($_.Id)|$($_.Path)" }`).Output()
	if err != nil {
		return fmt.Errorf("find Cursor: %w", err)
	}
	pid, exe, err := parseMainProcess(string(out))
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "-e", "process._debugProcess("+strconv.Itoa(pid)+")")
	cmd.Env = append(os.Environ(), "ELECTRON_RUN_AS_NODE=1")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("signal Cursor: %w (%s); this service must run with at least the same rights as Cursor", err, strings.TrimSpace(string(b)))
	}
	return nil
}

func parseMainProcess(s string) (int, string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, "", errors.New("no running Cursor window")
	}
	id, exe, _ := strings.Cut(s, "|")
	pid, err := strconv.Atoi(id)
	if err != nil {
		return 0, "", fmt.Errorf("unexpected process line %q", s)
	}
	if exe == "" {
		return 0, "", errors.New("cannot read the Cursor process path: Cursor runs as administrator, so start MCP Shell as administrator too")
	}
	return pid, exe, nil
}
