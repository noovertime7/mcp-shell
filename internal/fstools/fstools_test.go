package fstools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noovertime7/mcp-shell/internal/pathguard"
)

func newFS(t *testing.T) (*FS, string) {
	t.Helper()
	g, err := pathguard.New([]string{t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &FS{Guard: g, MaxReadBytes: 1 << 20}, g.Roots()[0]
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEditUniqueAndReplaceAll(t *testing.T) {
	f, root := newFS(t)
	p := filepath.Join(root, "a.go")
	write(t, p, "x := 1\nx := 1\ny := 2\n")

	if _, err := f.EditFile("a.go", "x := 1", "x := 3", false); err == nil || !strings.Contains(err.Error(), "occurs 2 times") {
		t.Fatalf("expected not-unique error, got %v", err)
	}
	if _, err := f.EditFile("a.go", "zzz", "q", false); err == nil {
		t.Fatal("expected not-found error")
	}
	if _, err := f.EditFile("a.go", "y := 2", "y := 5", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile("a.go", "x := 1", "x := 3", true); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "x := 3\nx := 3\ny := 5\n" {
		t.Fatalf("got %q", got)
	}
}

func TestEditPreservesCRLFAndBOM(t *testing.T) {
	f, root := newFS(t)
	p := filepath.Join(root, "win.txt")
	write(t, p, "\ufeffline one\r\nline two\r\nline three\r\n")

	// The client sends LF-only text, as models usually do.
	if _, err := f.EditFile(p, "line one\nline two", "line 1\nline 2", false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "\ufeffline 1\r\nline 2\r\nline three\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteFileAtomicAndDirs(t *testing.T) {
	f, root := newFS(t)
	if _, err := f.WriteFile("new/dir/x.txt", "hi", false); err == nil {
		t.Fatal("expected error without create_dirs")
	}
	if _, err := f.WriteFile("new/dir/x.txt", "hi", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteFile("new/dir/x.txt", "bye", false); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "new", "dir")
	if got := read(t, filepath.Join(dir, "x.txt")); got != "bye" {
		t.Fatalf("got %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestReadFileOffsetLimit(t *testing.T) {
	f, root := newFS(t)
	write(t, filepath.Join(root, "n.txt"), "a\r\nb\r\nc\r\nd\r\n")
	out, err := f.ReadFile("n.txt", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "     2\tb\n     3\tc\n") || !strings.Contains(out, "offset=4") {
		t.Fatalf("got %q", out)
	}
	write(t, filepath.Join(root, "bin.dat"), "ab\x00cd")
	if _, err := f.ReadFile("bin.dat", 0, 0); err == nil {
		t.Fatal("expected binary refusal")
	}
}

func TestDeleteAndMove(t *testing.T) {
	f, root := newFS(t)
	if _, err := f.DeletePath(root, true); err == nil {
		t.Fatal("deleting the root must be refused")
	}
	write(t, filepath.Join(root, "d", "f.txt"), "x")
	if _, err := f.DeletePath("d", false); err == nil {
		t.Fatal("non-recursive delete of non-empty dir must fail")
	}
	if _, err := f.MovePath("d/f.txt", "e/g.txt", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DeletePath("d", true); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(root, "e", "g.txt")); got != "x" {
		t.Fatalf("got %q", got)
	}
}

func TestGlobAndGrep(t *testing.T) {
	f, root := newFS(t)
	write(t, filepath.Join(root, "a", "main.go"), "package main\nfunc Hello() {}\n")
	write(t, filepath.Join(root, "a", "b", "x.ts"), "export const hello = 1\n")
	write(t, filepath.Join(root, "node_modules", "dep", "y.ts"), "hello\n")

	out, err := f.Glob("**/*.{go,ts}", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "x.ts") || strings.Contains(out, "node_modules") {
		t.Fatalf("glob got %q", out)
	}

	out, err = f.Grep(GrepOptions{Pattern: "hello", IgnoreCase: true, Glob: "*.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "x.ts:1:export const hello") || strings.Contains(out, "main.go") || strings.Contains(out, "node_modules") {
		t.Fatalf("grep got %q", out)
	}
}

func TestExpandBraces(t *testing.T) {
	got := strings.Join(expandBraces("a.{go,{ts,tsx}}"), " ")
	if got != "a.go a.ts a.tsx" {
		t.Fatalf("got %q", got)
	}
}
