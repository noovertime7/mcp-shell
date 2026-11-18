//go:build !windows

package exectools

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestZshBehavesLikeBashForUnmatchedGlobsAndEquals(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	r, _ := testRunner(t)
	res, err := r.Run(context.Background(), RunRequest{Command: "echo --include=*.nomatch-xyz; echo =====", Shell: "zsh"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "--include=*.nomatch-xyz") || !strings.Contains(res.Stdout, "=====") {
		t.Fatalf("exit %d stdout %q stderr %q", res.ExitCode, res.Stdout, res.Stderr)
	}
}
