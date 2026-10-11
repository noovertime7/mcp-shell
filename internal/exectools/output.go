package exectools

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// capBuffer keeps the first and last max/2 bytes written to it and counts
// what it dropped in between, so huge build logs keep their head and tail.
type capBuffer struct {
	mu    sync.Mutex
	half  int
	head  []byte
	tail  []byte
	total int64
}

func newCapBuffer(max int) *capBuffer {
	half := max / 2
	if half < 1 {
		half = 1
	}
	return &capBuffer{half: half}
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	c.total += int64(n)
	if room := c.half - len(c.head); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		c.head = append(c.head, p[:room]...)
		p = p[room:]
	}
	if len(p) > 0 {
		c.tail = append(c.tail, p...)
		if len(c.tail) > 2*c.half {
			c.tail = append([]byte(nil), c.tail[len(c.tail)-c.half:]...)
		}
	}
	return n, nil
}

// Result returns the captured text and whether anything was dropped.
func (c *capBuffer) Result() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tail := c.tail
	if len(tail) > c.half {
		tail = tail[len(tail)-c.half:]
	}
	dropped := c.total - int64(len(c.head)) - int64(len(tail))
	if dropped <= 0 {
		all := append(append([]byte(nil), c.head...), tail...)
		return decode(all), false
	}
	return decode(trimPartialEnd(c.head)) +
		fmt.Sprintf("\n…[truncated %d bytes]…\n", dropped) +
		decode(trimPartialStart(tail)), true
}

// ringLog keeps the most recent max bytes of a long-running process's output
// and addresses them by absolute byte offset.
type ringLog struct {
	mu   sync.Mutex
	max  int
	buf  []byte
	base int64 // absolute offset of buf[0]
}

func newRingLog(max int) *ringLog { return &ringLog{max: max} }

func (r *ringLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max+r.max/2 {
		drop := len(r.buf) - r.max
		r.buf = append([]byte(nil), r.buf[drop:]...)
		r.base += int64(drop)
	}
	return len(p), nil
}

// End returns the absolute offset just past the last byte written.
func (r *ringLog) End() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.base + int64(len(r.buf))
}

// Read returns up to limit bytes starting at since. dropped reports that some
// output between since and the oldest retained byte was discarded.
func (r *ringLog) Read(since int64, limit int) (data []byte, from int64, dropped bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	end := r.base + int64(len(r.buf))
	from = since
	if from < r.base {
		from, dropped = r.base, true
	}
	if from > end {
		from = end
	}
	to := end
	if to-from > int64(limit) {
		to = from + int64(limit)
	}
	return append([]byte(nil), r.buf[from-r.base:to-r.base]...), from, dropped
}

// decode turns process output into a string. Output that is not valid UTF-8
// is decoded with the system code page (e.g. CP936/CP950 on Chinese Windows).
func decode(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	if s, ok := decodeSystemCodePage(b); ok {
		return s
	}
	return strings.ToValidUTF8(string(b), "�")
}

// trimPartialEnd drops an incomplete UTF-8 sequence at the end of b.
func trimPartialEnd(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

// trimPartialStart drops UTF-8 continuation bytes at the start of b.
func trimPartialStart(b []byte) []byte {
	for i := 0; i < len(b) && i < utf8.UTFMax; i++ {
		if utf8.RuneStart(b[i]) {
			return b[i:]
		}
	}
	return b
}

// outputLog is the on-disk copy of a command's full output, so output that
// was truncated for the model can still be read or grepped afterwards. All
// methods are no-ops on a nil *outputLog, which means logging is off.
type outputLog struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// openLog creates a log file in LogDir. Failure only disables the log.
func (r *Runner) openLog(kind, command, cwd string) *outputLog {
	if r.LogDir == "" {
		return nil
	}
	f, err := os.CreateTemp(r.LogDir, kind+"-"+time.Now().Format("20060102-150405")+"-*.log")
	if err != nil {
		return nil
	}
	fmt.Fprintf(f, "# command: %s\n# cwd: %s\n# started: %s\n\n", oneLine(command, 2000), cwd, time.Now().Format(time.RFC3339))
	return &outputLog{f: f, path: f.Name()}
}

func (l *outputLog) write(p []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Write(p)
	}
}

func (l *outputLog) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// discard closes and deletes the log.
func (l *outputLog) discard() {
	if l == nil {
		return
	}
	l.close()
	os.Remove(l.path)
}

// keep returns the log's host path; the client reads it through mcp-shell.
func (l *outputLog) keep() string {
	if l == nil {
		return ""
	}
	return l.path
}

// teeWriter copies output to the log file without letting a disk error
// break the process's output pipe.
type teeWriter struct {
	w io.Writer
	f *outputLog
}

func (t *teeWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	t.f.write(p)
	return n, err
}

// CleanLogs removes log files in dir older than maxAge.
func CleanLogs(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".log") || !(strings.HasPrefix(name, "run-") || strings.HasPrefix(name, "proc-")) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(dir + string(os.PathSeparator) + name)
		}
	}
}
