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
	// The app's own origin would otherwise pick up ServeHTTP's credentialed CORS headers.
	for _, origin := range []string{"https://some-other-client.example", cfg.Server.AppURL} {
		req := httptest.NewRequest("OPTIONS", "/.well-known/matrix/client", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "GET")
		req.Header.Set("Access-Control-Request-Headers", "X-Requested-With")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", origin, w.Code, w.Body.String())
		}
		h := w.Header()
		if h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Access-Control-Allow-Credentials") != "" ||
			!strings.Contains(h.Get("Access-Control-Allow-Methods"), "GET") ||
			!strings.Contains(h.Get("Access-Control-Allow-Headers"), "X-Requested-With") {
			t.Fatalf("%s: preflight headers: %v", origin, h)
		}
	}
}

// The server name is usually the apex domain: a year of includeSubDomains HSTS there would
// lock every plain-http homelab service under it out of browsers.
func TestHSTSOnlyOnTheAppHost(t *testing.T) {
	srv, _, cfg := setupSQLiteServer(t)
	cfg.Server.AppURL = "https://chat.example.com"
	cfg.Security.CookieSecure = true
	cfg.Matrix.ServerName = "example.com"
	cfg.Matrix.Host = "https://matrix.example.com"
	for _, c := range []struct {
		host, path string
		want       bool
	}{
		{"example.com", "/.well-known/matrix/client", false},
		{"chat.example.com", "/.well-known/matrix/client", false},
		{"example.com", "/api/settings", false},
		{"chat.example.com", "/api/settings", true},
		{"CHAT.example.com:443", "/api/settings", true},
	} {
		req := httptest.NewRequest("GET", c.path, nil)
		req.Host = c.host
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if got := w.Header().Get("Strict-Transport-Security") != ""; got != c.want {
			t.Errorf("%s%s: HSTS sent = %v, want %v", c.host, c.path, got, c.want)
		}
	}
}
