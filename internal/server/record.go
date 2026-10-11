package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noovertime7/mcp-shell/internal/history"
)

type clientKey struct{}

func withClient(ctx context.Context, client string) context.Context {
	return context.WithValue(ctx, clientKey{}, client)
}

func clientFrom(ctx context.Context) string {
	s, _ := ctx.Value(clientKey{}).(string)
	return s
}

// clientLabel is the remote address plus the product part of the user agent,
// e.g. "192.168.31.76 claude-code/2.1.0".
func clientLabel(ip, userAgent string) string {
	ua, _, _ := strings.Cut(strings.TrimSpace(userAgent), " ")
	if len(ua) > 60 {
		ua = ua[:60]
	}
	return strings.TrimSpace(ip + " " + ua)
}

// callStatus classifies a finished call for the history and the log.
func callStatus(name, out string, err error) (status string, exitCode *int) {
	if err != nil {
		return "error", nil
	}
	if name != "run_command" {
		return "ok", nil
	}
	first, _, _ := strings.Cut(out, "\n")
	v := strings.TrimSpace(strings.TrimPrefix(first, "exit_code:"))
	if strings.HasPrefix(v, "none") {
		return "running", nil
	}
	code, convErr := strconv.Atoi(v)
	if convErr != nil {
		return "ok", nil
	}
	switch {
	case strings.Contains(out, "\ntimed_out: true"):
		return "timeout", &code
	case code != 0:
		return "failed", &code
	}
	return "ok", &code
}

const (
	historyResultLines = 40
	historyResultBytes = 4000
	historyArgBytes    = 4000
)

// record writes one call to the history; failures only reach the log.
func (s *Server) record(ctx context.Context, name string, in any, out string, err error, start time.Time, elapsed time.Duration) {
	if s.history == nil {
		return
	}
	status, code := callStatus(name, out, err)
	e := history.Entry{
		Time:        start,
		Tool:        name,
		Args:        summarizeArgs(in, historyArgBytes),
		Status:      status,
		ExitCode:    code,
		DurationMs:  elapsed.Milliseconds(),
		ResultBytes: len(out),
		Client:      clientFrom(ctx),
	}
	if err != nil {
		e.Error = clipBytes(err.Error(), historyResultBytes)
	} else if !readOnlyTools[name] {
		e.Result = headLines(out, historyResultLines, historyResultBytes)
	}
	if _, werr := s.history.Add(e); werr != nil {
		log.Printf("[历史] 写入操作历史失败: %v", werr)
	}
}

func headLines(s string, lines, bytes int) string {
	parts := strings.SplitN(s, "\n", lines+1)
	if len(parts) > lines {
		parts = parts[:lines]
	}
	return clipBytes(strings.Join(parts, "\n"), bytes)
}

// formatHistoryList renders a page of entries, one line each.
func formatHistoryList(h *history.Store, entries []history.Entry, total, offset int) string {
	var b strings.Builder
	if total == 0 {
		fmt.Fprintf(&b, "no matching calls in the history (%s, kept %d days)\n", h.Dir(), h.KeepDays())
		return b.String()
	}
	fmt.Fprintf(&b, "history: %s (kept %d days); calls %d-%d of %d matching, newest first\n",
		h.Dir(), h.KeepDays(), offset+1, offset+len(entries), total)
	today := time.Now().Format("2006-01-02")
	for i := range entries {
		e := &entries[i]
		when := e.Time.Format("01-02 15:04:05")
		if e.Time.Format("2006-01-02") == today {
			when = e.Time.Format("15:04:05")
		}
		result := strings.ToUpper(e.Status)
		if e.Status == "ok" {
			result = "ok"
		}
		if e.ExitCode != nil && *e.ExitCode != 0 {
			result += fmt.Sprintf(" exit=%d", *e.ExitCode)
		}
		fmt.Fprintf(&b, "#%d  %s  %-19s %-14s %7s  %s\n", e.ID, when, e.Tool, result, shortDuration(e.DurationMs), describeEntry(e))
	}
	if rest := total - offset - len(entries); rest > 0 {
		fmt.Fprintf(&b, "… %d more; pass offset=%d. Pass id=<n> for the details of one call.\n", rest, offset+len(entries))
	} else {
		b.WriteString("Pass id=<n> for the arguments and output of one call.\n")
	}
	return b.String()
}

func shortDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", ms)
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// describeEntry is the most telling argument of a call, on one line.
func describeEntry(e *history.Entry) string {
	str := func(k string) string {
		v, _ := e.Args[k].(string)
		return v
	}
	var s string
	switch {
	case str("command") != "":
		s = oneLine(str("command"), 160)
		if cwd := str("cwd"); cwd != "" {
			s += "  (cwd " + cwd + ")"
		}
	case str("src") != "":
		s = str("src") + " -> " + str("dst")
	case str("pattern") != "":
		s = strconv.Quote(oneLine(str("pattern"), 80))
		if p := str("path"); p != "" {
			s += " in " + p
		}
	case str("path") != "":
		s = str("path")
		if edits, ok := e.Args["edits"].([]any); ok {
			s += fmt.Sprintf("  (%d edits)", len(edits))
		}
	case str("id") != "":
		s = str("id")
	case str("name") != "":
		s = str("name")
	}
	if e.Error != "" {
		s += "  error: " + oneLine(e.Error, 120)
	}
	return s
}

// plainJSON encodes v without escaping <, > and &, which are common in
// commands and would be hard to read as <.
func plainJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatHistoryEntry renders all that is kept of one call.
func formatHistoryEntry(e *history.Entry) string {
	var b strings.Builder
	result := e.Status
	if e.ExitCode != nil {
		result += fmt.Sprintf(" (exit_code %d)", *e.ExitCode)
	}
	fmt.Fprintf(&b, "#%d %s  %s\n", e.ID, e.Tool, result)
	fmt.Fprintf(&b, "time: %s, took %s\n", e.Time.Format("2006-01-02 15:04:05 -07:00"), shortDuration(e.DurationMs))
	if e.Client != "" {
		fmt.Fprintf(&b, "client: %s\n", e.Client)
	}
	if len(e.Args) > 0 {
		b.WriteString("args:\n")
		keys := make([]string, 0, len(e.Args))
		for k := range e.Args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := e.Args[k]
			if str, ok := v.(string); ok {
				fmt.Fprintf(&b, "  %s: %s\n", k, str)
				continue
			}
			fmt.Fprintf(&b, "  %s: %s\n", k, plainJSON(v))
		}
	}
	if e.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", e.Error)
	}
	if e.Result != "" {
		fmt.Fprintf(&b, "result (start of %d bytes):\n%s\n", e.ResultBytes, strings.TrimRight(e.Result, "\n"))
	} else if e.ResultBytes > 0 {
		fmt.Fprintf(&b, "result: %d bytes (output of read-only tools is not kept)\n", e.ResultBytes)
	}
	return b.String()
}
