package fstools

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// gitignore rules, so glob and grep skip build output (dist/, target/, ...)
// the way git does. It covers what .gitignore files use in practice:
// comments, "!" negation, a trailing "/" for directories, patterns anchored
// by a "/" and "**". Global excludes and core.excludesFile are not read.

type ignoreRule struct {
	base    string // slash path of the directory holding the .gitignore, relative to the walk root ("" for root or above)
	above   string // for files above the walk root: the walk root relative to their directory
	pattern string
	negate  bool
	dirOnly bool
	anchor  bool // matched against the path relative to base, not just the name
}

// ignoreSet is the rules that apply inside one directory, outermost first.
type ignoreSet struct {
	rules []ignoreRule
}

func parseIgnore(path string, base, above string) []ignoreRule {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var rules []ignoreRule
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if !strings.HasSuffix(line, `\ `) {
			line = strings.TrimRight(line, " \t")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{base: base, above: above}
		if strings.HasPrefix(line, "!") {
			r.negate, line = true, line[1:]
		} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly, line = true, strings.TrimRight(line, "/")
		}
		if strings.Contains(line, "/") {
			r.anchor, line = true, strings.TrimPrefix(line, "/")
		}
		if line == "" {
			continue
		}
		if runtime.GOOS == "windows" {
			line = strings.ToLower(line)
		}
		r.pattern = line
		rules = append(rules, r)
	}
	return rules
}

// newIgnoreSet loads the .gitignore files from the repository root (the
// nearest directory with .git) down to root, and .git/info/exclude.
func newIgnoreSet(root string) *ignoreSet {
	var dirs []string
	repo := ""
	for d := root; ; {
		dirs = append(dirs, d)
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			repo = d
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if repo == "" {
		dirs = dirs[:1] // not in a repository: only root's own .gitignore
	}
	s := &ignoreSet{}
	if repo != "" {
		above, _ := filepath.Rel(repo, root)
		s.rules = append(s.rules, parseIgnore(filepath.Join(repo, ".git", "info", "exclude"), "", normRel(above))...)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		above, _ := filepath.Rel(dirs[i], root)
		s.rules = append(s.rules, parseIgnore(filepath.Join(dirs[i], ".gitignore"), "", normRel(above))...)
	}
	return s
}

// child returns the rules for directory dir (rel is its slash path relative
// to the walk root), adding dir's own .gitignore.
func (s *ignoreSet) child(dir, rel string) *ignoreSet {
	own := parseIgnore(filepath.Join(dir, ".gitignore"), normRel(rel), "")
	if len(own) == 0 {
		return s
	}
	return &ignoreSet{rules: append(append([]ignoreRule(nil), s.rules...), own...)}
}

// ignored reports whether rel (slash path relative to the walk root) is
// ignored; the last matching rule decides, as in git.
func (s *ignoreSet) ignored(rel string, isDir bool) bool {
	if s == nil || len(s.rules) == 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		rel = strings.ToLower(rel)
	}
	ignored := false
	for _, r := range s.rules {
		if r.dirOnly && !isDir {
			continue
		}
		p := rel
		switch {
		case r.above != "":
			p = r.above + "/" + rel
		case r.base != "":
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}
			p = strings.TrimPrefix(rel, r.base+"/")
		}
		var ok bool
		if r.anchor {
			ok = matchGlob(r.pattern, p)
		} else {
			ok = matchGlob(r.pattern, p[strings.LastIndex(p, "/")+1:])
		}
		if ok {
			ignored = !r.negate
		}
	}
	return ignored
}

// ignoreWalker tracks the rules per directory during a filepath.WalkDir.
type ignoreWalker struct {
	root    string
	sets    map[string]*ignoreSet
	skipped int
}

// newIgnoreWalker returns nil when ignore files should not be applied.
func newIgnoreWalker(root string, include bool) *ignoreWalker {
	if include {
		return nil
	}
	return &ignoreWalker{root: root, sets: map[string]*ignoreSet{root: newIgnoreSet(root)}}
}

// skip reports whether the walk should leave p out. For a directory that is
// kept it prepares the rules of its children.
func (w *ignoreWalker) skip(p string, isDir bool) bool {
	if w == nil || p == w.root {
		return false
	}
	set := w.sets[filepath.Dir(p)]
	rel, err := filepath.Rel(w.root, p)
	if err != nil || set == nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if set.ignored(rel, isDir) {
		w.skipped++
		return true
	}
	if isDir {
		w.sets[p] = set.child(p, rel)
	}
	return false
}

// note explains an empty result that ignore files may have caused.
func (w *ignoreWalker) note() string {
	if w == nil || w.skipped == 0 {
		return ""
	}
	return " (" + strconv.Itoa(w.skipped) + " paths ignored by .gitignore were skipped; pass include_ignored=true to search them)"
}

func normRel(rel string) string {
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return ""
	}
	if runtime.GOOS == "windows" {
		rel = strings.ToLower(rel)
	}
	return rel
}
