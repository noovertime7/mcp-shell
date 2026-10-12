package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type notifyUserIn struct {
	Title   string `json:"title,omitempty" jsonschema:"notification title; defaults to MCP Shell"`
	Message string `json:"message" jsonschema:"notification message content to display on the host"`
	Sound   bool   `json:"sound,omitempty" jsonschema:"play alert sound (default true on macOS)"`
}

func notifyUser(title, message string, sound bool) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", errors.New("message is required")
	}
	if title == "" {
		title = "MCP Shell"
	}
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`display notification %q with title %q`, message, title)
		if sound {
			script += ` sound name "Glass"`
		}
		cmd := exec.Command("osascript", "-e", script)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("osascript: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		return fmt.Sprintf("notification displayed on macOS: [%s] %s", title, message), nil
	case "windows":
		script := fmt.Sprintf(`[reflection.assembly]::loadwithpartialname('System.Windows.Forms'); [System.Windows.Forms.MessageBox]::Show(%q, %q)`, message, title)
		_ = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Start()
		return fmt.Sprintf("notification displayed on Windows: [%s] %s", title, message), nil
	default:
		if _, err := exec.LookPath("notify-send"); err == nil {
			_ = exec.Command("notify-send", title, message).Run()
			return fmt.Sprintf("notification displayed on Linux: [%s] %s", title, message), nil
		}
		return fmt.Sprintf("notification logged (no desktop notifier found): [%s] %s", title, message), nil
	}
}

type setClipboardIn struct {
	Text string `json:"text" jsonschema:"text content to place into host clipboard"`
}

func getClipboard() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("pbpaste").Output()
		if err != nil {
			return "", fmt.Errorf("pbpaste: %w", err)
		}
		return string(out), nil
	case "windows":
		out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "Get-Clipboard").Output()
		if err != nil {
			return "", fmt.Errorf("Get-Clipboard: %w", err)
		}
		return strings.ReplaceAll(string(out), "\r\n", "\n"), nil
	default:
		if _, err := exec.LookPath("wl-paste"); err == nil {
			out, _ := exec.Command("wl-paste").Output()
			return string(out), nil
		}
		if _, err := exec.LookPath("xclip"); err == nil {
			out, _ := exec.Command("xclip", "-selection", "clipboard", "-o").Output()
			return string(out), nil
		}
		return "", errors.New("no clipboard tool found (pbpaste/wl-paste/xclip)")
	}
}

func setClipboard(text string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader(text)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("pbcopy: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		return fmt.Sprintf("clipboard updated (%d bytes)", len(text)), nil
	case "windows":
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "Set-Clipboard -Value $input")
		cmd.Stdin = strings.NewReader(text)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("Set-Clipboard: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		return fmt.Sprintf("clipboard updated (%d bytes)", len(text)), nil
	default:
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd := exec.Command("wl-copy")
			cmd.Stdin = strings.NewReader(text)
			_ = cmd.Run()
			return fmt.Sprintf("clipboard updated (%d bytes)", len(text)), nil
		}
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd := exec.Command("xclip", "-selection", "clipboard")
			cmd.Stdin = strings.NewReader(text)
			_ = cmd.Run()
			return fmt.Sprintf("clipboard updated (%d bytes)", len(text)), nil
		}
		return "", errors.New("no clipboard tool found (pbcopy/wl-copy/xclip)")
	}
}

type captureScreenIn struct {
	Path       string `json:"path,omitempty" jsonschema:"destination PNG file path; defaults to screenshot_<timestamp>.png in scratch directory"`
	WindowOnly bool   `json:"window_only,omitempty" jsonschema:"capture only the frontmost window on macOS"`
}

func (s *Server) captureScreen(in captureScreenIn) (string, error) {
	target := strings.TrimSpace(in.Path)
	if target == "" {
		dir := s.scratch
		if dir == "" {
			dir = filepath.Join(os.TempDir(), "mcp-shell")
		}
		_ = os.MkdirAll(dir, 0o755)
		target = filepath.Join(dir, fmt.Sprintf("screenshot_%s.png", time.Now().Format("20060102_150405")))
	} else {
		resolved, err := s.guard.Resolve(target)
		if err != nil {
			return "", err
		}
		target = resolved
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}

	switch runtime.GOOS {
	case "darwin":
		args := []string{"-x"} // -x: quiet, no camera shutter sound
		if in.WindowOnly {
			args = append(args, "-w")
		}
		args = append(args, target)
		cmd := exec.Command("/usr/sbin/screencapture", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("screencapture: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		st, err := os.Stat(target)
		if err != nil {
			return "", fmt.Errorf("screenshot was not saved: %w", err)
		}
		return fmt.Sprintf("screenshot saved: %s (%d bytes)", target, st.Size()), nil
	default:
		return "", fmt.Errorf("capture_screen is currently only supported on macOS (host is %s)", runtime.GOOS)
	}
}

type openTargetIn struct {
	Target string `json:"target" jsonschema:"URL (e.g. http://localhost:3000) or local file/directory path to open on host"`
	App    string `json:"app,omitempty" jsonschema:"specific application name, e.g. Google Chrome, Safari, Finder (optional)"`
}

func (s *Server) openTarget(in openTargetIn) (string, error) {
	target := strings.TrimSpace(in.Target)
	if target == "" {
		return "", errors.New("target is required")
	}
	app := strings.TrimSpace(in.App)

	switch runtime.GOOS {
	case "darwin":
		var args []string
		if app != "" {
			args = append(args, "-a", app)
		}
		args = append(args, target)
		cmd := exec.Command("open", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("open: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		if app != "" {
			return fmt.Sprintf("opened %s in %s", target, app), nil
		}
		return fmt.Sprintf("opened %s with default handler", target), nil
	case "windows":
		var cmd *exec.Cmd
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			cmd = exec.Command("cmd", "/c", "start", "", target)
		} else {
			cmd = exec.Command("explorer", target)
		}
		if err := cmd.Start(); err != nil {
			return "", fmt.Errorf("start: %w", err)
		}
		return fmt.Sprintf("opened %s", target), nil
	default:
		cmd := exec.Command("xdg-open", target)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("xdg-open: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		return fmt.Sprintf("opened %s", target), nil
	}
}

type writeProcessIn struct {
	ID         string `json:"id" jsonschema:"process id returned by start_process, e.g. p1"`
	Input      string `json:"input" jsonschema:"text to write to standard input"`
	CloseStdin bool   `json:"close_stdin,omitempty" jsonschema:"close stdin after writing"`
	Raw        bool   `json:"raw,omitempty" jsonschema:"do not append newline to input"`
}

type inspectPortIn struct {
	Port int  `json:"port" jsonschema:"TCP port number to inspect (1-65535)"`
	Kill bool `json:"kill,omitempty" jsonschema:"terminate the process(es) listening on this port"`
}

func inspectPort(port int, kill bool) (string, error) {
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("invalid port %d; must be between 1 and 65535", port)
	}

	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		cmd := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN")
		out, err := cmd.CombinedOutput()
		output := strings.TrimSpace(string(out))
		if err != nil || output == "" {
			// Also check generic port binding if LISTEN state flag didn't catch UDP or all states
			cmd2 := exec.Command("lsof", "-nP", fmt.Sprintf("-i:%d", port))
			out2, _ := cmd2.CombinedOutput()
			output = strings.TrimSpace(string(out2))
			if output == "" {
				return fmt.Sprintf("port %d: free (no listening process found)", port), nil
			}
		}

		lines := strings.Split(output, "\n")
		var b strings.Builder
		fmt.Fprintf(&b, "port %d status:\n%s\n", port, output)

		if kill && len(lines) > 1 {
			killedPIDs := map[int]bool{}
			for _, line := range lines[1:] {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if pid, err := strconv.Atoi(fields[1]); err == nil && pid > 1 && !killedPIDs[pid] {
						killedPIDs[pid] = true
						if proc, err := os.FindProcess(pid); err == nil {
							_ = proc.Kill()
							fmt.Fprintf(&b, "killed process pid %d (%s)\n", pid, fields[0])
						}
					}
				}
			}
		}
		return b.String(), nil
	}

	if runtime.GOOS == "windows" {
		script := fmt.Sprintf(`Get-NetTCPConnection -LocalPort %d -ErrorAction SilentlyContinue | Select-Object LocalAddress,LocalPort,State,OwningProcess | Format-Table -AutoSize`, port)
		out, _ := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
		res := strings.TrimSpace(string(out))
		if res == "" {
			return fmt.Sprintf("port %d: free (no connection found)", port), nil
		}
		return fmt.Sprintf("port %d:\n%s", port, res), nil
	}

	return fmt.Sprintf("inspect_port not implemented on %s", runtime.GOOS), nil
}

type httpRequestIn struct {
	URL        string            `json:"url" jsonschema:"full URL to request, e.g. http://127.0.0.1:8080/health"`
	Method     string            `json:"method,omitempty" jsonschema:"HTTP method: GET, POST, PUT, DELETE, PATCH, HEAD (default GET)"`
	Headers    map[string]string `json:"headers,omitempty" jsonschema:"HTTP request headers"`
	Body       string            `json:"body,omitempty" jsonschema:"request body content"`
	TimeoutSec int               `json:"timeout_sec,omitempty" jsonschema:"timeout in seconds (default 10, max 60)"`
}

func (s *Server) httpRequest(ctx context.Context, in httpRequestIn) (string, error) {
	url := strings.TrimSpace(in.URL)
	if url == "" {
		return "", errors.New("url is required")
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = "GET"
	}
	timeoutSec := in.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 10
	}
	if timeoutSec > 60 {
		timeoutSec = 60
	}

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var bodyReader io.Reader
	if in.Body != "" {
		bodyReader = strings.NewReader(in.Body)
	}

	req, err := http.NewRequestWithContext(reqCtx, method, url, bodyReader)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	for k, v := range in.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: time.Duration(timeoutSec) * time.Second,
	}

	start := time.Now()
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		return "", fmt.Errorf("request failed after %s: %w", duration.Round(time.Millisecond), err)
	}
	defer resp.Body.Close()

	maxRead := 60000
	if s != nil && s.cfg != nil && s.cfg.MaxReadBytes > 0 {
		maxRead = s.cfg.MaxReadBytes
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxRead+1)))
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "HTTP %s %s -> %s (in %s)\n", method, url, resp.Status, duration.Round(time.Millisecond))
	b.WriteString("--- response headers ---\n")
	for k, v := range resp.Header {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(v, ", "))
	}

	truncated := false
	if len(bodyBytes) > maxRead {
		bodyBytes = bodyBytes[:maxRead]
		truncated = true
	}

	b.WriteString("--- response body ---\n")
	if len(bodyBytes) == 0 {
		b.WriteString("(empty body)\n")
	} else {
		b.Write(bodyBytes)
		if bodyBytes[len(bodyBytes)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	if truncated {
		fmt.Fprintf(&b, "(body truncated to %d bytes)\n", maxRead)
	}
	return b.String(), nil
}
