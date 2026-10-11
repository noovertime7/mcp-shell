// Package server wires the file and exec tools into an MCP server and puts
// bearer-token authentication, an IP allowlist and an audit log in front.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/noovertime7/mcp-shell/internal/config"
	"github.com/noovertime7/mcp-shell/internal/exectools"
	"github.com/noovertime7/mcp-shell/internal/fstools"
	"github.com/noovertime7/mcp-shell/internal/history"
	"github.com/noovertime7/mcp-shell/internal/mcp"
	"github.com/noovertime7/mcp-shell/internal/pathguard"
)

// Version is reported to MCP clients.
const Version = "0.2.0"

// Server is the mcp-shell server.
type Server struct {
	cfg     *config.Config
	guard   *pathguard.Guard
	fs      *fstools.FS
	runner  *exectools.Runner
	procs   *exectools.Manager
	mcp     *mcp.Server
	nets    []*net.IPNet
	history *history.Store
	// scratch is a directory for temporary files of the model (commit
	// messages, patches), offered in list_roots; empty without output_dir.
	scratch string

	auditMu sync.Mutex
	audit   *os.File
}

// New builds a Server from a validated config.
func New(cfg *config.Config) (*Server, error) {
	guard, err := pathguard.New(cfg.Roots, cfg.PathAliases)
	if err != nil {
		return nil, err
	}
	guard.SetUnrestricted(cfg.Unrestricted)
	s := &Server{
		cfg:   cfg,
		guard: guard,
		fs:    &fstools.FS{Guard: guard, MaxReadBytes: cfg.MaxReadBytes},
		runner: &exectools.Runner{
			Guard:          guard,
			DefaultShell:   cfg.DefaultShell,
			DefaultTimeout: time.Duration(cfg.CommandTimeoutSec) * time.Second,
			MaxTimeout:     time.Duration(cfg.MaxCommandTimeoutSec) * time.Second,
			MaxOutputBytes: cfg.MaxOutputBytes,
		},
	}
	if cfg.OutputDir != "" {
		if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
			return nil, fmt.Errorf("create output_dir: %w", err)
		}
		exectools.CleanLogs(cfg.OutputDir, 3*24*time.Hour)
		s.runner.LogDir = cfg.OutputDir
		s.scratch = filepath.Join(cfg.OutputDir, "scratch")
		if err := os.MkdirAll(s.scratch, 0o755); err != nil {
			s.scratch = ""
		}
	}
	s.procs = exectools.NewManager(s.runner)
	if cfg.MaxCallWaitSec > 0 {
		wait := time.Duration(cfg.MaxCallWaitSec) * time.Second
		s.runner.DetachAfter = wait
		s.runner.Procs = s.procs
		s.procs.MaxWait = wait
	}
	if cfg.HistoryDir != "" {
		h, err := history.Open(cfg.HistoryDir, cfg.HistoryDays)
		if err != nil {
			return nil, err
		}
		s.history = h
	}
	for _, a := range cfg.AllowedIPs {
		n, err := parseIPNet(a)
		if err != nil {
			return nil, err
		}
		s.nets = append(s.nets, n)
	}
	if cfg.AuditLog != "" {
		f, err := os.OpenFile(cfg.AuditLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open audit log: %w", err)
		}
		s.audit = f
	}
	s.mcp = mcp.NewServer("mcp-shell", Version, s.instructions())
	s.registerTools()
	return s, nil
}

// Roots returns the resolved roots, for the startup banner.
func (s *Server) Roots() []string { return s.guard.Roots() }

// Handler returns the HTTP handler: /mcp (authenticated) and /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", s.authenticate(s.mcp))
	mux.Handle("/mcp/", s.authenticate(s.mcp))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "ok mcp-shell %s\n", Version)
	})
	return mux
}

// Close stops background processes and closes the audit log.
func (s *Server) Close() {
	s.procs.StopAll()
	if s.audit != nil {
		s.audit.Close()
	}
}

func (s *Server) instructions() string {
	scope := "Restricted mode: every file path and command working directory must be inside a configured root."
	if s.cfg.Unrestricted {
		scope = "\nUNRESTRICTED MODE: all paths accessible to the service's OS user are permitted, including outside the configured roots. Roots only select the default working directory. All tools execute immediately without host-side approval.\n"
	}
	return fmt.Sprintf(`mcp-shell gives you access to a %s host machine.
- %s
- Configured directories: %s. Relative paths use the first directory. Prefer absolute paths on this host.%s
- Start with list_roots (shell, scratch directory, which tools are on PATH). system_info shows OS, memory, disks and tool versions; get_env shows environment variables (PATH one entry per line).
- Read before you edit: use read_file, then edit_file with an exact, unique old_string, or multi_edit for several changes to one file. Use write_file only for new files or full rewrites.
- run_command runs one command and waits (default shell: %s, default timeout %ds). Use it for builds, tests, git and package managers.%s
- For dev servers or anything that keeps running, use start_process, then read_process_output and stop_process when done. read_process_output can wait until_exit, or wait_for a regexp such as "listening on"; tail=true shows the end of long output.
- Commands are non-interactive: stdin is empty, so pass flags like -y/--yes instead of waiting for prompts.
- Long output is cut in the middle; the full log is kept on disk (full_output: in the result) and can be read with read_file or grep.
- glob and grep skip files ignored by .gitignore (build output such as dist/); pass include_ignored=true to search them.%s%s`,
		runtime.GOOS, scope, strings.Join(s.guard.Roots(), ", "), s.aliasHint(), s.cfg.DefaultShell, s.cfg.CommandTimeoutSec,
		s.detachHint(), s.shellHint(), s.historyHint())
}

func (s *Server) detachHint() string {
	if s.runner.DetachAfter <= 0 {
		return ""
	}
	return fmt.Sprintf(" A command still running after %s is not killed: the call returns with exit_code none and the command continues as a background process; follow the next: line (read_process_output with until_exit=true) to get its result.", s.runner.DetachAfter)
}

func (s *Server) shellHint() string {
	if s.cfg.DefaultShell != "powershell" {
		return ""
	}
	return "\n- The default shell is Windows PowerShell 5.1: there is no && or || (use ; and check if ($LASTEXITCODE -ne 0) { ... }), and do not add 2>&1 to native commands (stderr is already returned separately). File cmdlets (Get-Content, Set-Content, Out-File and >) use UTF-8 here; use write_file when the exact bytes matter. A .ps1 file you write and then run must start with a UTF-8 BOM if it contains non-ASCII text, or PowerShell 5.1 misreads it; prefer passing the script as the run_command command."
}

func (s *Server) historyHint() string {
	if s.history == nil {
		return ""
	}
	return "\n- operation_history lists earlier tool calls on this host, including those of other sessions (filter by tool, text, failed_only, since; pass id for the details of one call). Check it before redoing work or when a change is unexplained."
}

func (s *Server) aliasHint() string {
	if len(s.cfg.PathAliases) == 0 {
		return ""
	}
	var parts []string
	for from, to := range s.cfg.PathAliases {
		parts = append(parts, from+" -> "+to)
	}
	return "\n- Client-side paths are translated: " + strings.Join(parts, "; ") + "."
}

// ---------- authentication ----------

func (s *Server) authenticate(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.cfg.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r)
		if !s.ipAllowed(ip) {
			s.auditEvent(map[string]any{"event": "denied", "reason": "ip not allowed", "ip": ip})
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			s.auditEvent(map[string]any{"event": "denied", "reason": "bad token", "ip": ip})
			w.Header().Set("WWW-Authenticate", `Bearer realm="mcp-shell"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(withClient(r.Context(), clientLabel(ip, r.UserAgent()))))
	})
}

func (s *Server) ipAllowed(ip string) bool {
	if len(s.nets) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range s.nets {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func parseIPNet(s string) (*net.IPNet, error) {
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("allowed_ips: %w", err)
		}
		return n, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("allowed_ips: invalid IP %q", s)
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}

// ---------- audit ----------

func (s *Server) auditEvent(ev map[string]any) {
	ev["time"] = time.Now().Format(time.RFC3339Nano)
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if ev["event"] == "denied" {
		reason := ev["reason"]
		if reason == "bad token" {
			reason = "访问令牌不匹配"
		}
		if reason == "ip not allowed" {
			reason = "来源 IP 不在允许范围"
		}
		log.Printf("[认证] 拒绝连接 来源=%v 原因=%v", ev["ip"], reason)
	}
	if s.audit == nil {
		return
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	s.audit.Write(append(line, '\n'))
}

// summarize turns tool input into an audit-friendly map: large text fields
// are replaced by their length, values of env are hidden and long strings
// are clipped.
func summarize(in any) map[string]any {
	return summarizeArgs(in, 500)
}

// summarizeArgs is summarize with strings clipped at max bytes.
func summarizeArgs(in any, max int) map[string]any {
	raw, _ := json.Marshal(in)
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	summarizeMap(m, max)
	return m
}

func summarizeMap(m map[string]any, max int) {
	for k, v := range m {
		switch val := v.(type) {
		case string:
			switch k {
			case "content", "old_string", "new_string":
				m[k] = fmt.Sprintf("<%d bytes>", len(val))
			default:
				m[k] = clipBytes(val, max)
			}
		case map[string]any:
			if k == "env" {
				for name := range val {
					val[name] = "<hidden>"
				}
				continue
			}
			summarizeMap(val, max)
		case []any:
			for _, item := range val {
				if child, ok := item.(map[string]any); ok {
					summarizeMap(child, max)
				}
			}
		}
	}
}

func clipBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// ---------- tool plumbing ----------

// addTool registers a tool whose handler returns plain text. Errors become
// tool results with isError set, so the model sees the message and can react.
// Every call is written to the operation log, the audit log and the history.
func addTool[In any](s *Server, name, desc string, fn func(ctx context.Context, in In) (string, error)) {
	s.mcp.AddTool(mcp.Typed(name, desc, func(ctx context.Context, in In) (string, error) {
		start := time.Now()
		log.Printf("[操作开始] %s (%s) %s", toolLabel(name), name, describeArgs(in))
		out, err := fn(ctx, in)
		elapsed := time.Since(start)
		s.logCall(name, in, out, err, elapsed)
		s.record(ctx, name, in, out, err, start, elapsed)
		ev := map[string]any{
			"event":       "tool",
			"tool":        name,
			"args":        summarize(in),
			"duration_ms": elapsed.Milliseconds(),
			"ok":          err == nil,
		}
		if err != nil {
			ev["error"] = err.Error()
		}
		s.auditEvent(ev)
		if err != nil {
			return "", err
		}
		return capResult(out, s.cfg.MaxResultBytes), nil
	}))
}

// capResult keeps a tool result below max bytes so the client does not reject
// it; tools that can produce more output say how to page through it.
func capResult(out string, max int) string {
	if max <= 0 || len(out) <= max {
		return out
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut] + fmt.Sprintf("\n…[result cut at %d of %d bytes; narrow the request or page with offset/limit]\n", cut, len(out))
}
