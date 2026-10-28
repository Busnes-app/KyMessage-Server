package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/matrixrtc"
)

type suiteHealthBody struct {
	Schema, Service, Status string
	Checks                  []struct{ Name, Status, Reason string }
}

func getHealthz(t *testing.T, srv *api.Server, method string) (*httptest.ResponseRecorder, suiteHealthBody) {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(method, "/healthz", nil))
	if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers %v", w.Header())
	}
	var b suiteHealthBody
	if method == http.MethodGet {
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
			t.Fatalf("decode %q: %v", w.Body.String(), err)
		}
		if b.Schema != "ky.health/1" || b.Service != "kymessages" {
			t.Fatalf("envelope %+v", b)
		}
	}
	return w, b
}

func checkStates(b suiteHealthBody) string {
	var s []string
	for _, c := range b.Checks {
		s = append(s, c.Name+"="+c.Status+c.Reason)
	}
	return strings.Join(s, ",")
}

func TestHealthzWithoutMatrixIsPublicAndChecksTheDatabase(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	w, b := getHealthz(t, srv, http.MethodGet)
	if w.Code != http.StatusOK || b.Status != "ok" || checkStates(b) != "database=ok" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w, _ := getHealthz(t, srv, http.MethodHead); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", w.Code, w.Body.String())
	}
}

// fakeUpstream answers the given paths and records every path it was asked for.
func fakeUpstream(t *testing.T, routes map[string]string) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path)
		mu.Unlock()
		if b, ok := routes[r.URL.Path]; ok {
			fmt.Fprint(w, b)
			return
		}
		http.Error(w, "upstream detail: db=pw-hunter2", http.StatusInternalServerError)
	}))
	t.Cleanup(s.Close)
	return s.URL, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

func TestHealthzWithMatrixChecksUpstreamsWithoutAdminCalls(t *testing.T) {
	srv, _, _, f := setupMatrixServer(t)
	// Synapse's version endpoint fails: it answered /health, so it is up.
	synapse, _ := fakeUpstream(t, map[string]string{"/health": "OK"})
	mas, masSeen := fakeUpstream(t, map[string]string{"/.well-known/openid-configuration": "{}"})
	live, _ := fakeUpstream(t, map[string]string{"/twirp/livekit.RoomService/ListRooms": `{"rooms":[]}`})
	api.SetHealthTargetsForTest(srv, health.Targets{Synapse: synapse, MAS: mas, Element: "http://element.invalid"})
	api.SetRTCForTest(srv, matrixrtc.New(live, "key", strings.Repeat("s", 64)))

	w, b := getHealthz(t, srv, http.MethodGet)
	if w.Code != http.StatusOK || b.Status != "ok" || checkStates(b) != "database=ok,synapse=ok,mas=ok,livekit=ok" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	f.mu.Lock()
	versions := f.versions
	f.mu.Unlock()
	if got := masSeen(); len(got) != 1 || got[0] != "/.well-known/openid-configuration" || versions != 0 {
		t.Fatalf("MAS asked for %v, admin version calls %d; the public route must not reach the admin API", got, versions)
	}
}

func TestHealthzDownShowsNoUpstreamDetail(t *testing.T) {
	srv, _, cfg, _ := setupMatrixServer(t)
	cfg.Matrix.AdminSecret = "mas-secret-xyz"
	synapse, _ := fakeUpstream(t, nil) // every path fails with secret-looking text
	mas, _ := fakeUpstream(t, map[string]string{"/.well-known/openid-configuration": "{}"})
	live, _ := fakeUpstream(t, nil)
	api.SetHealthTargetsForTest(srv, health.Targets{Synapse: synapse, MAS: mas, Element: "http://element.invalid"})
	api.SetRTCForTest(srv, matrixrtc.New(live, "key", strings.Repeat("s", 64)))

	w, b := getHealthz(t, srv, http.MethodGet)
	if w.Code != http.StatusServiceUnavailable || b.Status != "down" || checkStates(b) != "database=ok,synapse=down,mas=ok,livekit=down" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, leak := range []string{"pw-hunter2", "mas-secret-xyz", "upstream", "127.0.0.1", "HTTP 500", "error", "version"} {
		if strings.Contains(body, leak) {
			t.Errorf("response exposes %q: %s", leak, body)
		}
	}
}
