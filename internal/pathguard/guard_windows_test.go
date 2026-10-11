//go:build windows

package pathguard

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCaseInsensitive(t *testing.T) {
	g, root := newGuard(t)
	got, err := g.Resolve(strings.ToUpper(filepath.Join(root, "SUB", "A.TXT")))
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(got, filepath.Join(root, "sub", "a.txt")) {
		t.Fatalf("got %q", got)
	}
}

func TestResolveWindowsRejects(t *testing.T) {
	g, _ := newGuard(t)
	for _, in := range []string{`C:foo`, `\foo`, `/foo`, `\\?\C:\Windows`, `\\.\PhysicalDrive0`, `//server/share`, `C:\Windows\win.ini`} {
		if got, err := g.Resolve(in); err == nil {
			t.Errorf("Resolve(%q) = %q, want error", in, got)
		}
	}
}

func TestResolveJunctionEscape(t *testing.T) {
	g, root := newGuard(t)
	outside := t.TempDir()
	link := filepath.Join(root, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
		t.Skipf("mklink /J failed: %v %s", err, out)
	}
	if _, err := g.Resolve(`junction\x.txt`); err == nil || !strings.Contains(err.Error(), "outside roots") {
		t.Fatalf("expected outside-roots error through junction, got %v", err)
	}
}
