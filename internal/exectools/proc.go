package exectools

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxProcs        = 20
	procLogBytes    = 1 << 20
	maxReadChunk    = 40_000
	maxReadWait     = 30 * time.Second
	stopGracePeriod = 10 * time.Second
)

// Manager tracks background processes started with start_process.
type Manager struct {
	Runner *Runner

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
	ended    time.Time
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
	return fmt.Sprintf("exited (exit_code %d, after %s)", p.exitCode, p.ended.Sub(p.Started).Round(time.Second))
}

// Start launches command in the background and returns immediately.
func (m *Manager) Start(command, cwd, shell, name string, env map[string]string) (*Proc, error) {
	m.mu.Lock()
	live := 0
	for _, p := range m.procs {
		if p.running() {
			live++
		}
	}
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
	if err := cmd.Start(); err != nil {
		cancel()
		cleanup()
		logFile.discard()
		return nil, fmt.Errorf("start %s: %w", shell, err)
	}

	m.mu.Lock()
	m.next++
	p := &Proc{
		ID:      fmt.Sprintf("p%d", m.next),
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
	}
	m.procs[p.ID] = p
	m.mu.Unlock()

	go func() {
		err := cmd.Wait()
		flush()
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		logFile.close()
		cleanup()
		p.mu.Lock()
		p.exitCode, p.ended = code, time.Now()
		p.mu.Unlock()
		close(p.done)
		cancel()
		log.Printf("[后台进程] %s 已退出 | 退出码=%d | 命令=%s", p.ID, code, oneLine(p.Command, 100))
	}()
	return p, nil
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

// Read returns output written since offset. With waitMs > 0 it waits up to
// that long for new output or for the process to exit.
func (m *Manager) Read(id string, since int64, waitMs int) (string, error) {
	p, err := m.get(id)
	if err != nil {
		return "", err
	}
	if waitMs > 0 {
		wait := time.Duration(waitMs) * time.Millisecond
		if wait > maxReadWait {
			wait = maxReadWait
		}
		deadline := time.Now().Add(wait)
		for p.log.End() <= since && p.running() && time.Now().Before(deadline) {
			select {
			case <-p.done:
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	data, from, dropped := p.log.Read(since, maxReadChunk)
	if p.running() {
		// Leave a split UTF-8 sequence for the next read.
		data = trimPartialEnd(data)
	}
	next := from + int64(len(data))

	var b strings.Builder
	fmt.Fprintf(&b, "id: %s\nstatus: %s\nnext_offset: %d\n", p.ID, p.status(), next)
	if dropped {
		fmt.Fprintf(&b, "note: older output was discarded; showing from offset %d\n", from)
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
