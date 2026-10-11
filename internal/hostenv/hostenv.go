// Package hostenv builds the environment commands run with and finds
// executables on it. On Windows it folds in PATH entries and variables that
// were added to the user or machine environment after mcp-shell started (an
// installer, setx), so new tools work without restarting mcp-shell.
package hostenv

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Diff is what Environ added to mcp-shell's own environment.
type Diff struct {
	// PathAdded are registry PATH entries the process PATH lacked.
	PathAdded []string
	// VarsAdded are names of registry variables the process lacked.
	VarsAdded []string
}

// Environ returns the environment for a new command and what it added to
// os.Environ(). Elsewhere than Windows it is os.Environ() unchanged.
func Environ() ([]string, Diff) {
	machine, _ := Scope("machine")
	user, _ := Scope("user")
	return merge(os.Environ(), machine, user)
}

// merge adds to base the variables of the scopes that base lacks and appends
// to PATH the scope PATH entries base lacks, machine before user, the order
// Windows itself builds a new logon environment in. Values already in base
// win: they are what mcp-shell was started with on purpose.
func merge(base []string, scopes ...map[string]string) ([]string, Diff) {
	var diff Diff
	out := append([]string(nil), base...)
	index := map[string]int{}
	pathKey := ""
	for i, kv := range out {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		index[envKey(k)] = i
		if envKey(k) == envKey("PATH") {
			pathKey = k
		}
	}
	var pathEntries []string
	if i, ok := index[envKey("PATH")]; ok {
		_, v, _ := strings.Cut(out[i], "=")
		pathEntries = SplitList(v)
	}
	seen := map[string]bool{}
	for _, e := range pathEntries {
		seen[pathEntryKey(e)] = true
	}
	for _, scope := range scopes {
		names := make([]string, 0, len(scope))
		for k := range scope {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			v := scope[k]
			if envKey(k) == envKey("PATH") {
				if pathKey == "" {
					pathKey = k
				}
				for _, e := range SplitList(v) {
					if key := pathEntryKey(e); !seen[key] {
						seen[key] = true
						pathEntries = append(pathEntries, e)
						diff.PathAdded = append(diff.PathAdded, e)
					}
				}
				continue
			}
			if _, ok := index[envKey(k)]; ok {
				continue
			}
			index[envKey(k)] = len(out)
			out = append(out, k+"="+v)
			diff.VarsAdded = append(diff.VarsAdded, k)
		}
	}
	if len(diff.PathAdded) > 0 {
		kv := pathKey + "=" + strings.Join(pathEntries, string(os.PathListSeparator))
		if i, ok := index[envKey("PATH")]; ok {
			out[i] = kv
		} else {
			out = append(out, kv)
		}
	}
	return out, diff
}

// Get returns the value of name in env (an os.Environ()-style list).
func Get(env []string, name string) (string, bool) {
	want := envKey(name)
	for i := len(env) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(env[i], "=")
		if ok && envKey(k) == want {
			return v, true
		}
	}
	return "", false
}

// SplitList splits a PATH-like value and drops empty entries.
func SplitList(v string) []string {
	var out []string
	for _, e := range filepath.SplitList(v) {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// LookPath finds an executable on the PATH of env, trying PATHEXT on Windows.
func LookPath(name string, env []string) string {
	path, _ := Get(env, "PATH")
	var exts []string
	if runtime.GOOS == "windows" {
		pathext, ok := Get(env, "PATHEXT")
		if !ok {
			pathext = ".COM;.EXE;.BAT;.CMD"
		}
		if filepath.Ext(name) == "" {
			for _, e := range strings.Split(strings.ToLower(pathext), ";") {
				if e != "" {
					exts = append(exts, e)
				}
			}
		} else {
			exts = []string{""}
		}
	} else {
		exts = []string{""}
	}
	for _, dir := range SplitList(path) {
		for _, ext := range exts {
			p := filepath.Join(dir, name+ext)
			if st, err := os.Stat(p); err == nil && !st.IsDir() && (runtime.GOOS == "windows" || st.Mode()&0o111 != 0) {
				return p
			}
		}
	}
	return ""
}

func envKey(k string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(k)
	}
	return k
}

func pathEntryKey(e string) string {
	e = strings.TrimRight(strings.TrimSpace(e), `\/`)
	if runtime.GOOS == "windows" {
		e = strings.ToLower(e)
	}
	return e
}
