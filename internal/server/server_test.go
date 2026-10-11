package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/noovertime7/mcp-shell/internal/config"
)

const testToken = "test-token-0123456789abcdef"

func newTestServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	// The desktop GUI passes its token in MCP_SHELL_TOKEN, which overrides
	// the config file; the tests use their own.
	t.Setenv("MCP_SHELL_TOKEN", "")
	root := t.TempDir()
	out := t.TempDir()
	cfg := map[string]any{
		"listen": "127.0.0.1:0", "token": testToken, "roots": []string{root},
		"output_dir": out, "command_timeout_sec": 30,
	}
	data, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv.URL + "/mcp", root
}

// call runs one tool and returns its text and isError.
func call(t *testing.T, url, tool string, args map[string]any) (string, bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-code/9.9.9 (test)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if len(r.Result.Content) == 0 {
		t.Fatalf("%s: empty result", tool)
	}
	return r.Result.Content[0].Text, r.Result.IsError
}

func TestNewToolsAreListed(t *testing.T) {
	s, _, _ := newTestServer(t)
	names := map[string]bool{}
	for _, tool := range listTools(s) {
		names[tool] = true
	}
	for _, want := range []string{"multi_edit", "system_info", "get_env", "operation_history"} {
		if !names[want] {
			t.Fatalf("tool %s is not registered: %v", want, names)
		}
	}
}

func listTools(s *Server) []string {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	s.Handler().ServeHTTP(rec, req)
	var r struct {
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	json.Unmarshal(rec.Body.Bytes(), &r)
	var out []string
	for _, tool := range r.Result.Tools {
		out = append(out, tool.Name)
	}
	return out
}

func TestOperationHistoryRecordsCalls(t *testing.T) {
	_, url, root := newTestServer(t)
	file := filepath.Join(root, "notes.txt")
	if _, isErr := call(t, url, "write_file", map[string]any{"path": file, "content": "one\ntwo\n"}); isErr {
		t.Fatal("write_file failed")
	}
	if text, isErr := call(t, url, "multi_edit", map[string]any{"path": file, "edits": []map[string]any{
		{"old_string": "one", "new_string": "ONE"}, {"old_string": "two", "new_string": "TWO"},
	}}); isErr || !strings.Contains(text, "applied 2 edit(s)") {
		t.Fatalf("multi_edit: %s", text)
	}
	failing := "exit 3"
	if runtime.GOOS == "windows" {
		failing = "cmd /c exit 3"
	}
	call(t, url, "run_command", map[string]any{"command": failing, "env": map[string]any{"SECRET_TOKEN": "s3cr3t-value"}})
	call(t, url, "read_file", map[string]any{"path": file})

	text, _ := call(t, url, "operation_history", map[string]any{})
	for _, want := range []string{"multi_edit", "write_file", "read_file", "FAILED exit=3", failing, "(2 edits)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("history list misses %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "operation_history") {
		t.Fatalf("the history should not list its own calls:\n%s", text)
	}

	text, _ = call(t, url, "operation_history", map[string]any{"failed_only": true})
	if !strings.Contains(text, "1 matching") || !strings.Contains(text, "run_command") {
		t.Fatalf("failed_only:\n%s", text)
	}
	text, _ = call(t, url, "operation_history", map[string]any{"changes_only": true})
	if strings.Contains(text, "read_file") {
		t.Fatalf("changes_only should drop read_file:\n%s", text)
	}

	text, _ = call(t, url, "operation_history", map[string]any{"tool": "run_command"})
	id := strings.Fields(strings.Split(text, "\n")[1])[0][1:]
	detail, isErr := call(t, url, "operation_history", map[string]any{"id": json.Number(id)})
	if isErr || !strings.Contains(detail, "exit_code 3") || !strings.Contains(detail, "claude-code/9.9.9") {
		t.Fatalf("detail of #%s:\n%s", id, detail)
	}
	if strings.Contains(detail, "s3cr3t-value") || !strings.Contains(detail, "<hidden>") {
		t.Fatalf("env values must not be kept in the history:\n%s", detail)
	}
}

func TestGetEnvListsPathAndHidesSecrets(t *testing.T) {
	_, url, _ := newTestServer(t)
	t.Setenv("MCP_SHELL_TEST_API_TOKEN", "very-secret")
	t.Setenv("MCP_SHELL_TEST_PLAIN", "plain-value")

	text, _ := call(t, url, "get_env", map[string]any{"filter": "MCP_SHELL_TEST"})
	if !strings.Contains(text, "MCP_SHELL_TEST_PLAIN=plain-value") || strings.Contains(text, "very-secret") || !strings.Contains(text, "<hidden") {
		t.Fatalf("filtered list:\n%s", text)
	}
	text, _ = call(t, url, "get_env", map[string]any{"name": "MCP_SHELL_TEST_API_TOKEN", "show_secrets": true})
	if !strings.Contains(text, "very-secret") {
		t.Fatalf("show_secrets should reveal it:\n%s", text)
	}
	t.Setenv("MCP_SHELL_TOKEN", testToken)
	text, _ = call(t, url, "get_env", map[string]any{"name": "MCP_SHELL_TOKEN", "show_secrets": true})
	if strings.Contains(text, testToken) || !strings.Contains(text, "is not set") {
		t.Fatalf("commands do not get mcp-shell's own token, so get_env must not show it:\n%s", text)
	}
	text, _ = call(t, url, "get_env", map[string]any{"name": "PATH"})
	if !strings.Contains(text, "entries, in search order") {
		t.Fatalf("PATH should be listed one entry per line:\n%s", text)
	}
}

func TestSystemInfoAndListRoots(t *testing.T) {
	_, url, _ := newTestServer(t)
	text, isErr := call(t, url, "system_info", map[string]any{})
	if isErr || !strings.Contains(text, "os:") || !strings.Contains(text, "tools on PATH:") || !strings.Contains(text, "PATH:") {
		t.Fatalf("system_info:\n%s", text)
	}
	text, _ = call(t, url, "list_roots", map[string]any{})
	for _, want := range []string{"scratch_dir:", "history:", "tools: found", "max_call_wait_sec: 45"} {
		if !strings.Contains(text, want) {
			t.Fatalf("list_roots misses %q:\n%s", want, text)
		}
	}
}
