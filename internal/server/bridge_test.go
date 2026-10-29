package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qcenaman/apichat/internal/chat"
	"github.com/qcenaman/apichat/internal/config"
	"github.com/qcenaman/apichat/internal/guard"
)

func TestBridgeAuth(t *testing.T) {
	t.Setenv("APICHAT_ROOT", t.TempDir())
	cfg := config.Load()
	s := &Server{Engine: &chat.Engine{Guard: &guard.Guard{}}, Version: "test"}
	h := s.bridgeHandler()

	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("OPTIONS", "/bridge/info", nil))
	if r.Code != 204 || r.Header().Get("access-control-allow-origin") != "*" {
		t.Fatalf("preflight: %d %v", r.Code, r.Header())
	}

	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/bridge/info", nil))
	if r.Code != 401 {
		t.Fatalf("no key: want 401, got %d", r.Code)
	}

	req := httptest.NewRequest("GET", "/bridge/info", nil)
	req.Header.Set("x-bridge-key", cfg.BridgeKey)
	r = httptest.NewRecorder()
	h.ServeHTTP(r, req)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"tools"`) {
		t.Fatalf("info: %d %s", r.Code, r.Body.String())
	}

	req = httptest.NewRequest("POST", "/bridge/call", strings.NewReader(`{"name":""}`))
	req.Header.Set("x-bridge-key", cfg.BridgeKey)
	req.Header.Set("content-type", "application/json")
	r = httptest.NewRecorder()
	h.ServeHTTP(r, req)
	if r.Code != http.StatusBadRequest {
		t.Fatalf("empty name: want 400, got %d", r.Code)
	}
}
