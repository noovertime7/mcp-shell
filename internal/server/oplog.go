package server

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"
)

// readOnlyTools only look at things; their output is not echoed to the log.
var readOnlyTools = map[string]bool{
	"list_roots": true, "list_dir": true, "read_file": true, "glob": true, "grep": true,
	"read_process_output": true, "list_processes": true,
}

// logCall writes one human-readable operation-log entry per tool call:
//
//	[tool] edit_file  OK 12ms  path=web/src/a.ts  -3/+5 lines
//	       => replaced 1 occurrence(s) in D:\ws\web\src\a.ts
//	       - old: …
//	       + new: …
func (s *Server) logCall(name string, in any, out string, err error, elapsed time.Duration) {
	status := "OK"
	if err != nil {
		status = "FAIL"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[tool] %-19s %s %s  %s", name, status, elapsed.Round(time.Millisecond), describeArgs(in))

	switch v := in.(type) {
	case editFileIn:
		fmt.Fprintf(&b, "  -%d/+%d lines", countLines(v.OldString), countLines(v.NewString))
		if v.ReplaceAll {
			b.WriteString(" (replace_all)")
		}
	case writeFileIn:
		fmt.Fprintf(&b, "  %d bytes, %d lines", len(v.Content), countLines(v.Content))
	}

	if err != nil {
		fmt.Fprintf(&b, "\n       => error: %s", oneLine(err.Error(), 500))
	} else if !readOnlyTools[name] {
		for i, line := range strings.SplitN(strings.TrimRight(out, "\n"), "\n", 4) {
			if i == 3 {
				break
			}
			fmt.Fprintf(&b, "\n       => %s", oneLine(line, 300))
		}
	}
	if v, ok := in.(editFileIn); ok && err == nil {
		fmt.Fprintf(&b, "\n       - old: %s\n       + new: %s", oneLine(v.OldString, 200), oneLine(v.NewString, 200))
	}
	log.Print(b.String())
}

// describeArgs renders the short arguments of a call as key=value pairs;
// file contents and edit strings are left out (they get their own lines).
func describeArgs(in any) string {
	m := summarize(in)
	keys := []string{"path", "src", "dst", "pattern", "glob", "id", "name", "cwd", "shell", "timeout_sec", "command"}
	var parts []string
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		str := fmt.Sprint(v)
		if str == "" || str == "0" || str == "false" {
			continue
		}
		if strings.ContainsAny(str, " \t\n\"") {
			str = fmt.Sprintf("%q", oneLine(str, 300))
		}
		parts = append(parts, k+"="+str)
	}
	return strings.Join(parts, " ")
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// oneLine collapses whitespace and clips to max runes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}
