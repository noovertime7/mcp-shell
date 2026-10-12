package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type echoIn struct {
	Text  string `json:"text" jsonschema:"text to echo"`
	Times int    `json:"times,omitempty" jsonschema:"repeat count"`
}

func newTestServer() *httptest.Server {
	s := NewServer("test", "0.0.1", "be nice")
	s.AddTool(Typed("echo", "echo text", func(ctx context.Context, in echoIn) (string, error) {
		if in.Text == "fail" {
			return "", errors.New("asked to fail")
		}
		if in.Times == 0 {
			in.Times = 1
		}
		return strings.Repeat(in.Text, in.Times), nil
	}))
	return httptest.NewServer(s)
}

func post(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, m
}

func TestInitializeListCall(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	code, m := post(t, ts.URL, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	res, _ := m["result"].(map[string]any)
	if code != 200 || res["protocolVersion"] != "2025-06-18" || res["instructions"] != "be nice" {
		t.Fatalf("initialize: %d %v", code, m)
	}

	if code, _ := post(t, ts.URL, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Fatalf("notification status %d", code)
	}

	_, m = post(t, ts.URL, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := m["result"].(map[string]any)["tools"].([]any)
	tool := tools[0].(map[string]any)
	schema := tool["inputSchema"].(map[string]any)
	if tool["name"] != "echo" || schema["type"] != "object" || len(schema["required"].([]any)) != 1 {
		t.Fatalf("tools/list: %v", m)
	}

	_, m = post(t, ts.URL, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"ab","times":2}}}`)
	res = m["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"]
	if text != "abab" || res["isError"] != false {
		t.Fatalf("tools/call: %v", m)
	}
}

func TestToolErrors(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	for body, want := range map[string]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"fail"}}}`: "asked to fail",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{}}}`:              `missing required argument "text"`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":5}}}`:      "invalid arguments",
	} {
		_, m := post(t, ts.URL, body)
		res := m["result"].(map[string]any)
		text, _ := res["content"].([]any)[0].(map[string]any)["text"].(string)
		if res["isError"] != true || !strings.Contains(text, want) {
			t.Errorf("%s: got %v", body, m)
		}
	}

	_, m := post(t, ts.URL, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`)
	if m["error"] == nil {
		t.Errorf("unknown tool should be a JSON-RPC error: %v", m)
	}
	_, m = post(t, ts.URL, `{"jsonrpc":"2.0","id":5,"method":"no/such"}`)
	if e, _ := m["error"].(map[string]any); e == nil || e["code"].(float64) != codeMethodNotFound {
		t.Errorf("unknown method: %v", m)
	}
}

func TestGetIsNotAllowed(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d", resp.StatusCode)
	}
}

type imageTestIn struct {
	Name string `json:"name"`
}

func TestTypedCallWithImage(t *testing.T) {
	s := NewServer("test", "0.0.1", "")
	s.AddTool(TypedCall("view_test_image", "view test image", func(ctx context.Context, in imageTestIn) (*CallResult, error) {
		return &CallResult{
			Content: []Content{
				{Type: "text", Text: "image info: " + in.Name},
				{Type: "image", Data: "aGVsbG8=", MIMEType: "image/jpeg"},
			},
		}, nil
	}))
	ts := httptest.NewServer(s)
	defer ts.Close()

	code, m := post(t, ts.URL, `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"view_test_image","arguments":{"name":"sample.jpg"}}}`)
	if code != 200 {
		t.Fatalf("status %d: %v", code, m)
	}
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing: %v", m)
	}
	contents, ok := res["content"].([]any)
	if !ok || len(contents) != 2 {
		t.Fatalf("expected 2 content items: %v", res)
	}
	textItem := contents[0].(map[string]any)
	imgItem := contents[1].(map[string]any)
	if textItem["type"] != "text" || textItem["text"] != "image info: sample.jpg" {
		t.Errorf("unexpected text item: %v", textItem)
	}
	if imgItem["type"] != "image" || imgItem["data"] != "aGVsbG8=" || imgItem["mimeType"] != "image/jpeg" {
		t.Errorf("unexpected image item: %v", imgItem)
	}
}
