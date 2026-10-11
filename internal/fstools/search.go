package fstools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const (
	maxGlobResults   = 500
	maxGlobScan      = 20000
	maxGrepFileBytes = 10 << 20
	maxGrepLineChars = 500
)

// ---------- glob ----------

// Glob finds files under base whose slash-separated relative path matches
// pattern. "**" matches any number of directories and {a,b} alternatives are
// supported. Paths ignored by .gitignore are skipped unless includeIgnored.
// Results are sorted newest first.
func (f *FS) Glob(pattern, base string, includeIgnored bool) (string, error) {
	if pattern == "" {
		return "", errors.New("pattern is required")
	}
	if base == "" {
		base = "."
	}
	root, err := f.Guard.Resolve(base)
	if err != nil {
		return "", err
	}
	pattern = strings.TrimPrefix(strings.ReplaceAll(pattern, `\`, "/"), "./")
	if filepath.IsAbs(pattern) || filepath.VolumeName(pattern) != "" {
		return "", errors.New("pattern must be relative; put the directory in path")
	}
	patterns := expandBraces(pattern)
	if runtime.GOOS == "windows" {
		for i := range patterns {
			patterns[i] = strings.ToLower(patterns[i])
		}
	}
	wantSkipped := mentionsSkipped(pattern)
	ignore := newIgnoreWalker(root, includeIgnored)

	var hits []string
	scanned := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] && !wantSkipped {
				return filepath.SkipDir
			}
			if ignore.skip(p, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if ignore.skip(p, false) {
			return nil
		}
		scanned++
		if scanned > maxGlobScan {
			return errStop
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if runtime.GOOS == "windows" {
			rel = strings.ToLower(rel)
		}
		for _, pat := range patterns {
			if matchGlob(pat, rel) {
				hits = append(hits, p)
				break
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return "", err
	}
	if len(hits) == 0 {
		return fmt.Sprintf("no files match %q under %s%s", pattern, root, ignore.note()), nil
	}
	sortNewestFirst(hits)
	var b strings.Builder
	for i, h := range hits {
		if i == maxGlobResults {
			fmt.Fprintf(&b, "… %d more; narrow the pattern\n", len(hits)-maxGlobResults)
			break
		}
		b.WriteString(h)
		b.WriteByte('\n')
	}
	if scanned > maxGlobScan {
		fmt.Fprintf(&b, "… stopped after scanning %d files; use a more specific path\n", maxGlobScan)
	}
	return b.String(), nil
}

func matchGlob(pattern, rel string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

func matchSegs(p, s []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if matchSegs(p[1:], s[i:]) {
					return true
				}
			}
			return false
		}
		if len(s) == 0 {
			return false
		}
		if ok, err := path.Match(p[0], s[0]); err != nil || !ok {
			return false
		}
		p, s = p[1:], s[1:]
	}
	return len(s) == 0
}

// expandBraces turns "a.{go,ts}" into ["a.go", "a.ts"]. Nested braces work.
func expandBraces(p string) []string {
	open := strings.IndexByte(p, '{')
	if open < 0 {
		return []string{p}
	}
	depth := 0
	for i := open; i < len(p); i++ {
		switch p[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				var parts []string
				start, d := open+1, 0
				for j := open + 1; j < i; j++ {
					switch p[j] {
					case '{':
						d++
					case '}':
						d--
					case ',':
						if d == 0 {
							parts = append(parts, p[start:j])
							start = j + 1
						}
					}
				}
				parts = append(parts, p[start:i])
				var out []string
				for _, part := range parts {
					out = append(out, expandBraces(p[:open]+part+p[i+1:])...)
				}
				return out
			}
		}
	}
	return []string{p}
}

func mentionsSkipped(pattern string) bool {
	for _, seg := range strings.Split(pattern, "/") {
		if skipDirs[seg] {
			return true
		}
	}
	return false
}

func sortNewestFirst(paths []string) {
	times := make(map[string]int64, len(paths))
	for _, p := range paths {
		times[p] = modTime(p).UnixNano()
	}
	sort.SliceStable(paths, func(i, j int) bool { return times[paths[i]] > times[paths[j]] })
}

// ---------- grep ----------

// GrepOptions are the parameters of Grep.
type GrepOptions struct {
	Pattern    string
	Path       string
	Glob       string
	IgnoreCase bool
	Context    int
	MaxResults int
	FilesOnly  bool
	// IncludeIgnored also searches paths ignored by .gitignore.
	IncludeIgnored bool
}

// Grep searches files for a regular expression (Go RE2 syntax).
func (f *FS) Grep(o GrepOptions) (string, error) {
	if o.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	expr := o.Pattern
	if o.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return "", fmt.Errorf("invalid regexp (Go RE2 syntax): %w", err)
	}
	if o.Path == "" {
		o.Path = "."
	}
	root, err := f.Guard.Resolve(o.Path)
	if err != nil {
		return "", err
	}
	if o.MaxResults <= 0 {
		o.MaxResults = 200
	}
	if o.Context < 0 {
		o.Context = 0
	}
	if o.Context > 10 {
		o.Context = 10
	}
	var globs []string
	if o.Glob != "" {
		for _, g := range expandBraces(strings.ReplaceAll(o.Glob, `\`, "/")) {
			if runtime.GOOS == "windows" {
				g = strings.ToLower(g)
			}
			globs = append(globs, g)
		}
	}
	wantSkipped := o.Glob != "" && mentionsSkipped(o.Glob)

	var b strings.Builder
	matches, files := 0, 0
	budget := f.MaxReadBytes
	if budget <= 0 {
		budget = 60_000
	}
	overBudget := func() bool { return b.Len() >= budget }
	searchFile := func(p string) error {
		if globs != nil {
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			name := filepath.Base(p)
			if runtime.GOOS == "windows" {
				rel, name = strings.ToLower(rel), strings.ToLower(name)
			}
			ok := false
			for _, g := range globs {
				target := name
				if strings.Contains(g, "/") {
					target = rel
				}
				if matchGlob(g, target) {
					ok = true
					break
				}
			}
			if !ok {
				return nil
			}
		}
		st, err := os.Stat(p)
		if err != nil || st.Size() > maxGrepFileBytes || isBinaryFile(p) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		var hit []int
		for i, line := range lines {
			if re.MatchString(line) {
				hit = append(hit, i)
			}
		}
		if len(hit) == 0 {
			return nil
		}
		files++
		if o.FilesOnly {
			b.WriteString(p)
			b.WriteByte('\n')
			matches++
			if matches >= o.MaxResults {
				return errStop
			}
			return nil
		}
		printed := -1
		for _, h := range hit {
			from, to := h-o.Context, h+o.Context
			if from < 0 {
				from = 0
			}
			if to >= len(lines) {
				to = len(lines) - 1
			}
			if from <= printed {
				from = printed + 1
			} else if printed >= 0 && o.Context > 0 {
				b.WriteString("--\n")
			}
			for i := from; i <= to; i++ {
				sep := "-"
				if i == h || re.MatchString(lines[i]) {
					sep = ":"
				}
				fmt.Fprintf(&b, "%s%s%d%s%s\n", p, sep, i+1, sep, clip(lines[i]))
			}
			if to > printed {
				printed = to
			}
			matches++
			if matches >= o.MaxResults || overBudget() {
				return errStop
			}
		}
		return nil
	}

	st, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	var ignore *ignoreWalker
	if !st.IsDir() {
		globs = nil
		err = searchFile(root)
	} else {
		ignore = newIgnoreWalker(root, o.IncludeIgnored)
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != root && skipDirs[d.Name()] && !wantSkipped {
					return filepath.SkipDir
				}
				if ignore.skip(p, true) {
					return filepath.SkipDir
				}
				return nil
			}
			if ignore.skip(p, false) {
				return nil
			}
			return searchFile(p)
		})
	}
	if err != nil && !errors.Is(err, errStop) {
		return "", err
	}
	if matches == 0 {
		return fmt.Sprintf("no matches for %q under %s%s", o.Pattern, root, ignore.note()), nil
	}
	if matches >= o.MaxResults {
		fmt.Fprintf(&b, "… stopped at max_results=%d (%d files so far); narrow the search\n", o.MaxResults, files)
	} else if overBudget() {
		fmt.Fprintf(&b, "… stopped after %d matches (output limit); narrow the search or use files_only\n", matches)
	}
	return b.String(), nil
}

func clip(s string) string {
	s = strings.TrimRight(s, "\r")
	if len(s) <= maxGrepLineChars {
		return s
	}
	cut := maxGrepLineChars
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " …"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
