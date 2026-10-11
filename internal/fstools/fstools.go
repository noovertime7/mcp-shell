// Package fstools implements the file tools: list, read, write, edit, delete,
// move, glob and grep. Every path goes through the pathguard first.
package fstools

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/noovertime7/mcp-shell/internal/pathguard"
)

// skipDirs are not descended into by list_dir, glob and grep.
var skipDirs = map[string]bool{".git": true, "node_modules": true, ".pnpm-store": true, ".next": true, "__pycache__": true}

const (
	defaultReadLimit = 2000
	maxLineChars     = 2000
	binarySniffBytes = 8000
)

// FS holds the shared state of the file tools.
type FS struct {
	Guard        *pathguard.Guard
	MaxReadBytes int
}

// ---------- list_dir ----------

// ListDir lists path up to depth levels deep.
func (f *FS) ListDir(path string, depth int, showHidden bool) (string, error) {
	if path == "" {
		path = "."
	}
	dir, err := f.Guard.Resolve(path)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("not a directory: %s", dir)
	}
	if depth <= 0 {
		depth = 1
	}
	if depth > 6 {
		depth = 6
	}
	const maxEntries = 1000
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", dir)
	count := 0
	var walk func(d string, level int) error
	walk = func(d string, level int) error {
		entries, err := os.ReadDir(d)
		if err != nil {
			fmt.Fprintf(&b, "%s[error: %v]\n", strings.Repeat("  ", level), err)
			return nil
		}
		for _, e := range entries {
			name := e.Name()
			if !showHidden && strings.HasPrefix(name, ".") {
				continue
			}
			if count >= maxEntries {
				return errStop
			}
			count++
			indent := strings.Repeat("  ", level)
			info, err := e.Info()
			if err != nil {
				fmt.Fprintf(&b, "%s%s [error: %v]\n", indent, name, err)
				continue
			}
			mtime := info.ModTime().Format("2006-01-02 15:04")
			switch {
			case e.IsDir():
				if skipDirs[name] {
					fmt.Fprintf(&b, "%s%s/  (%s, not expanded)\n", indent, name, mtime)
					continue
				}
				fmt.Fprintf(&b, "%s%s/  (%s)\n", indent, name, mtime)
				if level+1 < depth {
					if err := walk(filepath.Join(d, name), level+1); err != nil {
						return err
					}
				}
			case info.Mode()&fs.ModeSymlink != 0:
				fmt.Fprintf(&b, "%s%s@  (link)\n", indent, name)
			default:
				fmt.Fprintf(&b, "%s%s  (%s, %s)\n", indent, name, humanSize(info.Size()), mtime)
			}
		}
		return nil
	}
	if err := walk(dir, 0); errors.Is(err, errStop) {
		fmt.Fprintf(&b, "... stopped after %d entries; list a subdirectory or lower depth\n", maxEntries)
	}
	return b.String(), nil
}

var errStop = errors.New("stop")

// ---------- read_file ----------

// ReadFile returns lines [offset, offset+limit) of path, numbered from 1.
func (f *FS) ReadFile(path string, offset, limit int) (string, error) {
	p, err := f.Guard.Resolve(path)
	if err != nil {
		return "", err
	}
	file, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a directory; use list_dir", p)
	}
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = defaultReadLimit
	}

	r := bufio.NewReaderSize(file, 64*1024)
	head, _ := r.Peek(binarySniffBytes)
	if bytes.IndexByte(head, 0) >= 0 {
		return "", fmt.Errorf("%s looks like a binary file (%s); not reading it", p, humanSize(st.Size()))
	}

	var b strings.Builder
	lineNo, written, budget := 0, 0, f.MaxReadBytes
	if budget <= 0 {
		budget = 60_000
	}
	more := false
	for {
		line, err := r.ReadString('\n')
		if line == "" && err != nil {
			break
		}
		lineNo++
		if lineNo == 1 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if lineNo < offset {
			if err != nil {
				break
			}
			continue
		}
		if written >= limit || budget <= 0 {
			more = true
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if utf8.RuneCountInString(line) > maxLineChars {
			line = string([]rune(line)[:maxLineChars]) + " …[line truncated]"
		}
		out := fmt.Sprintf("%6d\t%s\n", lineNo, line)
		b.WriteString(out)
		budget -= len(out)
		written++
		if err != nil {
			break
		}
	}
	if written == 0 {
		if lineNo == 0 {
			return fmt.Sprintf("(empty file: %s)", p), nil
		}
		return "", fmt.Errorf("offset %d is past the end of %s (%d lines)", offset, p, lineNo)
	}
	if more {
		fmt.Fprintf(&b, "… more lines follow; call read_file with offset=%d to continue\n", offset+written)
	}
	return b.String(), nil
}

// ---------- write_file ----------

// WriteFile creates or overwrites path with content, atomically.
func (f *FS) WriteFile(path, content string, createDirs bool) (string, error) {
	p, err := f.Guard.Resolve(path)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return "", fmt.Errorf("%s is a directory", p)
	}
	dir := filepath.Dir(p)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if !createDirs {
			return "", fmt.Errorf("directory %s does not exist; pass create_dirs=true to create it", dir)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	existed, err := atomicWrite(p, []byte(content))
	if err != nil {
		return "", err
	}
	verb := "created"
	if existed {
		verb = "overwrote"
	}
	return fmt.Sprintf("%s %s (%d bytes)", verb, p, len(content)), nil
}

// atomicWrite writes data to a temp file next to p and renames it over p,
// keeping p's permissions when it already exists.
func atomicWrite(p string, data []byte) (existed bool, err error) {
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(p); err == nil {
		existed = true
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".mcp-shell-*.tmp")
	if err != nil {
		return existed, err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return existed, err
	}
	if err = tmp.Close(); err != nil {
		return existed, err
	}
	if err = os.Chmod(tmpName, mode); err != nil {
		return existed, err
	}
	if err = os.Rename(tmpName, p); err != nil {
		return existed, err
	}
	return existed, nil
}

// ---------- edit_file ----------

// EditFile replaces oldStr with newStr in path. Unless replaceAll is set,
// oldStr must occur exactly once. Line endings and a UTF-8 BOM are preserved.
func (f *FS) EditFile(path, oldStr, newStr string, replaceAll bool) (string, error) {
	if oldStr == "" {
		return "", errors.New("old_string must not be empty; use write_file to create or replace a whole file")
	}
	if oldStr == newStr {
		return "", errors.New("old_string and new_string are identical")
	}
	p, err := f.Guard.Resolve(path)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	text, bom := string(raw), ""
	if strings.HasPrefix(text, "\ufeff") {
		bom, text = "\ufeff", strings.TrimPrefix(text, "\ufeff")
	}
	crlf := isCRLF(text)
	if crlf {
		text = strings.ReplaceAll(text, "\r\n", "\n")
		oldStr = strings.ReplaceAll(oldStr, "\r\n", "\n")
		newStr = strings.ReplaceAll(newStr, "\r\n", "\n")
	}

	n := strings.Count(text, oldStr)
	switch {
	case n == 0:
		return "", fmt.Errorf("old_string not found in %s (it must match exactly, including whitespace and indentation)", p)
	case n > 1 && !replaceAll:
		return "", fmt.Errorf("old_string occurs %d times in %s; include more surrounding context to make it unique, or set replace_all=true", n, p)
	}
	if replaceAll {
		text = strings.ReplaceAll(text, oldStr, newStr)
	} else {
		text = strings.Replace(text, oldStr, newStr, 1)
	}
	if crlf {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	if _, err := atomicWrite(p, []byte(bom+text)); err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced %d occurrence(s) in %s", n, p), nil
}

// isCRLF reports whether every newline in s is part of a CRLF pair.
func isCRLF(s string) bool {
	crlf := strings.Count(s, "\r\n")
	return crlf > 0 && crlf == strings.Count(s, "\n")
}

// ---------- delete_path ----------

// DeletePath removes a file, or a directory when recursive is set (or it is empty).
func (f *FS) DeletePath(path string, recursive bool) (string, error) {
	p, err := f.Guard.Resolve(path)
	if err != nil {
		return "", err
	}
	if f.Guard.IsRoot(p) {
		return "", fmt.Errorf("refusing to delete a root directory: %s", p)
	}
	st, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if st.IsDir() && recursive {
		if err := os.RemoveAll(p); err != nil {
			return "", err
		}
		return fmt.Sprintf("deleted directory %s and its contents", p), nil
	}
	if err := os.Remove(p); err != nil {
		if st.IsDir() {
			return "", fmt.Errorf("%v (directory not empty? pass recursive=true)", err)
		}
		return "", err
	}
	return fmt.Sprintf("deleted %s", p), nil
}

// ---------- move_path ----------

// MovePath renames src to dst, creating dst's parent directories.
func (f *FS) MovePath(src, dst string, overwrite bool) (string, error) {
	s, err := f.Guard.Resolve(src)
	if err != nil {
		return "", err
	}
	d, err := f.Guard.Resolve(dst)
	if err != nil {
		return "", err
	}
	if f.Guard.IsRoot(s) {
		return "", fmt.Errorf("refusing to move a root directory: %s", s)
	}
	if _, err := os.Lstat(s); err != nil {
		return "", err
	}
	if st, err := os.Lstat(d); err == nil {
		if !overwrite {
			return "", fmt.Errorf("destination %s already exists; pass overwrite=true to replace it", d)
		}
		if st.IsDir() {
			return "", fmt.Errorf("destination %s is an existing directory; delete it first", d)
		}
	}
	if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(s, d); err != nil {
		return "", err
	}
	return fmt.Sprintf("moved %s -> %s", s, d), nil
}

// ---------- helpers ----------

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1fGB", float64(n)/(1024*1024*1024))
	}
}

// isBinaryFile sniffs the first bytes of a file for NUL bytes.
func isBinaryFile(p string) bool {
	file, err := os.Open(p)
	if err != nil {
		return true
	}
	defer file.Close()
	buf := make([]byte, binarySniffBytes)
	n, _ := io.ReadFull(file, buf)
	return bytes.IndexByte(buf[:n], 0) >= 0
}

// modTime is used for sorting search results, newest first.
func modTime(p string) time.Time {
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}
