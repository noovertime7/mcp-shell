package pathguard

import (
	"path/filepath"
	"testing"
)

func TestUnrestrictedAccessIsExplicit(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	g, err := New([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outside, "new-file.txt")
	if _, err := g.Resolve(path); err == nil {
		t.Fatal("restricted mode accepted outside root")
	}
	g.SetUnrestricted(true)
	if _, err := g.Resolve(path); err != nil {
		t.Fatalf("unrestricted mode rejected outside root: %v", err)
	}
	if g.IsRoot(g.Roots()[0]) {
		t.Fatal("unrestricted mode still applying app-level root protection")
	}
	g.SetUnrestricted(false)
	if _, err := g.Resolve(path); err == nil {
		t.Fatal("restricted mode not restored")
	}
}
