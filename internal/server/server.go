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
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/noovertime7/mcp-shell/internal/config"
	"github.com/noovertime7/mcp-shell/internal/exectools"
	"github.com/noovertime7/mcp-shell/internal/fstools"
	"github.com/noovertime7/mcp-shell/internal/mcp"
	"github.com/noovertime7/mcp-shell/internal/pathguard"
)

// Version is reported to MCP clients.
const Version = "0.1.0"

// Server is the mcp-shell server.
type Server struct {
	cfg    *config.Config
	guard  *pathguard.Guard
	fs     *fstools.FS
	runner *exectools.Runner
	procs  *exectools.Manager
	mcp    *mcp.Server
	nets   []*net.IPNet

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
	}
	s.procs = exectools.NewManager(s.runner)
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
- Read before you edit: use read_file, then edit_file with an exact, unique old_string. Use write_file only for new files or full rewrites.
- run_command runs one command and waits (default shell: %s, default timeout %ds). Use it for builds, tests, git and package managers.
- For dev servers or anything that keeps running, use start_process, then read_process_output (with wait_ms) and stop_process when done.
- Commands are non-interactive: stdin is empty, so pass flags like -y/--yes instead of waiting for prompts.
- Long output is cut in the middle; the full log is kept on disk (full_output: in the result) and can be read with read_file or grep.`,
		runtime.GOOS, scope, strings.Join(s.guard.Roots(), ", "), s.aliasHint(), s.cfg.DefaultShell, s.cfg.CommandTimeoutSec)
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
		next.ServeHTTP(w, r)
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
// are replaced by their length and long strings are clipped.
func summarize(in any) map[string]any {
	raw, _ := json.Marshal(in)
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	for k, v := range m {
		str, ok := v.(string)
		if !ok {
			continue
		}
		switch k {
		case "content", "old_string", "new_string":
			m[k] = fmt.Sprintf("<%d bytes>", len(str))
		default:
			if len(str) > 500 {
				m[k] = str[:500] + "…"
			}
		}
	}
	return m
}

// ---------- tool plumbing ----------

// addTool registers a tool whose handler returns plain text. Errors become
// tool results with isError set, so the model sees the message and can react.
// Every call is written to the operation log and the audit log.
func addTool[In any](s *Server, name, desc string, fn func(ctx context.Context, in In) (string, error)) {
	s.mcp.AddTool(mcp.Typed(name, desc, func(ctx context.Context, in In) (string, error) {
		start := time.Now()
		log.Printf("[操作开始] %s (%s) %s", toolLabel(name), name, describeArgs(in))
		out, err := fn(ctx, in)
		elapsed := time.Since(start)
		s.logCall(name, in, out, err, elapsed)
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
