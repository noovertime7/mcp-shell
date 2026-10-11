// Package exectools runs shell commands on the host: one-shot commands with a
// timeout and long-running background processes such as dev servers.
package exectools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/noovertime7/mcp-shell/internal/hostenv"
	"github.com/noovertime7/mcp-shell/internal/pathguard"
)

// Runner runs commands with their working directory confined to the roots.
type Runner struct {
	Guard          *pathguard.Guard
	DefaultShell   string
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	MaxOutputBytes int
	// LogDir, when set, receives the full output of every command. Logs of
	// commands whose output fit in MaxOutputBytes are removed again.
	LogDir string
	// DetachAfter hands a command that is still running after this long to
	// Procs as a background process and returns, so the tool call ends before
	// the client or a proxy in between gives up waiting for it. The command
	// keeps its timeout. Zero (or a nil Procs) disables it.
	DetachAfter time.Duration
	Procs       *Manager
}

// RunRequest is the input of Run.
type RunRequest struct {
	Command    string
	Cwd        string
	Shell      string
	TimeoutSec int
	Env        map[string]string
}

// RunResult is the outcome of a finished (or detached) command.
type RunResult struct {
	Command   string
	Cwd       string
	Shell     string
	ExitCode  int
	Stdout    string
	Stderr    string
	Duration  time.Duration
	TimedOut  bool
	Truncated bool
	// LogFile is the full output, kept only when Truncated is set.
	LogFile string
	// Detached is the background process the still-running command became;
	// NextOffset is where read_process_output continues its output.
	Detached   *Proc
	NextOffset int64
}

// String renders the result for the model.
func (r *RunResult) String() string {
	var b strings.Builder
	if r.Detached != nil {
		fmt.Fprintf(&b, "exit_code: none (still running)\ndetached: still running after %s, it continues in the background as %s (pid %d) with its timeout unchanged\n",
			r.Duration.Round(time.Second), r.Detached.ID, r.Detached.PID)
		fmt.Fprintf(&b, "next: read_process_output id=%s since_offset=%d until_exit=true\n", r.Detached.ID, r.NextOffset)
		fmt.Fprintf(&b, "cwd: %s\nshell: %s\n", r.Cwd, r.Shell)
	} else {
		fmt.Fprintf(&b, "exit_code: %d\nduration: %s\ncwd: %s\nshell: %s\n", r.ExitCode, r.Duration.Round(time.Millisecond), r.Cwd, r.Shell)
	}
	if r.TimedOut {
		b.WriteString("timed_out: true (the process tree was killed; pass a larger timeout_sec or use start_process)\n")
	}
	if r.Truncated {
		b.WriteString("truncated: true (middle of the output was dropped)\n")
	}
	if r.LogFile != "" {
		fmt.Fprintf(&b, "full_output: %s (read_file or grep it)\n", r.LogFile)
	}
	suffix := ""
	if r.Detached != nil {
		suffix = " so far"
	}
	if r.Stdout != "" {
		b.WriteString("--- stdout" + suffix + " ---\n")
		b.WriteString(strings.TrimRight(r.Stdout, "\r\n"))
		b.WriteByte('\n')
	}
	if r.Stderr != "" {
		b.WriteString("--- stderr" + suffix + " ---\n")
		b.WriteString(strings.TrimRight(r.Stderr, "\r\n"))
		b.WriteByte('\n')
	}
	if r.Stdout == "" && r.Stderr == "" {
		b.WriteString("(no output" + suffix + ")\n")
	}
	return b.String()
}

// prepare validates the request and builds the command without starting it.
// The returned cleanup must be called after the command has exited (or if it
// is never started).
func (r *Runner) prepare(ctx context.Context, command, cwd, shell string, env map[string]string) (*exec.Cmd, string, string, func(), error) {
	if strings.TrimSpace(command) == "" {
		return nil, "", "", nil, errors.New("command is required")
	}
	if cwd == "" {
		cwd = r.Guard.Roots()[0]
	}
	dir, err := r.Guard.Resolve(cwd)
	if err != nil {
		return nil, "", "", nil, err
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, "", "", nil, fmt.Errorf("cwd %s is not an existing directory", dir)
	}
	if shell == "" {
		shell = r.DefaultShell
	}
	cmd, cleanup, err := newShellCmd(ctx, shell, command)
	if err != nil {
		return nil, "", "", nil, err
	}
	cmd.Dir = dir
	cmd.Env = buildEnv(env)
	// Stdin stays nil (the null device) so prompts fail instead of hanging.
	// WaitDelay stops Wait from blocking on grandchildren that keep the
	// output pipes open after the shell has exited.
	cmd.WaitDelay = 3 * time.Second
	return cmd, dir, shell, cleanup, nil
}

// serverOnlyEnv are variables of mcp-shell itself that commands must not
// inherit: the desktop GUI hands the access token over in MCP_SHELL_TOKEN,
// and a command printing its environment would otherwise reveal it.
var serverOnlyEnv = []string{"MCP_SHELL_TOKEN"}

// CommandEnviron is the environment commands start from (before the
// variables mcp-shell adds for git and pagers): the host environment
// without mcp-shell's own variables. diff is what came from the registry.
func CommandEnviron() (env []string, diff hostenv.Diff) {
	all, diff := hostenv.Environ()
	for _, kv := range all {
		k, _, _ := strings.Cut(kv, "=")
		keep := true
		for _, name := range serverOnlyEnv {
			if strings.EqualFold(k, name) {
				keep = false
			}
		}
		if keep {
			env = append(env, kv)
		}
	}
	return env, diff
}

func buildEnv(extra map[string]string) []string {
	base, _ := CommandEnviron()
	env := append(base,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"NO_COLOR=1",
	)
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+extra[k])
	}
	return env
}

// Run executes a command and waits for it, killing the whole process tree on
// timeout or when ctx is cancelled (e.g. the client aborted the tool call).
// A command still running after DetachAfter is handed to Procs instead.
func (r *Runner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	timeout := r.DefaultTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}
	if timeout > r.MaxTimeout {
		timeout = r.MaxTimeout
	}
	// The command does not run under ctx: a detached command must outlive
	// the tool call. ctx still kills it as long as Run is waiting for it.
	runCtx, runCancel := context.WithTimeout(context.Background(), timeout)

	cmd, dir, shell, cleanup, err := r.prepare(runCtx, req.Command, req.Cwd, req.Shell, req.Env)
	if err != nil {
		runCancel()
		return nil, err
	}
	stdout, stderr := newCapBuffer(r.MaxOutputBytes), newCapBuffer(r.MaxOutputBytes)
	ring := newRingLog(procLogBytes)
	var outW, errW io.Writer = io.MultiWriter(stdout, ring), io.MultiWriter(stderr, ring)
	logFile := r.openLog("run", req.Command, dir)
	if logFile != nil {
		outW = &teeWriter{w: outW, f: logFile}
		errW = &teeWriter{w: errW, f: logFile}
	}
	var flush func()
	cmd.Stdout, cmd.Stderr, flush = streamOutput(outW, errW, oneLine(req.Command, 100))

	start := time.Now()
	if err := cmd.Start(); err != nil {
		flush()
		cleanup()
		runCancel()
		logFile.discard()
		return nil, fmt.Errorf("start %s: %w", shell, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var detach <-chan time.Time
	if r.DetachAfter > 0 && r.Procs != nil && timeout > r.DetachAfter {
		t := time.NewTimer(r.DetachAfter)
		defer t.Stop()
		detach = t.C
	}
	var waitErr error
	cancelled := false
wait:
	for {
		select {
		case waitErr = <-done:
			break wait
		case <-ctx.Done():
			cancelled = true
			runCancel()
			waitErr = <-done
			break wait
		case <-detach:
			detach = nil
			p := r.Procs.adopt(adoptedRun{
				cmd: cmd, command: req.Command, shell: shell, cwd: dir, started: start,
				ring: ring, logFile: logFile, ctx: runCtx, cancel: runCancel, done: done,
				flush: flush, cleanup: cleanup,
			})
			if p == nil {
				continue // too many background processes; keep waiting here
			}
			res := &RunResult{Command: req.Command, Cwd: dir, Shell: shell, ExitCode: -1, Duration: time.Since(start), Detached: p, NextOffset: ring.End()}
			res.Stdout, _ = stdout.Result()
			res.Stderr, _ = stderr.Result()
			return res, nil
		}
	}
	flush()
	cleanup()
	res := &RunResult{Command: req.Command, Cwd: dir, Shell: shell, Duration: time.Since(start)}
	switch {
	case cancelled:
		res.ExitCode = -1
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		res.TimedOut, res.ExitCode = true, -1
	case waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) && !isExitError(waitErr):
		runCancel()
		logFile.discard()
		return nil, waitErr
	default:
		res.ExitCode = exitCodeOf(cmd, waitErr)
	}
	runCancel()
	var t1, t2 bool
	res.Stdout, t1 = stdout.Result()
	res.Stderr, t2 = stderr.Result()
	res.Truncated = t1 || t2
	logFile.close()
	if res.Truncated {
		res.LogFile = logFile.keep()
	} else {
		logFile.discard()
	}
	return res, nil
}

func isExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}
