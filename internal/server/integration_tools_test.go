package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"github.com/noovertime7/mcp-shell/internal/config"
)

func TestInspectPortFree(t *testing.T) {
	// Pick an improbable port or dynamic check
	res, err := inspectPort(59999, false)
	if err != nil {
		t.Fatalf("inspectPort error: %v", err)
	}
	if !strings.Contains(res, "59999") {
		t.Errorf("expected port mention in output, got %q", res)
	}
}

func TestHttpRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "val" {
			http.Error(w, "missing header", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello mcp shell"))
	}))
	defer server.Close()

	s := &Server{
		cfg: &config.Config{
			MaxReadBytes: 1000,
		},
	}

	res, err := s.httpRequest(context.Background(), httpRequestIn{
		URL:     server.URL,
		Method:  "GET",
		Headers: map[string]string{"X-Test": "val"},
	})
	if err != nil {
		t.Fatalf("httpRequest failed: %v", err)
	}
	if !strings.Contains(res, "200 OK") || !strings.Contains(res, "hello mcp shell") {
		t.Errorf("unexpected httpRequest response: %s", res)
	}
}
