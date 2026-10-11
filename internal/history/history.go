// Package history keeps every tool call on disk, one JSON line per call in a
// file per day, so a later session can look up what was done on this host
// (by itself or by other sessions) and how it turned out.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const dayLayout = "2006-01-02"

// Entry is one tool call.
type Entry struct {
	ID         int64          `json:"id"`
	Time       time.Time      `json:"time"`
	Tool       string         `json:"tool"`
	Args       map[string]any `json:"args,omitempty"`
	Status     string         `json:"status"` // ok, failed, error, timeout, running
	ExitCode   *int           `json:"exit_code,omitempty"`
	DurationMs int64          `json:"duration_ms"`
	Error      string         `json:"error,omitempty"`
	// Result is the head of the output of calls that change something.
	Result string `json:"result,omitempty"`
	// ResultBytes is the full size of the output.
	ResultBytes int    `json:"result_bytes,omitempty"`
	Client      string `json:"client,omitempty"`
}

// Failed reports whether the call did not succeed.
func (e *Entry) Failed() bool { return e.Status != "ok" && e.Status != "running" }

// Store appends entries to <dir>/<day>.jsonl and drops days older than keep.
type Store struct {
	dir  string
	keep time.Duration

	mu        sync.Mutex
	next      int64
	lastClean string
}

// Open prepares dir and continues the id sequence of the existing files.
func Open(dir string, keepDays int) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create history dir: %w", err)
	}
	if keepDays <= 0 {
		keepDays = 30
	}
	s := &Store{dir: dir, keep: time.Duration(keepDays) * 24 * time.Hour}
	s.clean(time.Now())
	for _, f := range s.files() {
		if id := lastID(filepath.Join(dir, f)); id > 0 {
			s.next = id
			break
		}
	}
	return s, nil
}

// Dir is where the history is kept.
func (s *Store) Dir() string { return s.dir }

// KeepDays is how many days of history are kept.
func (s *Store) KeepDays() int { return int(s.keep / (24 * time.Hour)) }

// Add stores e with the next id and returns it.
func (s *Store) Add(e Entry) (int64, error) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if day := e.Time.Format(dayLayout); day != s.lastClean {
		s.clean(e.Time)
	}
	s.next++
	e.ID = s.next
	line, err := json.Marshal(e)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, e.Time.Format(dayLayout)+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return 0, err
	}
	return e.ID, nil
}

// Query selects entries; zero values mean "any".
type Query struct {
	Limit, Offset int
	// Tool matches the tool name exactly.
	Tool string
	// Text matches, case-insensitively, the arguments, result and error.
	Text string
	// FailedOnly keeps failed, timed-out and errored calls.
	FailedOnly bool
	// ChangesOnly drops calls of tools in ReadOnly.
	ChangesOnly bool
	ReadOnly    map[string]bool
	// Exclude drops these tools unless Tool names one of them.
	Exclude map[string]bool
	Since   time.Time
}

// Find returns matching entries, newest first, and how many match in total.
func (s *Store) Find(q Query) ([]Entry, int, error) {
	if q.Limit <= 0 {
		q.Limit = 30
	}
	text := strings.ToLower(q.Text)
	var out []Entry
	total := 0
	for _, f := range s.files() {
		if !q.Since.IsZero() && f < q.Since.Format(dayLayout)+".jsonl" {
			break
		}
		entries, err := readFile(filepath.Join(s.dir, f))
		if err != nil {
			return nil, 0, err
		}
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			switch {
			case !q.Since.IsZero() && e.Time.Before(q.Since):
				continue
			case q.Tool != "" && e.Tool != q.Tool:
				continue
			case q.Tool == "" && q.Exclude[e.Tool]:
				continue
			case q.FailedOnly && !e.Failed():
				continue
			case q.ChangesOnly && q.ReadOnly[e.Tool]:
				continue
			case text != "" && !strings.Contains(strings.ToLower(searchText(&e)), text):
				continue
			}
			total++
			if total > q.Offset && len(out) < q.Limit {
				out = append(out, e)
			}
		}
	}
	return out, total, nil
}

// Get returns the entry with id.
func (s *Store) Get(id int64) (*Entry, error) {
	for _, f := range s.files() {
		entries, err := readFile(filepath.Join(s.dir, f))
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 && entries[0].ID > id {
			continue
		}
		for i := range entries {
			if entries[i].ID == id {
				return &entries[i], nil
			}
		}
	}
	return nil, fmt.Errorf("no history entry #%d (it may be older than %d days)", id, s.KeepDays())
}

func searchText(e *Entry) string {
	args, _ := json.Marshal(e.Args)
	return string(args) + "\n" + e.Result + "\n" + e.Error
}

// files lists the day files, newest first.
func (s *Store) files() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			if _, err := time.Parse(dayLayout, strings.TrimSuffix(e.Name(), ".jsonl")); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names
}

// clean drops day files older than keep; s.mu is held or s is not shared yet.
func (s *Store) clean(now time.Time) {
	s.lastClean = now.Format(dayLayout)
	cutoff := now.Add(-s.keep).Format(dayLayout) + ".jsonl"
	for _, f := range s.files() {
		if f < cutoff {
			os.Remove(filepath.Join(s.dir, f))
		}
	}
}

func readFile(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID > 0 {
			out = append(out, e)
		}
	}
	return out, nil
}

func lastID(path string) int64 {
	entries, _ := readFile(path)
	var max int64
	for _, e := range entries {
		if e.ID > max {
			max = e.ID
		}
	}
	return max
}

// ParseSince accepts a duration back from now ("30m", "2h", "3d") or a
// date / time ("2026-10-11", "2026-10-11 15:04", RFC 3339) in local time.
func ParseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return now.Add(-time.Duration(days) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04", dayLayout} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("since %q: use a duration such as 30m, 2h or 3d, or a time such as 2026-10-11 or 2026-10-11 15:04", s)
}
