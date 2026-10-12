// Package mcp is a minimal Model Context Protocol server over Streamable
// HTTP, built on the standard library only so it compiles with Go 1.20.
//
// It implements what a tool server needs: initialize, ping, tools/list,
// tools/call and notifications/cancelled. Every POST gets a plain
// application/json response; there are no sessions and no SSE streams,
// which the spec allows ("stateless" server). GET and DELETE return 405.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"sync"
)

// LatestProtocolVersion is answered when the client asks for an unknown one.
const LatestProtocolVersion = "2025-06-18"

var knownProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
	"2025-11-25": true,
}

// maxBodyBytes bounds one request body (write_file content travels in it).
const maxBodyBytes = 64 << 20

// JSON-RPC error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Content represents an item in an MCP tool call result.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

// CallResult represents an MCP tool call result with rich content.
type CallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Tool is one callable tool. Handler receives the raw "arguments" object.
// A returned error is reported to the model as a tool result with isError.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(ctx context.Context, args json.RawMessage) (string, error)
	CallHandler func(ctx context.Context, args json.RawMessage) (*CallResult, error)
}

// Server dispatches JSON-RPC messages to tools. It implements http.Handler.
type Server struct {
	Name         string
	Version      string
	Instructions string

	tools  []*Tool
	byName map[string]*Tool

	mu       sync.Mutex
	inflight map[string]context.CancelFunc // by raw request id
}

// NewServer returns a server without tools.
func NewServer(name, version, instructions string) *Server {
	return &Server{
		Name: name, Version: version, Instructions: instructions,
		byName:   map[string]*Tool{},
		inflight: map[string]context.CancelFunc{},
	}
}

// AddTool registers a tool. Registering the same name twice panics.
func (s *Server) AddTool(t *Tool) {
	if _, dup := s.byName[t.Name]; dup {
		panic("mcp: duplicate tool " + t.Name)
	}
	if t.InputSchema == nil {
		t.InputSchema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	s.tools = append(s.tools, t)
	s.byName[t.Name] = t
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var nullID = json.RawMessage("null")

func errorResponse(id json.RawMessage, code int, msg string) *response {
	if len(id) == 0 {
		id = nullID
	}
	return &response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// ServeHTTP handles the Streamable HTTP transport.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed (this server has no SSE stream or sessions)", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > maxBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	body = bytes.TrimSpace(body)

	// A batch (JSON array) is still accepted for older clients.
	if len(body) > 0 && body[0] == '[' {
		var msgs []json.RawMessage
		if err := json.Unmarshal(body, &msgs); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse(nil, codeParseError, "parse error: "+err.Error()))
			return
		}
		var out []*response
		for _, m := range msgs {
			if resp := s.handle(r, m); resp != nil {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	resp := s.handle(r, body)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted) // notification or client response
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// handle processes one message and returns nil when no response is due.
func (s *Server) handle(r *http.Request, raw json.RawMessage) *response {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return errorResponse(nil, codeParseError, "parse error: "+err.Error())
	}
	isNotification := len(req.ID) == 0 || bytes.Equal(req.ID, nullID)
	if req.Method == "" {
		if isNotification {
			return errorResponse(nil, codeInvalidRequest, "missing method")
		}
		return nil // a response from the client; we never send requests
	}
	if isNotification {
		s.notify(req)
		return nil
	}

	result, rerr := s.call(r, req)
	if rerr != nil {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: rerr}
	}
	return &response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

func (s *Server) notify(req request) {
	switch req.Method {
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
			Reason    string          `json:"reason"`
		}
		if json.Unmarshal(req.Params, &p) == nil {
			s.mu.Lock()
			cancel := s.inflight[string(p.RequestID)]
			s.mu.Unlock()
			if cancel != nil {
				log.Printf("[MCP连接] 客户端取消请求 %s 原因=%s", p.RequestID, p.Reason)
				cancel()
			}
		}
	}
	// notifications/initialized and anything else need no action.
}

func (s *Server) call(r *http.Request, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := LatestProtocolVersion
		if knownProtocolVersions[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		log.Printf("[MCP连接] 客户端已连接 | 来源=%s | 客户端=%s %s | 协议=%s", r.RemoteAddr, p.ClientInfo.Name, p.ClientInfo.Version, version)
		res := map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}
		if s.Instructions != "" {
			res["instructions"] = s.Instructions
		}
		return res, nil

	case "ping":
		return map[string]any{}, nil

	case "tools/list":
		list := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			list = append(list, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			})
		}
		return map[string]any{"tools": list}, nil

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: " + err.Error()}
		}
		t := s.byName[p.Name]
		if t == nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name}
		}
		if t.CallHandler != nil {
			res, err := s.runCallTool(r.Context(), req.ID, t, p.Arguments)
			if err != nil {
				return toolResult("error: "+err.Error(), true), nil
			}
			return res, nil
		}
		text, err := s.runTool(r.Context(), req.ID, t, p.Arguments)
		if err != nil {
			return toolResult("error: "+err.Error(), true), nil
		}
		return toolResult(text, false), nil

	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
}

// runCallTool runs a CallHandler, cancellable by request or notification, and recovers panics.
func (s *Server) runCallTool(parent context.Context, id json.RawMessage, t *Tool, args json.RawMessage) (res *CallResult, err error) {
	ctx, cancel := context.WithCancel(parent)
	key := string(id)
	s.mu.Lock()
	s.inflight[key] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
		cancel()
	}()
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[mcp] panic in tool %s: %v\n%s", t.Name, p, debug.Stack())
			err = fmt.Errorf("internal error in %s: %v", t.Name, p)
		}
	}()
	if len(args) == 0 || bytes.Equal(args, nullID) {
		args = json.RawMessage("{}")
	}
	return t.CallHandler(ctx, args)
}

// runTool runs a handler, cancellable by the HTTP request ending or by
// notifications/cancelled, and turns a panic into a tool error.
func (s *Server) runTool(parent context.Context, id json.RawMessage, t *Tool, args json.RawMessage) (text string, err error) {
	ctx, cancel := context.WithCancel(parent)
	key := string(id)
	s.mu.Lock()
	s.inflight[key] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
		cancel()
	}()
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[mcp] panic in tool %s: %v\n%s", t.Name, p, debug.Stack())
			err = fmt.Errorf("internal error in %s: %v", t.Name, p)
		}
	}()
	if len(args) == 0 || bytes.Equal(args, nullID) {
		args = json.RawMessage("{}")
	}
	return t.Handler(ctx, args)
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}
