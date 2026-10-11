package exectools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pick returns the PowerShell variant of a test command on Windows.
func pick(shell, sh, ps string) string {
	if shell == "powershell" {
		return ps
	}
	return sh
}

func TestRunDetachesLongCommandAndKeepsItsOutput(t *testing.T) {
	r, shell := testRunner(t)
	r.DetachAfter = 2 * time.Second
	m := NewManager(r)
	r.Procs = m
	cmd := pick(shell, "echo begin; sleep 4; echo finished", "Write-Output begin; Start-Sleep -Seconds 4; Write-Output finished")

	start := time.Now()
	res, err := r.Run(context.Background(), RunRequest{Command: cmd, TimeoutSec: 30})
	if err != nil {
		t.Fatal(err)
	}
	if res.Detached == nil || time.Since(start) > 4*time.Second {
		t.Fatalf("expected the call to return detached after ~2s, got after %s:\n%s", time.Since(start), res)
	}
	out := res.String()
	if !strings.Contains(out, "exit_code: none") || !strings.Contains(out, "begin") || !strings.Contains(out, "read_process_output id="+res.Detached.ID) {
		t.Fatalf("detached result does not say how to continue:\n%s", out)
	}

	read, err := m.Read(res.Detached.ID, ReadOptions{Since: res.NextOffset, UntilExit: true, WaitMs: 20_000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read, "exited (exit_code 0") || !strings.Contains(read, "finished") || strings.Contains(read, "begin") {
		t.Fatalf("until_exit should return the rest of the output once the command exits:\n%s", read)
	}
}

func TestRunWithoutDetachWaitsForTheEnd(t *testing.T) {
	r, shell := testRunner(t)
	r.DetachAfter = 10 * time.Second
	r.Procs = NewManager(r)
	res, err := r.Run(context.Background(), RunRequest{Command: pick(shell, "echo quick", "Write-Output quick"), TimeoutSec: 30})
	if err != nil {
		t.Fatal(err)
	}
	if res.Detached != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, "quick") {
		t.Fatalf("a quick command must finish normally:\n%s", res)
	}
}

func TestRunCancelledBeforeDetachKillsTheCommand(t *testing.T) {
	r, shell := testRunner(t)
	r.DetachAfter = 20 * time.Second
	r.Procs = NewManager(r)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(time.Second, cancel)
	start := time.Now()
	res, err := r.Run(ctx, RunRequest{Command: pick(shell, "sleep 30", "Start-Sleep -Seconds 30"), TimeoutSec: 60})
	if err != nil {
		t.Fatal(err)
	}
	if res.Detached != nil || res.ExitCode != -1 || time.Since(start) > 15*time.Second {
		t.Fatalf("a cancelled call must kill its command, got after %s:\n%s", time.Since(start), res)
	}
}

func TestReadWaitForAndTail(t *testing.T) {
	r, shell := testRunner(t)
	m := NewManager(r)
	m.MaxWait = 20 * time.Second
	cmd := pick(shell,
		"echo booting; sleep 1; echo 'ready on 3000'; sleep 30",
		"Write-Output booting; Start-Sleep -Seconds 1; Write-Output 'ready on 3000'; Start-Sleep -Seconds 30")
	p, err := m.Start(cmd, "", "", "server", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop(p.ID)
	out, err := m.Read(p.ID, ReadOptions{WaitFor: `ready on \d+`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wait_for: matched") || !strings.Contains(out, "ready on 3000") {
		t.Fatalf("wait_for should return once the pattern shows up:\n%s", out)
	}

	big := pick(shell,
		"for i in $(seq 1 6000); do echo line-$i-padding-padding; done",
		"1..6000 | ForEach-Object { \"line-$_-padding-padding\" }")
	q, err := m.Start(big, "", "", "big", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop(q.ID)
	out, err = m.Read(q.ID, ReadOptions{UntilExit: true, Tail: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line-6000-") || strings.Contains(out, "line-1-") || !strings.Contains(out, "skipped") {
		t.Fatalf("tail should show the end of a long output:\n%.400s", out)
	}
}

func TestCommandsDoNotInheritTheServerToken(t *testing.T) {
	r, shell := testRunner(t)
	t.Setenv("MCP_SHELL_TOKEN", "token-that-must-not-leak")
	res, err := r.Run(context.Background(), RunRequest{Command: pick(shell, "env", "Get-ChildItem env: | Out-String -Width 400")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "token-that-must-not-leak") || !strings.Contains(strings.ToUpper(res.Stdout), "PATH") {
		t.Fatalf("the environment of a command must not contain MCP_SHELL_TOKEN:\n%.2000s", res.Stdout)
	}
}

func TestNormalizeExitCode(t *testing.T) {
	for in, want := range map[int]int{0: 0, 1: 1, 4294967295: -1, 4294967294: -2} {
		if got := normalizeExitCode(in); got != want {
			t.Fatalf("normalizeExitCode(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestPowerShellNativeStderrIsNotAFailure(t *testing.T) {
	r, shell := testRunner(t)
	if shell != "powershell" {
		t.Skip("Windows PowerShell only")
	}
	for _, cmd := range []string{
		`cmd /c "echo warning 1>&2 & exit 0" 2>&1`,
		`cmd /c "echo warning 1>&2 & exit 0" 2>$null`,
	} {
		res, err := r.Run(context.Background(), RunRequest{Command: cmd})
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode != 0 {
			t.Fatalf("%s: a native command that only warned on stderr must exit 0:\n%s", cmd, res)
		}
	}
	res, err := r.Run(context.Background(), RunRequest{Command: `Get-Item C:\no-such-path-mcp-shell`})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("a failing cmdlet must still fail:\n%s", res)
	}
	res, err = r.Run(context.Background(), RunRequest{Command: `cmd /c "exit 5" 2>&1`})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 5 {
		t.Fatalf("a native exit code must be kept:\n%s", res)
	}
}

func TestPowerShellFileCmdletsUseUTF8(t *testing.T) {
	r, shell := testRunner(t)
	if shell != "powershell" {
		t.Skip("Windows PowerShell only")
	}
	dir := r.Guard.Roots()[0]
	if err := os.WriteFile(filepath.Join(dir, "in.txt"), []byte("备份内容浏览\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), RunRequest{Command: `Get-Content in.txt; '恢复中文' > out.txt`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "备份内容浏览") {
		t.Fatalf("Get-Content must read UTF-8 files:\n%s", res)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "恢复中文") {
		t.Fatalf("> must write UTF-8, got % x", data)
	}
}
