// Package exectools runs shell commands on the host: one-shot commands with a
// timeout and long-running background processes such as dev servers.
package exectools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

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
}

// RunRequest is the input of Run.
type RunRequest struct {
	Command    string
	Cwd        string
	Shell      string
	TimeoutSec int
	Env        map[string]string
}

// RunResult is the outcome of a finished command.
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
}

// String renders the result for the model.
func (r *RunResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "exit_code: %d\nduration: %s\ncwd: %s\nshell: %s\n", r.ExitCode, r.Duration.Round(time.Millisecond), r.Cwd, r.Shell)
	if r.TimedOut {
		b.WriteString("timed_out: true (the process tree was killed; use start_process for long-running commands)\n")
	}
	if r.Truncated {
		b.WriteString("truncated: true (middle of the output was dropped)\n")
	}
	if r.LogFile != "" {
		fmt.Fprintf(&b, "full_output: %s (read_file or grep it)\n", r.LogFile)
	}
	if r.Stdout != "" {
		b.WriteString("--- stdout ---\n")
		b.WriteString(strings.TrimRight(r.Stdout, "\r\n"))
		b.WriteByte('\n')
	}
	if r.Stderr != "" {
		b.WriteString("--- stderr ---\n")
		b.WriteString(strings.TrimRight(r.Stderr, "\r\n"))
		b.WriteByte('\n')
	}
	if r.Stdout == "" && r.Stderr == "" {
		b.WriteString("(no output)\n")
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

func buildEnv(extra map[string]string) []string {
	env := append(os.Environ(),
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
func (r *Runner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	timeout := r.DefaultTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}
	if timeout > r.MaxTimeout {
		timeout = r.MaxTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd, dir, shell, cleanup, err := r.prepare(ctx, req.Command, req.Cwd, req.Shell, req.Env)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	stdout, stderr := newCapBuffer(r.MaxOutputBytes), newCapBuffer(r.MaxOutputBytes)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	logFile := r.openLog("run", req.Command, dir)
	if logFile != nil {
		cmd.Stdout = &teeWriter{w: stdout, f: logFile}
		cmd.Stderr = &teeWriter{w: stderr, f: logFile}
	}
	var flush func()
	cmd.Stdout, cmd.Stderr, flush = streamOutput(cmd.Stdout, cmd.Stderr, oneLine(req.Command, 100))
	defer flush()

	start := time.Now()
	if err := cmd.Start(); err != nil {
		logFile.discard()
		return nil, fmt.Errorf("start %s: %w", shell, err)
	}
	waitErr := cmd.Wait()
	res := &RunResult{Command: req.Command, Cwd: dir, Shell: shell, Duration: time.Since(start)}

	var exitErr *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut, res.ExitCode = true, -1
	case ctx.Err() != nil:
		res.ExitCode = -1
	case errors.As(waitErr, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	case waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay):
		logFile.discard()
		return nil, waitErr
	default:
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
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
