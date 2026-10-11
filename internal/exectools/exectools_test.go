package exectools

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/noovertime7/mcp-shell/internal/pathguard"
)

func TestCapBufferTruncates(t *testing.T) {
	c := newCapBuffer(10)
	c.Write([]byte("01234"))
	c.Write([]byte(strings.Repeat("x", 100)))
	c.Write([]byte("56789"))
	got, truncated := c.Result()
	if !truncated || !strings.HasPrefix(got, "01234") || !strings.HasSuffix(got, "56789") || !strings.Contains(got, "truncated 100 bytes") {
		t.Fatalf("got %q truncated=%v", got, truncated)
	}

	c = newCapBuffer(100)
	c.Write([]byte("short"))
	if got, truncated := c.Result(); got != "short" || truncated {
		t.Fatalf("got %q truncated=%v", got, truncated)
	}
}

func TestRingLogOffsets(t *testing.T) {
	r := newRingLog(10)
	r.Write([]byte("abcdef"))
	data, from, dropped := r.Read(2, 100)
	if string(data) != "cdef" || from != 2 || dropped {
		t.Fatalf("got %q from=%d dropped=%v", data, from, dropped)
	}
	r.Write([]byte(strings.Repeat("z", 20)))
	data, from, dropped = r.Read(0, 100)
	if !dropped || from == 0 || int64(len(data))+from != r.End() {
		t.Fatalf("got len=%d from=%d dropped=%v end=%d", len(data), from, dropped, r.End())
	}
}

func TestTrimPartialUTF8(t *testing.T) {
	s := []byte("中文")
	if got := string(trimPartialEnd(s[:4])); got != "中" {
		t.Fatalf("trimPartialEnd got %q", got)
	}
	if got := string(trimPartialStart(s[2:])); got != "文" {
		t.Fatalf("trimPartialStart got %q", got)
	}
}

func testRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	g, err := pathguard.New([]string{t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	shell := "sh"
	if runtime.GOOS == "windows" {
		shell = "powershell"
	}
	return &Runner{Guard: g, DefaultShell: shell, DefaultTimeout: 30 * time.Second, MaxTimeout: time.Minute, MaxOutputBytes: 100_000}, shell
}

func TestRunExitCodeAndOutput(t *testing.T) {
	r, shell := testRunner(t)
	cmd := "echo hello-中文; echo oops 1>&2; exit 3"
	if shell == "powershell" {
		cmd = "Write-Output 'hello-中文'; [Console]::Error.WriteLine('oops'); cmd /c exit 3"
	}
	res, err := r.Run(context.Background(), RunRequest{Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || !strings.Contains(res.Stdout, "hello-中文") || !strings.Contains(res.Stderr, "oops") {
		t.Fatalf("unexpected result:\n%s", res)
	}
}

func TestRunPowerShellErrorsArePlainText(t *testing.T) {
	r, shell := testRunner(t)
	if shell != "powershell" {
		t.Skip("Windows PowerShell only")
	}
	res, err := r.Run(context.Background(), RunRequest{Command: "Write-Output '中文'; no-such-command-xyz"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stderr, "CLIXML") || !strings.Contains(res.Stderr, "no-such-command-xyz") || !strings.Contains(res.Stdout, "中文") {
		t.Fatalf("unexpected result:\n%s", res)
	}
	if res.ExitCode == 0 {
		t.Fatalf("a failed command should not exit 0:\n%s", res)
	}
}

func TestRunTimeoutKills(t *testing.T) {
	r, shell := testRunner(t)
	cmd := "sleep 30"
	if shell == "powershell" {
		cmd = "Start-Sleep -Seconds 30"
	}
	start := time.Now()
	res, err := r.Run(context.Background(), RunRequest{Command: cmd, TimeoutSec: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || time.Since(start) > 15*time.Second {
		t.Fatalf("expected timeout within a few seconds, got %+v after %s", res, time.Since(start))
	}
}

func TestRunRejectsCwdOutsideRoots(t *testing.T) {
	r, _ := testRunner(t)
	if _, err := r.Run(context.Background(), RunRequest{Command: "echo x", Cwd: ".."}); err == nil {
		t.Fatal("expected cwd outside roots to be rejected")
	}
}

func TestBackgroundProcess(t *testing.T) {
	r, shell := testRunner(t)
	m := NewManager(r)
	cmd := "echo started; sleep 30"
	if shell == "powershell" {
		cmd = "Write-Output started; Start-Sleep -Seconds 30"
	}
	p, err := m.Start(cmd, "", "", "sleeper", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Read(p.ID, 0, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "started") || !strings.Contains(out, "running") {
		t.Fatalf("unexpected read:\n%s", out)
	}
	if _, err := m.Stop(p.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.List(), "no background processes") {
		t.Fatalf("process not forgotten: %s", m.List())
	}
}

func TestRunKeepsFullOutputOnlyWhenTruncated(t *testing.T) {
	r, shell := testRunner(t)
	r.LogDir = t.TempDir()
	r.MaxOutputBytes = 100

	small := "echo small"
	big := "for i in $(seq 1 200); do echo line-$i; done"
	if shell == "powershell" {
		small = "Write-Output small"
		big = "1..200 | ForEach-Object { Write-Output \"line-$_\" }"
	}
	res, err := r.Run(context.Background(), RunRequest{Command: small})
	if err != nil {
		t.Fatal(err)
	}
	if res.LogFile != "" {
		t.Fatalf("small output should not keep a log: %s", res.LogFile)
	}

	res, err = r.Run(context.Background(), RunRequest{Command: big})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.LogFile == "" {
		t.Fatalf("expected truncated result with a log file:\n%s", res)
	}
	data, err := os.ReadFile(res.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "line-1\n") && !strings.Contains(string(data), "line-1\r\n") || !strings.Contains(string(data), "line-200") {
		t.Fatalf("log misses output:\n%s", data)
	}
	entries, _ := os.ReadDir(r.LogDir)
	if len(entries) != 1 {
		t.Fatalf("expected exactly one log file, got %d", len(entries))
	}
}
