//go:build windows

package exectools

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

// newShellCmd builds the command; cleanup must be called once it has exited.
func newShellCmd(ctx context.Context, shell, command string) (*exec.Cmd, func(), error) {
	var cmd *exec.Cmd
	cleanup := noCleanup
	switch shell {
	case "powershell", "pwsh":
		exe := "powershell.exe"
		if shell == "pwsh" {
			exe = "pwsh.exe"
		}
		args, c, err := powershellArgs(command)
		if err != nil {
			return nil, nil, fmt.Errorf("write script: %w", err)
		}
		cleanup = c
		cmd = exec.CommandContext(ctx, exe, args...)
	case "cmd":
		// cmd.exe has its own quoting rules, so pass the raw command line.
		// /s strips the outer quotes and keeps everything inside verbatim.
		cmd = exec.CommandContext(ctx, "cmd.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CmdLine: `cmd.exe /d /s /c "chcp 65001>nul & ` + command + `"`,
		}
	case "bash", "sh":
		cmd = exec.CommandContext(ctx, shell, "-c", command)
	default:
		return nil, nil, fmt.Errorf("unknown shell %q (use powershell, pwsh or cmd)", shell)
	}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			killTree(cmd.Process.Pid)
		}
		return nil
	}
	return cmd, cleanup, nil
}

// killTree kills pid and all of its descendants.
func killTree(pid int) {
	kill := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(pid))
	_ = kill.Run()
	if p, err := syscallOpenProcess(pid); err == nil {
		_ = syscall.TerminateProcess(p, 1)
		_ = syscall.CloseHandle(p)
	}
}

func syscallOpenProcess(pid int) (syscall.Handle, error) {
	return syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
}

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
)

// decodeSystemCodePage converts ANSI code page bytes to a string.
func decodeSystemCodePage(b []byte) (string, bool) {
	if len(b) == 0 {
		return "", true
	}
	const cpACP = 0
	n, _, _ := procMultiByteToWideChar.Call(cpACP, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, 0)
	if n == 0 {
		return "", false
	}
	buf := make([]uint16, n)
	n, _, _ = procMultiByteToWideChar.Call(cpACP, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), uintptr(unsafe.Pointer(&buf[0])), n)
	if n == 0 {
		return "", false
	}
	return syscall.UTF16ToString(buf[:n]), true
}
