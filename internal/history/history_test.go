package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func intp(n int) *int { return &n }

func TestAddFindGetAndContinueIDs(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 30)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	add := func(e Entry) {
		t.Helper()
		if _, err := s.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	add(Entry{Time: now.Add(-3 * time.Hour), Tool: "read_file", Args: map[string]any{"path": `E:\a.txt`}, Status: "ok"})
	add(Entry{Time: now.Add(-2 * time.Hour), Tool: "run_command", Args: map[string]any{"command": "git push origin master"}, Status: "failed", ExitCode: intp(128), Result: "fatal: unable to access"})
	add(Entry{Time: now.Add(-1 * time.Hour), Tool: "edit_file", Args: map[string]any{"path": `E:\b.go`}, Status: "ok"})
	add(Entry{Time: now, Tool: "operation_history", Status: "ok"})

	got, total, err := s.Find(Query{Exclude: map[string]bool{"operation_history": true}})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || got[0].Tool != "edit_file" || got[2].Tool != "read_file" {
		t.Fatalf("newest first without history calls, got total=%d %+v", total, got)
	}
	if got, _, _ := s.Find(Query{FailedOnly: true}); len(got) != 1 || *got[0].ExitCode != 128 {
		t.Fatalf("failed_only: %+v", got)
	}
	if got, _, _ := s.Find(Query{Text: "GIT PUSH"}); len(got) != 1 || got[0].Tool != "run_command" {
		t.Fatalf("text search is case-insensitive over arguments: %+v", got)
	}
	readOnly := map[string]bool{"read_file": true, "operation_history": true}
	if got, _, _ := s.Find(Query{ChangesOnly: true, ReadOnly: readOnly}); len(got) != 2 {
		t.Fatalf("changes_only: %+v", got)
	}
	if got, _, _ := s.Find(Query{Since: now.Add(-90 * time.Minute)}); len(got) != 2 {
		t.Fatalf("since: %+v", got)
	}
	if got, total, _ := s.Find(Query{Limit: 1, Offset: 1, Exclude: map[string]bool{"operation_history": true}}); total != 3 || len(got) != 1 || got[0].Tool != "run_command" {
		t.Fatalf("paging: total=%d %+v", total, got)
	}
	e, err := s.Get(2)
	if err != nil || e.Tool != "run_command" {
		t.Fatalf("get #2: %+v %v", e, err)
	}

	again, err := Open(dir, 30)
	if err != nil {
		t.Fatal(err)
	}
	id, err := again.Add(Entry{Tool: "list_dir", Status: "ok"})
	if err != nil || id != 5 {
		t.Fatalf("ids must continue after a restart, got %d %v", id, err)
	}
}

func TestOldDaysAreDropped(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().AddDate(0, 0, -40).Format(dayLayout) + ".jsonl"
	if err := os.WriteFile(filepath.Join(dir, old), []byte(`{"id":1,"tool":"x","status":"ok"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Fatalf("a day older than history_days must be removed, stat err=%v", err)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 11, 15, 0, 0, 0, time.Local)
	cases := map[string]time.Time{
		"30m":              now.Add(-30 * time.Minute),
		"2h":               now.Add(-2 * time.Hour),
		"3d":               now.Add(-72 * time.Hour),
		"2026-10-10":       time.Date(2026, 10, 10, 0, 0, 0, 0, time.Local),
		"2026-10-11 09:30": time.Date(2026, 10, 11, 9, 30, 0, 0, time.Local),
	}
	for in, want := range cases {
		got, err := ParseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Fatalf("ParseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseSince("yesterday", now); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}
