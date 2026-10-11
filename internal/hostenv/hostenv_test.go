package hostenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMergeAddsMissingPathEntriesAndVariables(t *testing.T) {
	sep := string(os.PathListSeparator)
	base := []string{"PATH=" + strings.Join([]string{"/a", "/b"}, sep), "KEEP=process"}
	machine := map[string]string{"PATH": strings.Join([]string{"/b/", "/c"}, sep), "KEEP": "registry", "NEWVAR": "1"}
	user := map[string]string{"PATH": strings.Join([]string{"/c", "/d"}, sep)}
	env, diff := merge(base, machine, user)

	path, _ := Get(env, "PATH")
	if got, want := path, strings.Join([]string{"/a", "/b", "/c", "/d"}, sep); got != want {
		t.Fatalf("PATH = %q, want %q (existing entries first, a trailing slash is the same entry)", got, want)
	}
	if v, _ := Get(env, "KEEP"); v != "process" {
		t.Fatalf("a variable mcp-shell was started with must win, got %q", v)
	}
	if v, ok := Get(env, "NEWVAR"); !ok || v != "1" {
		t.Fatalf("a variable only in the registry must be added, got %q %v", v, ok)
	}
	if strings.Join(diff.PathAdded, ",") != "/c,/d" || strings.Join(diff.VarsAdded, ",") != "NEWVAR" {
		t.Fatalf("diff = %+v", diff)
	}
}

func TestMergeWithoutChangesKeepsBase(t *testing.T) {
	base := []string{"PATH=/a", "X=1"}
	env, diff := merge(base, map[string]string{"PATH": "/a", "X": "2"})
	if strings.Join(env, "|") != strings.Join(base, "|") || len(diff.PathAdded)+len(diff.VarsAdded) != 0 {
		t.Fatalf("env=%v diff=%+v", env, diff)
	}
}

func TestLookPathUsesGivenEnvironment(t *testing.T) {
	dir := t.TempDir()
	name := "mytool"
	file := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		file += ".cmd"
	}
	if err := os.WriteFile(file, []byte("echo"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + dir, "PATHEXT=.COM;.EXE;.BAT;.CMD"}
	if got := LookPath(name, env); !strings.EqualFold(got, file) {
		t.Fatalf("LookPath = %q, want %q", got, file)
	}
	if got := LookPath(name, []string{"PATH=" + t.TempDir()}); got != "" {
		t.Fatalf("LookPath found %q on a PATH without it", got)
	}
}

func TestEnvironOnThisHost(t *testing.T) {
	env, _ := Environ()
	if _, ok := Get(env, "PATH"); !ok {
		t.Fatal("PATH missing from Environ()")
	}
	if runtime.GOOS == "windows" {
		if _, err := Scope("machine"); err != nil {
			t.Fatalf("reading the machine environment: %v", err)
		}
	}
}
