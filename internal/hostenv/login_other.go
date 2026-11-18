//go:build !windows

package hostenv

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A GUI app or a launch agent starts mcp-shell without what the user's
// shell profile adds (Homebrew, gvm, nvm, cargo), so commands could not find
// go or cargo. loginScope reads the environment of a login shell of the user
// and Environ adds what mcp-shell's own environment lacks.

const (
	loginEnvTTL     = 5 * time.Minute
	loginEnvTimeout = 5 * time.Second
	loginEnvMarker  = "__MCP_SHELL_LOGIN_ENV__"
)

// loginEnvSkip are variables of the shell session itself, not of the user's
// setup; colour switches would put escape codes into command output.
var loginEnvSkip = map[string]bool{
	"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true, "TERM": true,
	"TERM_PROGRAM": true, "TERM_PROGRAM_VERSION": true, "TERM_SESSION_ID": true,
	"CLICOLOR": true, "CLICOLOR_FORCE": true, "FORCE_COLOR": true, "PS1": true,
}

var loginCache struct {
	mu   sync.Mutex
	vars map[string]string
	at   time.Time
}

// loginScope returns the variables of a login shell, read at most every
// loginEnvTTL. MCP_SHELL_LOGIN_ENV=0 turns it off; a shell that fails or
// hangs gives an empty scope.
func loginScope() map[string]string {
	if os.Getenv("MCP_SHELL_LOGIN_ENV") == "0" {
		return nil
	}
	loginCache.mu.Lock()
	defer loginCache.mu.Unlock()
	if loginCache.vars != nil && time.Since(loginCache.at) < loginEnvTTL {
		return loginCache.vars
	}
	vars := readLoginEnv()
	if vars == nil {
		vars = map[string]string{}
	}
	loginCache.vars, loginCache.at = vars, time.Now()
	return vars
}

func loginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		switch filepath.Base(sh) {
		case "zsh", "bash", "sh":
			return sh
		}
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	return "/bin/bash"
}

func readLoginEnv() map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), loginEnvTimeout)
	defer cancel()
	// -i also reads ~/.zshrc / ~/.bashrc, where version managers usually
	// hook in; the marker skips whatever the rc files print.
	cmd := exec.CommandContext(ctx, loginShell(), "-l", "-i", "-c", "printf '%s' "+loginEnvMarker+"; env -0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	return parseLoginEnv(out)
}

// parseLoginEnv reads the NUL-separated output of env -0 after the marker.
func parseLoginEnv(out []byte) map[string]string {
	i := bytes.LastIndex(out, []byte(loginEnvMarker))
	if i < 0 {
		return nil
	}
	vars := map[string]string{}
	for _, kv := range bytes.Split(out[i+len(loginEnvMarker):], []byte{0}) {
		k, v, ok := strings.Cut(string(kv), "=")
		if !ok || k == "" || strings.ContainsAny(k, "\n ") || loginEnvSkip[k] {
			continue
		}
		vars[k] = v
	}
	return vars
}

// withUTF8Locale sets LANG when no locale variable is set: in the C locale
// macOS tools print non-ASCII file names as "?".
func withUTF8Locale(env []string, diff *Diff) []string {
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v, ok := Get(env, name); ok && v != "" {
			return env
		}
	}
	lang := "C.UTF-8"
	if runtime.GOOS == "darwin" {
		lang = "en_US.UTF-8"
	}
	diff.VarsAdded = append(diff.VarsAdded, "LANG")
	return append(env, "LANG="+lang)
}

// addedFrom names where Environ's additions come from.
const addedFrom = "the login shell"
