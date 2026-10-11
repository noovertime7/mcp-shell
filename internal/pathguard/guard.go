// Package pathguard confines every path a client sends to a fixed set of root
// directories. It is the main safety boundary of the file tools.
package pathguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const isWindows = runtime.GOOS == "windows"

// Guard resolves client paths and rejects anything outside its roots.
type Guard struct {
	roots   []string // real (symlink-free) absolute paths
	aliases []alias  // longest prefix first
}

type alias struct {
	from string // client prefix, forward slashes, no trailing slash
	to   string // host path
}

// New builds a Guard. Every root must exist and be a directory.
func New(roots []string, aliases map[string]string) (*Guard, error) {
	g := &Guard{}
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("root %q: %w", r, err)
		}
		real, err := realPath(abs)
		if err != nil {
			return nil, fmt.Errorf("root %q: %w", r, err)
		}
		st, err := os.Stat(real)
		if err != nil {
			return nil, fmt.Errorf("root %q: %w", r, err)
		}
		if !st.IsDir() {
			return nil, fmt.Errorf("root %q is not a directory", r)
		}
		g.roots = append(g.roots, filepath.Clean(real))
	}
	if len(g.roots) == 0 {
		return nil, errors.New("no roots configured")
	}
	for from, to := range aliases {
		from = strings.TrimRight(strings.ReplaceAll(from, `\`, "/"), "/")
		if from == "" || to == "" {
			continue
		}
		g.aliases = append(g.aliases, alias{from: from, to: to})
	}
	sort.Slice(g.aliases, func(i, j int) bool { return len(g.aliases[i].from) > len(g.aliases[j].from) })
	return g, nil
}

// Roots returns the resolved root directories.
func (g *Guard) Roots() []string {
	return append([]string(nil), g.roots...)
}

// IsRoot reports whether p (already resolved) is one of the roots itself.
func (g *Guard) IsRoot(p string) bool {
	for _, r := range g.roots {
		if samePath(r, p) {
			return true
		}
	}
	return false
}

// Resolve turns a client path into a real absolute host path inside a root.
// Relative paths are taken relative to the first root. Symlinks and junctions
// in the existing part of the path are resolved before the check, so a link
// inside a root that points outside it is rejected.
func (g *Guard) Resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is required")
	}
	orig := p
	p = g.applyAlias(p)

	if strings.HasPrefix(p, `\\`) || (isWindows && strings.HasPrefix(p, "//")) {
		return "", fmt.Errorf("UNC and device paths are not allowed: %s", orig)
	}
	if isWindows {
		vol := filepath.VolumeName(p)
		if vol != "" && !filepath.IsAbs(p) {
			return "", fmt.Errorf("drive-relative paths are not allowed, use a full path like %s\\...: %s", vol, orig)
		}
		if vol == "" && (strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`)) {
			return "", fmt.Errorf("path %s has no drive letter; use a full Windows path (e.g. %s) or a path relative to it", orig, g.roots[0])
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(g.roots[0], p)
	}
	p = filepath.Clean(p)

	real, err := resolveExisting(p)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", orig, err)
	}
	for _, r := range g.roots {
		if within(r, real) {
			return real, nil
		}
	}
	return "", fmt.Errorf("path outside roots: %s (allowed roots: %s)", orig, strings.Join(g.roots, ", "))
}

func (g *Guard) applyAlias(p string) string {
	norm := strings.ReplaceAll(p, `\`, "/")
	for _, a := range g.aliases {
		if norm == a.from || strings.HasPrefix(norm, a.from+"/") {
			return filepath.Join(a.to, filepath.FromSlash(strings.TrimPrefix(norm, a.from)))
		}
	}
	return p
}

// ToClient maps a host path back to the client's view through the path
// aliases (the inverse of applyAlias), so the client can open it with its
// own tools. Paths outside every alias are returned unchanged.
func (g *Guard) ToClient(p string) string {
	p = filepath.Clean(p)
	for _, a := range g.aliases {
		to := filepath.Clean(a.to)
		if len(p) < len(to) || !samePath(p[:len(to)], to) {
			continue
		}
		rest := p[len(to):]
		if rest == "" {
			return a.from
		}
		if rest[0] != filepath.Separator && !strings.HasSuffix(to, string(filepath.Separator)) {
			continue // e.g. D:\ws-other is not under D:\ws
		}
		return a.from + "/" + strings.TrimLeft(filepath.ToSlash(rest), "/")
	}
	return p
}

// resolveExisting resolves links in the deepest existing ancestor of p and
// re-appends the components that do not exist yet.
func resolveExisting(p string) (string, error) {
	cur := p
	var rest []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			real, err := realPath(cur)
			if err != nil {
				return "", err
			}
			for i := len(rest) - 1; i >= 0; i-- {
				real = filepath.Join(real, rest[i])
			}
			return filepath.Clean(real), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

func within(root, p string) bool {
	if isWindows {
		root, p = strings.ToLower(root), strings.ToLower(p)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if isWindows {
		return strings.EqualFold(a, b)
	}
	return a == b
}
