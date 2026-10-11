package sysinfo

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestDescribeThisHost(t *testing.T) {
	wd, _ := os.Getwd()
	h := Describe(context.Background(), []string{wd})
	if h.Hostname == "" || h.OS == "" || h.CPUs <= 0 || len(h.Disks) == 0 {
		t.Fatalf("incomplete host description: %+v", h)
	}
	out := h.String(nil)
	for _, want := range []string{"host:", "os:", "disks:", "mcp-shell: pid"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestProbeFindsGoWithVersion(t *testing.T) {
	tools := ProbeTools(context.Background())
	for _, tool := range tools {
		if tool.Name == "go" && tool.Path != "" {
			if !strings.Contains(tool.Version, "go version") {
				t.Fatalf("go found at %s but version is %q", tool.Path, tool.Version)
			}
			return
		}
	}
	t.Skip("go is not on PATH here")
}

func TestBytes(t *testing.T) {
	for in, want := range map[uint64]string{512: "512 B", 2048: "2.0 KB", 3 << 30: "3.0 GB"} {
		if got := Bytes(in); got != want {
			t.Fatalf("Bytes(%d) = %q, want %q", in, got, want)
		}
	}
}
