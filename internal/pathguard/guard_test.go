package pathguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newGuard(t *testing.T) (*Guard, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := New([]string{root}, map[string]string{"/mnt/hgfs/ws": root})
	if err != nil {
		t.Fatal(err)
	}
	return g, g.Roots()[0]
}

func TestResolveInside(t *testing.T) {
	g, root := newGuard(t)
	cases := map[string]string{
		filepath.Join(root, "sub", "a.txt"):     filepath.Join(root, "sub", "a.txt"),
		"sub/a.txt":                             filepath.Join(root, "sub", "a.txt"),
		"sub/new/deeper.txt":                    filepath.Join(root, "sub", "new", "deeper.txt"),
		"sub/../sub/a.txt":                      filepath.Join(root, "sub", "a.txt"),
		"/mnt/hgfs/ws/sub/a.txt":                filepath.Join(root, "sub", "a.txt"),
		"/mnt/hgfs/ws":                          root,
		filepath.Join(root, "sub") + "/../sub/": filepath.Join(root, "sub"),
	}
	for in, want := range cases {
		got, err := g.Resolve(in)
		if err != nil {
			t.Errorf("Resolve(%q) error: %v", in, err)
			continue
		}
		if !samePath(got, want) {
			t.Errorf("Resolve(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveOutside(t *testing.T) {
	g, root := newGuard(t)
	for _, in := range []string{
		"..",
		"../x",
		"sub/../../x",
		filepath.Dir(root),
		root + "-sibling",
		`\\server\share\x`,
		"/mnt/hgfs/wsx/a",
		"",
	} {
		if got, err := g.Resolve(in); err == nil {
			t.Errorf("Resolve(%q) = %q, want error", in, got)
		}
	}
}

func TestResolveSymlinkEscape(t *testing.T) {
	g, root := newGuard(t)
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if _, err := g.Resolve("escape/file.txt"); err == nil || !strings.Contains(err.Error(), "outside roots") {
		t.Fatalf("expected outside-roots error through symlink, got %v", err)
	}
}

func TestIsRoot(t *testing.T) {
	g, root := newGuard(t)
	if !g.IsRoot(root) {
		t.Fatal("root not recognised")
	}
	if g.IsRoot(filepath.Join(root, "sub")) {
		t.Fatal("subdir treated as root")
	}
}

func TestToClient(t *testing.T) {
	g, root := newGuard(t)
	other := filepath.Join(root+"-other", "x")
	cases := map[string]string{
		root:                                "/mnt/hgfs/ws",
		filepath.Join(root, "sub", "a.txt"): "/mnt/hgfs/ws/sub/a.txt",
		other:                               other,
	}
	for in, want := range cases {
		if got := g.ToClient(in); got != want {
			t.Errorf("ToClient(%q) = %q, want %q", in, got, want)
		}
	}
}
