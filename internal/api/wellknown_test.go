package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWellKnownMatrixClientServesHomeserver(t *testing.T) {
	srv, _, cfg := setupSQLiteServer(t)
	cfg.Matrix.ServerName = "example.com"
	cfg.Matrix.Host = "https://matrix.example.com"
	cfg.Matrix.ChatHost = "https://chat.example.com"
	req := httptest.NewRequest("GET", "/.well-known/matrix/client", nil)
	req.Header.Set("Origin", "https://chat.example.com")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := strings.TrimSuffix(w.Body.String(), "\n"); got != `{"m.homeserver":{"base_url":"https://matrix.example.com"}}` {
		t.Fatalf("body %q", got)
	}
	for k, want := range map[string]string{"Access-Control-Allow-Origin": "*", "Content-Type": "application/json", "Access-Control-Allow-Credentials": ""} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestWellKnownMatrixClientIs404WhenUnconfigured(t *testing.T) {
	srv, _, _ := setupSQLiteServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/matrix/client", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Fatalf("want a JSON error, got %q", w.Body.String())
	}
}

func TestSettingsExposeChatURL(t *testing.T) {
	srv, _, cfg := setupSQLiteServer(t)
	get := func() any {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest("GET", "/api/settings", nil))
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out["chat_url"]
	}
	if got := get(); got != "" {
		t.Fatalf("unconfigured chat_url = %#v, want empty string", got)
	}
	cfg.Matrix.ServerName = "example.com"
	cfg.Matrix.Host = "https://matrix.example.com"
	cfg.Matrix.ChatHost = "https://chat.example.com"
	if got := get(); got != "https://chat.example.com" {
		t.Fatalf("chat_url = %#v", got)
	}
}

// Browsers preflight cross-origin discovery when a client adds headers; any origin may ask.
func TestWellKnownMatrixClientPreflightFromAnyOrigin(t *testing.T) {
	srv, _, cfg := setupSQLiteServer(t)
	cfg.Matrix.ServerName = "example.com"
	cfg.Matrix.Host = "https://matrix.example.com"
	cfg.Matrix.ChatHost = "https://chat.example.com"
	req := httptest.NewRequest("OPTIONS", "/.well-known/matrix/client", nil)
	req.Header.Set("Origin", "https://some-other-client.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "X-Requested-With")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	h := w.Header()
	if h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Access-Control-Allow-Credentials") != "" ||
		!strings.Contains(h.Get("Access-Control-Allow-Methods"), "GET") ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "X-Requested-With") {
		t.Fatalf("preflight headers: %v", h)
	}
}
