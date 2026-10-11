//go:build !windows

package exectools

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
)

// newShellCmd builds the command for macOS and Linux; cleanup must be called
// once it has exited. Commands run in their own process group so the whole
// tree can be killed.
func newShellCmd(ctx context.Context, shell, command string) (*exec.Cmd, func(), error) {
	var cmd *exec.Cmd
	cleanup := noCleanup
	switch shell {
	case "powershell", "pwsh":
		args, c, err := powershellArgs(command)
		if err != nil {
			return nil, nil, fmt.Errorf("write script: %w", err)
		}
		cleanup = c
		cmd = exec.CommandContext(ctx, "pwsh", args...)
	case "sh", "bash", "zsh":
		cmd = exec.CommandContext(ctx, shell, "-c", command)
	case "cmd":
		return nil, nil, fmt.Errorf("shell cmd is only available on Windows")
	default:
		return nil, nil, fmt.Errorf("unknown shell %q (use zsh, bash, sh or pwsh)", shell)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			killTree(cmd.Process.Pid)
		}
		return nil
	}
	return cmd, cleanup, nil
}

func killTree(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func decodeSystemCodePage([]byte) (string, bool) { return "", false }
