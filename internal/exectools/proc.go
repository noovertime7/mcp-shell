package exectools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxProcs        = 20
	procLogBytes    = 1 << 20
	maxReadChunk    = 40_000
	defaultMaxWait  = 45 * time.Second
	stopGracePeriod = 10 * time.Second
)

// Manager tracks background processes: those started with start_process and
// run_command calls that were still running when the call had to return.
type Manager struct {
	Runner *Runner
	// MaxWait caps how long one read_process_output call may wait, so the
	// call returns before the client gives up on it. Zero means 45s.
	MaxWait time.Duration

	mu    sync.Mutex
	next  int
	procs map[string]*Proc
}

// Proc is one background process.
type Proc struct {
	ID      string
	Name    string
	Command string
	Shell   string
	Cwd     string
	PID     int
	Started time.Time
	// LogFile is the full output on disk (host path), empty without LogDir.
	LogFile string

	log    *ringLog
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	exitCode int
	timedOut bool
	ended    time.Time

	stdin   io.WriteCloser
	stdinMu sync.Mutex
}

// NewManager returns an empty process manager.
func NewManager(r *Runner) *Manager {
	return &Manager{Runner: r, procs: map[string]*Proc{}}
}

func (p *Proc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *Proc) status() string {
	if p.running() {
		return fmt.Sprintf("running (pid %d, up %s)", p.PID, time.Since(p.Started).Round(time.Second))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timedOut {
		return fmt.Sprintf("killed: timed out after %s", p.ended.Sub(p.Started).Round(time.Second))
	}
	return fmt.Sprintf("exited (exit_code %d, after %s)", p.exitCode, p.ended.Sub(p.Started).Round(time.Second))
}

// liveCount counts running processes; m.mu must be held.
func (m *Manager) liveCount() int {
	live := 0
	for _, p := range m.procs {
		if p.running() {
			live++
		}
	}
	return live
}

// register stores p under a new id, unless too many processes are running.
func (m *Manager) register(p *Proc) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.liveCount() >= maxProcs {
		return false
	}
	m.next++
	p.ID = fmt.Sprintf("p%d", m.next)
	m.procs[p.ID] = p
	return true
}

// Start launches command in the background and returns immediately.
func (m *Manager) Start(command, cwd, shell, name string, env map[string]string) (*Proc, error) {
	m.mu.Lock()
	live := m.liveCount()
	m.mu.Unlock()
	if live >= maxProcs {
		return nil, fmt.Errorf("already %d background processes running; stop some first", live)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd, dir, shell, cleanup, err := m.Runner.prepare(ctx, command, cwd, shell, env)
	if err != nil {
		cancel()
		return nil, err
	}
	ring := newRingLog(procLogBytes)
	cmd.Stdout, cmd.Stderr = ring, ring
	logFile := m.Runner.openLog("proc", command, dir)
	if logFile != nil {
		tee := &teeWriter{w: ring, f: logFile}
		cmd.Stdout, cmd.Stderr = tee, tee
	}
	var flush func()
	cmd.Stdout, cmd.Stderr, flush = streamOutput(cmd.Stdout, cmd.Stderr, "后台任务 "+oneLine(command, 100))
	var stdinPipe io.WriteCloser
	if pipe, err := cmd.StdinPipe(); err == nil {
		stdinPipe = pipe
	}
	if err := cmd.Start(); err != nil {
		if stdinPipe != nil {
			_ = stdinPipe.Close()
		}
		cancel()
		cleanup()
		logFile.discard()
		return nil, fmt.Errorf("start %s: %w", shell, err)
	}

	p := &Proc{
		Name:    name,
		Command: command,
		Shell:   shell,
		Cwd:     dir,
		PID:     cmd.Process.Pid,
		Started: time.Now(),
		LogFile: logFile.keep(),
		log:     ring,
		cancel:  cancel,
		done:    make(chan struct{}),
		stdin:   stdinPipe,
	}
	if !m.register(p) {
		cancel()
		_ = cmd.Wait()
		flush()
		cleanup()
		logFile.discard()
		return nil, fmt.Errorf("already %d background processes running; stop some first", maxProcs)
	}
	// cmd.Wait must run inside the goroutine, not as an argument of it.
	go func() { p.finish(cmd, cmd.Wait(), ctx, flush, logFile, cleanup) }()
	return p, nil
}

// adoptedRun is a running run_command handed over to the manager.
type adoptedRun struct {
	cmd                 *exec.Cmd
	command, shell, cwd string
	started             time.Time
	ring                *ringLog
	logFile             *outputLog
	ctx                 context.Context
	cancel              context.CancelFunc
	done                <-chan error
	flush, cleanup      func()
}

// adopt turns a running run_command into a background process. It returns
// nil when too many background processes are running already.
func (m *Manager) adopt(a adoptedRun) *Proc {
	p := &Proc{
		Name:    "run_command",
		Command: a.command,
		Shell:   a.shell,
		Cwd:     a.cwd,
		PID:     a.cmd.Process.Pid,
		Started: a.started,
		LogFile: a.logFile.keep(),
		log:     a.ring,
		cancel:  a.cancel,
		done:    make(chan struct{}),
	}
	if !m.register(p) {
		return nil
	}
	log.Printf("[后台进程] 命令运行超过等待上限，转为后台进程 %s | 命令=%s", p.ID, oneLine(a.command, 100))
	go func() { p.finish(a.cmd, <-a.done, a.ctx, a.flush, a.logFile, a.cleanup) }()
	return p
}

// finish records how the process ended and releases what it held.
func (p *Proc) finish(cmd *exec.Cmd, waitErr error, ctx context.Context, flush func(), logFile *outputLog, cleanup func()) {
	flush()
	p.stdinMu.Lock()
	if p.stdin != nil {
		_ = p.stdin.Close()
		p.stdin = nil
	}
	p.stdinMu.Unlock()
	code := exitCodeOf(cmd, waitErr)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	logFile.close()
	cleanup()
	p.mu.Lock()
	p.exitCode, p.timedOut, p.ended = code, timedOut, time.Now()
	p.mu.Unlock()
	close(p.done)
	p.cancel()
	log.Printf("[后台进程] %s 已退出 | 退出码=%d | 命令=%s", p.ID, code, oneLine(p.Command, 100))
}

func (m *Manager) get(id string) (*Proc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[id]
	if !ok {
		return nil, fmt.Errorf("no background process %q; call list_processes", id)
	}
	return p, nil
}

// ReadOptions are the parameters of Read.
type ReadOptions struct {
	// Since is the next_offset of the previous read; 0 reads from the start.
	Since int64
	// WaitMs waits up to this long (capped at MaxWait) for something to return.
	WaitMs int
	// UntilExit waits for the process to exit rather than for new output.
	UntilExit bool
	// WaitFor waits until the new output matches this regular expression.
	WaitFor string
	// Tail returns the end of the new output instead of its start when there
	// is more than one chunk, e.g. the summary of a finished test run.
	Tail bool
}

// MaxWaitDuration is the longest one Read may wait.
func (m *Manager) MaxWaitDuration() time.Duration {
	if m.MaxWait > 0 {
		return m.MaxWait
	}
	return defaultMaxWait
}

// Read returns output written since o.Since, after waiting as o asks.
func (m *Manager) Read(id string, o ReadOptions) (string, error) {
	p, err := m.get(id)
	if err != nil {
		return "", err
	}
	var re *regexp.Regexp
	if o.WaitFor != "" {
		if re, err = regexp.Compile(o.WaitFor); err != nil {
			return "", fmt.Errorf("invalid wait_for regexp (Go RE2 syntax): %w", err)
		}
	}
	matched := func() bool {
		data, _, _ := p.log.Read(o.Since, procLogBytes)
		return re.Match(data)
	}
	waitedOut := false
	isMatch := false
	if o.WaitMs > 0 || o.UntilExit || re != nil {
		wait := time.Duration(o.WaitMs) * time.Millisecond
		if wait <= 0 || wait > m.MaxWaitDuration() {
			wait = m.MaxWaitDuration()
		}
		deadline := time.Now().Add(wait)
		checked := int64(-1)
		for p.running() {
			end := p.log.End()
			if re != nil {
				if end != checked {
					checked = end
					if isMatch = matched(); isMatch {
						break
					}
				}
			} else if !o.UntilExit && end > o.Since {
				break
			}
			if !time.Now().Before(deadline) {
				waitedOut = true
				break
			}
			select {
			case <-p.done:
			case <-time.After(200 * time.Millisecond):
			}
		}
		if re != nil && !isMatch {
			isMatch = matched()
		}
	}

	end := p.log.End()
	since := o.Since
	skipped := int64(0)
	if o.Tail && end-since > maxReadChunk {
		skipped = end - maxReadChunk - since
		since = end - maxReadChunk
	}
	data, from, dropped := p.log.Read(since, maxReadChunk)
	if o.Tail && len(data) > 0 {
		data = trimPartialStart(data)
	}
	if p.running() {
		// Leave a split UTF-8 sequence for the next read.
		data = trimPartialEnd(data)
	}
	next := from + int64(len(data))

	var b strings.Builder
	fmt.Fprintf(&b, "id: %s\nstatus: %s\nnext_offset: %d\n", p.ID, p.status(), next)
	if re != nil {
		switch {
		case isMatch:
			b.WriteString("wait_for: matched\n")
		case p.running():
			fmt.Fprintf(&b, "wait_for: not matched yet (waited %s; call again to keep waiting)\n", m.MaxWaitDuration().Round(time.Second))
		default:
			b.WriteString("wait_for: not matched; the process has exited\n")
		}
	} else if o.UntilExit && waitedOut && p.running() {
		fmt.Fprintf(&b, "until_exit: still running after this wait; call again with since_offset=%d until_exit=true\n", next)
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "note: skipped %d bytes to show the last part of the output\n", skipped)
	}
	if dropped || skipped > 0 {
		if dropped {
			fmt.Fprintf(&b, "note: older output was discarded; showing from offset %d\n", from)
		}
		if p.LogFile != "" {
			fmt.Fprintf(&b, "full_output: %s\n", p.LogFile)
		}
	}
	if more := p.log.End() - next; more > 0 {
		fmt.Fprintf(&b, "more: %d bytes not shown yet; read again with since_offset=%d\n", more, next)
	}
	b.WriteString("--- output ---\n")
	if len(data) == 0 {
		b.WriteString("(no new output)\n")
	} else {
		b.WriteString(decode(data))
		if data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

// List describes all tracked processes.
func (m *Manager) List() string {
	m.mu.Lock()
	procs := make([]*Proc, 0, len(m.procs))
	for _, p := range m.procs {
		procs = append(procs, p)
	}
	m.mu.Unlock()
	if len(procs) == 0 {
		return "no background processes"
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].Started.Before(procs[j].Started) })
	var b strings.Builder
	for _, p := range procs {
		name := p.Name
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(&b, "%s  name=%s  %s  output=%dB  cwd=%s\n    %s\n", p.ID, name, p.status(), p.log.End(), p.Cwd, oneLine(p.Command, 200))
		if p.LogFile != "" {
			fmt.Fprintf(&b, "    log: %s\n", p.LogFile)
		}
	}
	return b.String()
}

// Stop kills a process tree (if still running) and forgets it.
func (m *Manager) Stop(id string) (string, error) {
	p, err := m.get(id)
	if err != nil {
		return "", err
	}
	if p.running() {
		p.cancel()
		select {
		case <-p.done:
		case <-time.After(stopGracePeriod):
			return "", fmt.Errorf("process %s (pid %d) did not exit within %s", p.ID, p.PID, stopGracePeriod)
		}
	}
	m.mu.Lock()
	delete(m.procs, id)
	m.mu.Unlock()
	return fmt.Sprintf("%s stopped: %s", p.ID, p.status()), nil
}

// StopAll kills every running process; used on shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	procs := make([]*Proc, 0, len(m.procs))
	for _, p := range m.procs {
		procs = append(procs, p)
	}
	m.mu.Unlock()
	for _, p := range procs {
		if p.running() {
			p.cancel()
		}
	}
	for _, p := range procs {
		select {
		case <-p.done:
		case <-time.After(stopGracePeriod):
		}
	}
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// WriteInput sends input to the process's standard input.
func (p *Proc) WriteInput(input string, closeStdin bool) error {
	if !p.running() {
		return fmt.Errorf("process %s is not running", p.ID)
	}
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	if p.stdin == nil {
		return fmt.Errorf("process %s stdin is not available or already closed", p.ID)
	}
	if _, err := io.WriteString(p.stdin, input); err != nil {
		return fmt.Errorf("write stdin to %s: %w", p.ID, err)
	}
	if closeStdin {
		_ = p.stdin.Close()
		p.stdin = nil
	}
	return nil
}

// WriteInput sends input to a background process's stdin.
func (m *Manager) WriteInput(id, input string, closeStdin bool) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	return p.WriteInput(input, closeStdin)
}

// ProcInfo is structured data about a tracked background process.
type ProcInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
	Shell   string `json:"shell"`
	Cwd     string `json:"cwd"`
	PID     int    `json:"pid"`
	Started string `json:"started"`
	Running bool   `json:"running"`
	Status  string `json:"status"`
	LogFile string `json:"log_file,omitempty"`
}

// ListInfo returns structured info of all tracked background processes.
func (m *Manager) ListInfo() []ProcInfo {
	m.mu.Lock()
	procs := make([]*Proc, 0, len(m.procs))
	for _, p := range m.procs {
		procs = append(procs, p)
	}
	m.mu.Unlock()
	sort.Slice(procs, func(i, j int) bool { return procs[i].Started.Before(procs[j].Started) })
	infos := make([]ProcInfo, 0, len(procs))
	for _, p := range procs {
		name := p.Name
		if name == "" {
			name = "-"
		}
		infos = append(infos, ProcInfo{
			ID:      p.ID,
			Name:    name,
			Command: p.Command,
			Shell:   p.Shell,
			Cwd:     p.Cwd,
			PID:     p.PID,
			Started: p.Started.Format("2006-01-02 15:04:05"),
			Running: p.running(),
			Status:  p.status(),
			LogFile: p.LogFile,
		})
	}
	return infos
}
