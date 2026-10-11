// Package config loads and validates the mcp-shell JSON configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// defaultShell is powershell on Windows, zsh on macOS and bash elsewhere.
func defaultShell() string {
	switch runtime.GOOS {
	case "windows":
		return "powershell"
	case "darwin":
		return "zsh"
	default:
		return "bash"
	}
}

// Config is the on-disk configuration of mcp-shell.
type Config struct {
	// Unrestricted removes application-level file scope checks (OS permissions still apply).
	Unrestricted bool `json:"unrestricted"`
	// Listen is the TCP address to bind, e.g. "192.168.1.10:8765".
	Listen string `json:"listen"`
	// Token is the shared bearer token. MCP_SHELL_TOKEN overrides it.
	Token string `json:"token"`
	// AllowedIPs lists client IPs or CIDRs. Empty means any client with the token.
	AllowedIPs []string `json:"allowed_ips"`
	// Roots are the directories the file tools and command cwd are confined to.
	Roots []string `json:"roots"`
	// PathAliases maps client-side path prefixes (e.g. the VM mount
	// "/mnt/hgfs/ws") to host paths (e.g. "D:\\ws").
	PathAliases map[string]string `json:"path_aliases"`
	// DefaultShell is "powershell", "pwsh" or "cmd".
	DefaultShell string `json:"default_shell"`
	// CommandTimeoutSec is the default run_command timeout.
	CommandTimeoutSec int `json:"command_timeout_sec"`
	// MaxCommandTimeoutSec caps the timeout a caller may request.
	MaxCommandTimeoutSec int `json:"max_command_timeout_sec"`
	// MaxOutputBytes caps captured stdout and stderr, each.
	MaxOutputBytes int `json:"max_output_bytes"`
	// MaxReadBytes caps the bytes read_file and grep return in one call.
	MaxReadBytes int `json:"max_read_bytes"`
	// MaxResultBytes caps any single tool result. Claude Code rejects MCP
	// results above roughly 25k tokens (MAX_MCP_OUTPUT_TOKENS), so keep this
	// well below that.
	MaxResultBytes int `json:"max_result_bytes"`
	// OutputDir keeps the full output of truncated commands and of background
	// processes. Defaults to <first root>\.tmp\mcp-shell; "-" disables it.
	OutputDir string `json:"output_dir"`
	// AuditLog is the JSON-lines audit file. Empty disables auditing.
	AuditLog string `json:"audit_log"`
	// LogFile receives the human-readable operation log (every tool call,
	// what it changed, connections, denials) in addition to the console.
	// Empty means console only.
	LogFile string `json:"log_file"`
	// HistoryDir keeps every tool call, one JSON-lines file per day, for the
	// operation_history tool. Defaults to <output_dir>/history (or
	// <first root>/.tmp/mcp-shell/history); "-" disables it.
	HistoryDir string `json:"history_dir"`
	// HistoryDays is how many days of history are kept (default 30).
	HistoryDays int `json:"history_days"`
	// MaxCallWaitSec is the longest one tool call waits before it returns
	// (default 45): run_command hands a command still running after this
	// long to the background, and read_process_output waits at most this
	// long. Keep it below the client's and any proxy's request timeout.
	// A negative value turns the hand-over off.
	MaxCallWaitSec int `json:"max_call_wait_sec"`
}

// Load reads path, applies defaults and the MCP_SHELL_TOKEN override, and validates.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	// Tolerate a UTF-8 BOM, which Notepad on Windows likes to add.
	data = []byte(strings.TrimPrefix(string(data), "\ufeff"))

	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if tok := os.Getenv("MCP_SHELL_TOKEN"); tok != "" {
		c.Token = tok
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8765"
	}
	if c.DefaultShell == "" {
		c.DefaultShell = defaultShell()
	}
	if c.CommandTimeoutSec <= 0 {
		c.CommandTimeoutSec = 600
	}
	if c.MaxCommandTimeoutSec <= 0 {
		c.MaxCommandTimeoutSec = 3600
	}
	if c.CommandTimeoutSec > c.MaxCommandTimeoutSec {
		c.CommandTimeoutSec = c.MaxCommandTimeoutSec
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = 25_000
	}
	if c.MaxReadBytes <= 0 {
		c.MaxReadBytes = 60_000
	}
	if c.MaxResultBytes <= 0 {
		c.MaxResultBytes = 60_000
	}
	if c.OutputDir == "" && len(c.Roots) > 0 {
		c.OutputDir = filepath.Join(c.Roots[0], ".tmp", "mcp-shell")
	}
	if c.HistoryDir == "" && len(c.Roots) > 0 {
		base := c.OutputDir
		if base == "-" || base == "" {
			base = filepath.Join(c.Roots[0], ".tmp", "mcp-shell")
		}
		c.HistoryDir = filepath.Join(base, "history")
	}
	if c.HistoryDir == "-" {
		c.HistoryDir = ""
	}
	if c.HistoryDays <= 0 {
		c.HistoryDays = 30
	}
	if c.MaxCallWaitSec == 0 {
		c.MaxCallWaitSec = 45
	}
	if c.OutputDir == "-" {
		c.OutputDir = ""
	}
}

func (c *Config) validate() error {
	if len(c.Token) < 16 {
		return errors.New("config: token must be set (at least 16 characters); set \"token\" or MCP_SHELL_TOKEN")
	}
	if strings.Contains(c.Token, "change-me") {
		return errors.New("config: token is still the example placeholder; generate one with .\\start.ps1 -NewToken")
	}
	if len(c.Roots) == 0 {
		return errors.New("config: roots must list at least one directory")
	}
	for _, r := range c.Roots {
		st, err := os.Stat(r)
		if err != nil {
			return fmt.Errorf("config: root %q: %w", r, err)
		}
		if !st.IsDir() {
			return fmt.Errorf("config: root %q is not a directory", r)
		}
	}
	switch c.DefaultShell {
	case "powershell", "pwsh", "cmd", "sh", "bash", "zsh":
	default:
		return fmt.Errorf("config: unknown default_shell %q", c.DefaultShell)
	}
	return nil
}
