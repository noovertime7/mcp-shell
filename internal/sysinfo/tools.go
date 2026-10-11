// Package sysinfo describes the host for the system_info and list_roots
// tools: OS, memory, disks and which developer tools are on PATH.
package sysinfo

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/noovertime7/mcp-shell/internal/hostenv"
)

// Tool is one developer tool looked up on PATH.
type Tool struct {
	Name    string
	Path    string // empty when not found
	Version string // first line of its version output, when probed
}

type probe struct {
	name string
	args []string
}

// probes are the tools worth knowing about before running a build.
var probes = []probe{
	{"git", []string{"--version"}},
	{"go", []string{"version"}},
	{"node", []string{"--version"}},
	{"npm", []string{"--version"}},
	{"pnpm", []string{"--version"}},
	{"yarn", []string{"--version"}},
	{"python", []string{"--version"}},
	{"python3", []string{"--version"}},
	{"java", []string{"-version"}},
	{"mvn", []string{"-v"}},
	{"docker", []string{"--version"}},
	{"cargo", []string{"--version"}},
	{"dotnet", []string{"--version"}},
	{"gh", []string{"--version"}},
	{"pwsh", []string{"-NoLogo", "-NoProfile", "-Command", "$PSVersionTable.PSVersion.ToString()"}},
	{"powershell", []string{"-NoLogo", "-NoProfile", "-Command", "$PSVersionTable.PSVersion.ToString()"}},
}

const versionTimeout = 8 * time.Second

var (
	cacheMu   sync.Mutex
	cached    []Tool
	cachedAt  time.Time
	cacheLife = 2 * time.Minute
)

// FindTools looks the tools up on the PATH commands get, without running
// them. It is cheap enough for every list_roots call.
func FindTools() []Tool {
	env, _ := hostenv.Environ()
	out := make([]Tool, 0, len(probes))
	for _, p := range probes {
		if runtime.GOOS != "windows" && p.name == "powershell" {
			continue
		}
		out = append(out, Tool{Name: p.name, Path: hostenv.LookPath(p.name, env)})
	}
	return out
}

// ProbeTools also runs each found tool for its version, in parallel. The
// result is cached for a short while.
func ProbeTools(ctx context.Context) []Tool {
	cacheMu.Lock()
	if cached != nil && time.Since(cachedAt) < cacheLife {
		out := append([]Tool(nil), cached...)
		cacheMu.Unlock()
		return out
	}
	cacheMu.Unlock()

	env, _ := hostenv.Environ()
	tools := FindTools()
	var wg sync.WaitGroup
	for i := range tools {
		if tools[i].Path == "" {
			continue
		}
		wg.Add(1)
		go func(t *Tool) {
			defer wg.Done()
			t.Version = version(ctx, t, env)
		}(&tools[i])
	}
	wg.Wait()

	cacheMu.Lock()
	cached, cachedAt = append([]Tool(nil), tools...), time.Now()
	cacheMu.Unlock()
	return tools
}

func version(ctx context.Context, t *Tool, env []string) string {
	var args []string
	for _, p := range probes {
		if p.name == t.Name {
			args = p.args
		}
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	var cmd *exec.Cmd
	ext := strings.ToLower(filepath.Ext(t.Path))
	if runtime.GOOS == "windows" && (ext == ".cmd" || ext == ".bat") {
		cmd = exec.CommandContext(ctx, "cmd.exe", append([]string{"/d", "/c", t.Path}, args...)...)
	} else {
		cmd = exec.CommandContext(ctx, t.Path, args...)
	}
	cmd.Env = env
	hideWindow(cmd)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	line := firstLine(out.String())
	if ctx.Err() != nil {
		return "(no answer within " + versionTimeout.String() + ")"
	}
	if err != nil {
		if line == "" {
			line = firstLine(err.Error())
		}
		// e.g. dotnet without an SDK prints an error instead of a version
		return "(not working: " + line + ")"
	}
	return line
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > 100 {
				l = string(r[:100]) + "…"
			}
			return l
		}
	}
	return ""
}

// Summary is a one-line list of found and missing tools.
func Summary(tools []Tool) string {
	var found, missing []string
	for _, t := range tools {
		if t.Path == "" {
			missing = append(missing, t.Name)
		} else {
			found = append(found, t.Name)
		}
	}
	s := "found " + strings.Join(found, ", ")
	if len(missing) > 0 {
		s += "; not on PATH: " + strings.Join(missing, ", ")
	}
	return s
}
