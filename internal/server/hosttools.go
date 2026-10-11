package server

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/noovertime7/mcp-shell/internal/exectools"
	"github.com/noovertime7/mcp-shell/internal/hostenv"
	"github.com/noovertime7/mcp-shell/internal/sysinfo"
)

// systemInfo is the system_info tool.
func (s *Server) systemInfo(ctx context.Context) string {
	host := sysinfo.Describe(ctx, s.guard.Roots())
	tools := sysinfo.ProbeTools(ctx)
	var b strings.Builder
	b.WriteString(host.String(tools))
	fmt.Fprintf(&b, "default_shell: %s\n", s.cfg.DefaultShell)
	env, diff := exectools.CommandEnviron()
	path, _ := hostenv.Get(env, "PATH")
	entries := hostenv.SplitList(path)
	missing := 0
	for _, e := range entries {
		if st, err := os.Stat(e); err != nil || !st.IsDir() {
			missing++
		}
	}
	fmt.Fprintf(&b, "PATH: %d entries", len(entries))
	if missing > 0 {
		fmt.Fprintf(&b, ", %d of them missing", missing)
	}
	if n := len(diff.PathAdded); n > 0 {
		fmt.Fprintf(&b, ", %d added in the registry after mcp-shell started (commands get them)", n)
	}
	b.WriteString(" — get_env name=PATH lists them\n")
	return b.String()
}

type getEnvIn struct {
	Name        string `json:"name,omitempty" jsonschema:"one variable, e.g. PATH (case-insensitive on Windows); PATH-like values are listed one entry per line"`
	Filter      string `json:"filter,omitempty" jsonschema:"only variables whose name or value contains this text (case-insensitive)"`
	Scope       string `json:"scope,omitempty" jsonschema:"process (default: what commands run with), user or machine (the values stored in the Windows registry)"`
	ShowSecrets bool   `json:"show_secrets,omitempty" jsonschema:"show values of variables that look like secrets (tokens, passwords, keys); hidden by default"`
}

// getEnv is the get_env tool.
func (s *Server) getEnv(in getEnvIn) (string, error) {
	scope := strings.ToLower(strings.TrimSpace(in.Scope))
	if scope == "" {
		scope = "process"
	}
	vars := map[string]string{}
	var diff hostenv.Diff
	switch scope {
	case "process":
		var env []string
		env, diff = exectools.CommandEnviron()
		for _, kv := range env {
			if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
				vars[k] = v
			}
		}
	default:
		m, err := hostenv.Scope(scope)
		if err != nil {
			return "", err
		}
		vars = m
	}

	if in.Name != "" {
		for k, v := range vars {
			if sameEnvName(k, in.Name) {
				return s.describeVar(k, v, scope, diff, in.ShowSecrets), nil
			}
		}
		return fmt.Sprintf("%s is not set (scope %s)", in.Name, scope), nil
	}

	names := make([]string, 0, len(vars))
	filter := strings.ToLower(in.Filter)
	for k, v := range vars {
		if filter != "" && !strings.Contains(strings.ToLower(k), filter) && !strings.Contains(strings.ToLower(v), filter) {
			continue
		}
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	var b strings.Builder
	fmt.Fprintf(&b, "scope: %s, %d variables", scope, len(names))
	if filter != "" {
		fmt.Fprintf(&b, " matching %q", in.Filter)
	}
	b.WriteString("\n")
	for _, k := range names {
		v := vars[k]
		switch {
		case s.hideValue(k, v, in.ShowSecrets):
			v = hiddenNote(v)
		case isListVar(k) && len(hostenv.SplitList(v)) > 1:
			v = fmt.Sprintf("<%d entries; get_env name=%s lists them>", len(hostenv.SplitList(v)), k)
		default:
			v = clipBytes(v, 300)
		}
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	if scope == "process" && (len(diff.PathAdded) > 0 || len(diff.VarsAdded) > 0) {
		fmt.Fprintf(&b, "note: commands get mcp-shell's own environment plus %d PATH entries and %d variables set in the registry after it started\n", len(diff.PathAdded), len(diff.VarsAdded))
	}
	return b.String(), nil
}

func (s *Server) describeVar(name, value, scope string, diff hostenv.Diff, showSecrets bool) string {
	if s.hideValue(name, value, showSecrets) {
		return fmt.Sprintf("%s=%s", name, hiddenNote(value))
	}
	entries := hostenv.SplitList(value)
	if !isListVar(name) || len(entries) < 2 {
		return fmt.Sprintf("%s=%s", name, value)
	}
	added := map[string]bool{}
	if sameEnvName(name, "PATH") {
		for _, e := range diff.PathAdded {
			added[e] = true
		}
	}
	isDirs := !sameEnvName(name, "PATHEXT")
	var b strings.Builder
	fmt.Fprintf(&b, "%s (scope %s, %d entries, in search order):\n", name, scope, len(entries))
	for i, e := range entries {
		fmt.Fprintf(&b, "%3d  %s", i+1, e)
		if isDirs {
			if st, err := os.Stat(e); err != nil || !st.IsDir() {
				b.WriteString("  [missing]")
			}
		}
		if added[e] {
			b.WriteString("  [added in the registry after mcp-shell started]")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// hideValue reports whether a value must not be shown. mcp-shell's own
// token is never shown.
func (s *Server) hideValue(name, value string, showSecrets bool) bool {
	if sameEnvName(name, "MCP_SHELL_TOKEN") || (s.cfg.Token != "" && strings.Contains(value, s.cfg.Token)) {
		return true
	}
	return !showSecrets && looksSecret(name)
}

func hiddenNote(v string) string {
	return fmt.Sprintf("<hidden, %d characters; pass show_secrets=true to see it>", len(v))
}

var secretParts = []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "APIKEY", "API_KEY", "ACCESS_KEY", "ACCESSKEY", "PRIVATE_KEY", "AUTH_", "_AUTH", "COOKIE", "DSN"}

func looksSecret(name string) bool {
	n := strings.ToUpper(name)
	for _, p := range secretParts {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

// isListVar reports variables whose value is a list of paths.
func isListVar(name string) bool {
	n := strings.ToUpper(name)
	return n == "PATH" || n == "PATHEXT" || strings.HasSuffix(n, "PATH") || strings.HasSuffix(n, "_DIRS")
}

func sameEnvName(a, b string) bool {
	if os.PathListSeparator == ';' { // Windows: names are case-insensitive
		return strings.EqualFold(a, b)
	}
	return a == b
}
