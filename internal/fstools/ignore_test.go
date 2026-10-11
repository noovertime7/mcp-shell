package fstools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobAndGrepSkipGitignoredPaths(t *testing.T) {
	f, root := newFS(t)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, ".gitignore"), "# build output\ndist/\n*.log\n/coverage\n!keep.log\n")
	write(t, filepath.Join(repo, "src", "app.ts"), "const needle = 1\n")
	write(t, filepath.Join(repo, "src", ".gitignore"), "generated.ts\n")
	write(t, filepath.Join(repo, "src", "generated.ts"), "const needle = 2\n")
	write(t, filepath.Join(repo, "dist", "app.js"), "var needle=1\n")
	write(t, filepath.Join(repo, "debug.log"), "needle\n")
	write(t, filepath.Join(repo, "keep.log"), "needle\n")
	write(t, filepath.Join(repo, "coverage", "x.txt"), "needle\n")
	write(t, filepath.Join(repo, "docs", "coverage", "y.txt"), "needle\n") // /coverage is anchored to the repo root

	out, err := f.Grep(GrepOptions{Pattern: "needle", Path: repo})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"app.ts", "keep.log", filepath.Join("docs", "coverage", "y.txt")} {
		if !strings.Contains(out, want) {
			t.Fatalf("grep should find %s:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"generated.ts", "app.js", "debug.log", filepath.Join(repo, "coverage")} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("grep should skip ignored %s:\n%s", unwanted, out)
		}
	}

	// Searching below the repository root still applies the root .gitignore.
	out, err = f.Grep(GrepOptions{Pattern: "needle", Path: filepath.Join(repo, "src")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "generated.ts") || !strings.Contains(out, "app.ts") {
		t.Fatalf("nested .gitignore not applied:\n%s", out)
	}

	out, err = f.Grep(GrepOptions{Pattern: "needle", Path: repo, IncludeIgnored: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "app.js") || !strings.Contains(out, "debug.log") {
		t.Fatalf("include_ignored should search everything:\n%s", out)
	}

	out, err = f.Glob("dist/**/*.js", repo, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "include_ignored=true") {
		t.Fatalf("an empty result should mention ignored paths:\n%s", out)
	}
	out, err = f.Glob("dist/**/*.js", repo, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "app.js") {
		t.Fatalf("include_ignored glob should find dist files:\n%s", out)
	}

	// An explicit path inside an ignored directory is searched.
	out, err = f.Grep(GrepOptions{Pattern: "needle", Path: filepath.Join(repo, "dist")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "app.js") {
		t.Fatalf("grep inside an ignored directory named as path should search it:\n%s", out)
	}
}

func TestMultiEditIsAllOrNothing(t *testing.T) {
	f, root := newFS(t)
	p := filepath.Join(root, "a.txt")
	write(t, p, "alpha\r\nbeta\r\ngamma\r\n")

	out, err := f.MultiEdit(p, []Edit{
		{OldString: "alpha", NewString: "ALPHA"},
		{OldString: "ALPHA\nbeta", NewString: "ALPHA\nBETA"}, // sees the first edit; LF matches CRLF
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "ALPHA\r\nBETA\r\ngamma\r\n" || !strings.Contains(out, "applied 2 edit(s)") {
		t.Fatalf("got %q (%s)", got, out)
	}

	_, err = f.MultiEdit(p, []Edit{
		{OldString: "gamma", NewString: "GAMMA"},
		{OldString: "missing", NewString: "x"},
	})
	if err == nil || !strings.Contains(err.Error(), "edit 2") || !strings.Contains(err.Error(), "no edit was written") {
		t.Fatalf("expected the failing edit to be named, got %v", err)
	}
	if got := read(t, p); strings.Contains(got, "GAMMA") {
		t.Fatalf("a failed multi_edit must not write anything: %q", got)
	}
}
