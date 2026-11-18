package cursorctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/websocket"
)

// fakeInspector 模拟 inspector：记下收到的表达式，按 reply 回复。
func fakeInspector(t *testing.T, reply func(expr string) string) (port int, exprs *[]string) {
	t.Helper()
	got := []string{}
	mux := http.NewServeMux()
	var addr string
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[{"webSocketDebuggerUrl":"ws://%s/ws"}]`, addr)
	})
	mux.Handle("/ws", websocket.Server{Handler: func(c *websocket.Conn) {
		var raw string
		if websocket.Message.Receive(c, &raw) != nil {
			return
		}
		var m struct {
			ID     int
			Params struct{ Expression string }
		}
		_ = json.Unmarshal([]byte(raw), &m)
		got = append(got, m.Params.Expression)
		// 无关事件要被跳过
		_ = websocket.Message.Send(c, `{"method":"Runtime.executionContextCreated"}`)
		_ = websocket.Message.Send(c, reply(m.Params.Expression))
	}})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	addr = srv.Listener.Addr().String()
	_, p, _ := net.SplitHostPort(addr)
	port, _ = strconv.Atoi(p)
	return port, &got
}

func value(v string) string {
	b, _ := json.Marshal(v)
	return `{"id":1,"result":{"result":{"value":` + string(b) + `}}}`
}

func noOpen(context.Context, int) error { return errors.New("must not open") }

func TestAgentsAndArguments(t *testing.T) {
	port, exprs := fakeInspector(t, func(string) string {
		return value(`[{"i":0,"selected":true,"busy":true,"title":"先读","sub":"Editing a.go"}]`)
	})
	c := &Client{Port: port, open: noOpen}
	got, err := c.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Busy || !got[0].Selected || got[0].Title != "先读" {
		t.Fatalf("agents = %+v", got)
	}
	if _, err := c.NewAgent(context.Background(), "你好\n\"引号\""); err != nil {
		t.Fatal(err)
	}
	last := (*exprs)[1]
	if !strings.Contains(last, `"cmd":"new"`) || !strings.Contains(last, `你好\n\"引号\"`) {
		t.Fatalf("arguments not encoded: %s", last[len(last)-120:])
	}
}

func TestPageErrorIsReturned(t *testing.T) {
	port, _ := fakeInspector(t, func(string) string {
		return `{"id":1,"result":{"result":{"type":"object","value":{}},"exceptionDetails":{"exception":{"description":"Error: agent 2 is still running\n    at x"}}}}`
	})
	c := &Client{Port: port, open: noOpen}
	_, err := c.Send(context.Background(), "2", "go", "")
	if err == nil || err.Error() != "agent 2 is still running" {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyTextIsRejected(t *testing.T) {
	c := &Client{Port: 1, open: noOpen}
	if _, err := c.Send(context.Background(), "1", "  ", ""); err == nil {
		t.Fatal("empty send accepted")
	}
	if _, err := c.NewAgent(context.Background(), ""); err == nil {
		t.Fatal("empty new accepted")
	}
}

func TestSendMode(t *testing.T) {
	port, exprs := fakeInspector(t, func(string) string {
		return value(`{"i":0,"busy":true,"title":"a"}`)
	})
	c := &Client{Port: port, open: noOpen}
	if _, err := c.Send(context.Background(), "0", "go", ModeNow); err != nil {
		t.Fatal(err)
	}
	if last := (*exprs)[0]; !strings.Contains(last, `"mode":"now"`) {
		t.Fatalf("mode not sent: %s", last[len(last)-80:])
	}
	if _, err := c.Send(context.Background(), "0", "go", "steer"); err == nil {
		t.Fatal("bad mode accepted")
	}
	if len(*exprs) != 1 {
		t.Fatalf("bad mode reached Cursor: %d calls", len(*exprs))
	}
}

func TestOpensInspectorWhenClosed(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	opened := false
	c := &Client{Port: port, open: func(context.Context, int) error { opened = true; return errors.New("no cursor") }}
	if _, err := c.Agents(context.Background()); err == nil || !opened || !strings.Contains(err.Error(), "no cursor") {
		t.Fatalf("opened=%v err=%v", opened, err)
	}
}

func TestParseMainProcess(t *testing.T) {
	pid, exe, err := parseMainProcess("27180|D:\\cursor\\Cursor.exe\r\n")
	if err != nil || pid != 27180 || exe != `D:\cursor\Cursor.exe` {
		t.Fatalf("%d %q %v", pid, exe, err)
	}
	if _, _, err := parseMainProcess("  "); err == nil {
		t.Fatal("empty accepted")
	}
	if _, _, err := parseMainProcess("27180|"); err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("no path: %v", err)
	}
}
