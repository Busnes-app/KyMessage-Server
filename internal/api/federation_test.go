package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/matrixinit"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

type fedView struct {
	Level      string
	ServerName string `json:"server_name"`
	Servers    []struct{ Name string }
	Presets    []struct {
		Name   string
		Ticked bool
	}
	RestartHint string `json:"restart_hint"`
	FileError   string `json:"file_error"`
}

// setupFederation is a Matrix server with matrix-init's closed federation file.
func setupFederation(t *testing.T) (*api.Server, store.Store, *config.Config, *fakeMAS, string) {
	t.Helper()
	srv, st, cfg, f := setupMatrixServer(t)
	cfg.Matrix.Host = "https://matrix.example.com"
	cfg.Matrix.Dir = t.TempDir()
	cfg.Matrix.SynapseRestartHint = config.DefaultSynapseRestartHint
	path := filepath.Join(cfg.Matrix.Dir, matrixinit.FederationFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := matrixinit.WriteFederation(path, "example.com", nil); err != nil {
		t.Fatal(err)
	}
	return srv, st, cfg, f, path
}

// allowList is what Synapse would read from the federation file.
func allowList(t *testing.T, path string) []string {
	t.Helper()
	var fed struct {
		Allow     []string `yaml:"federation_domain_whitelist"`
		Listeners []struct {
			Resources []struct{ Names []string }
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, &fed); err != nil {
		t.Fatal(err)
	}
	if open := slices.Contains(fed.Listeners[0].Resources[0].Names, "federation"); open != (len(fed.Allow) > 0) {
		t.Fatalf("federation resource %v with allow-list %v", open, fed.Allow)
	}
	return fed.Allow
}

func wellKnownServer(srv *api.Server) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/matrix/server", nil))
	return w
}

func TestFederationIsClosedByDefault(t *testing.T) {
	srv, st, _, _, path := setupFederation(t)
	admin := loginAs(t, srv, st, "root", "admin")
	v := decode[fedView](t, adminDo(t, srv, admin, "GET", "/api/admin/federation", nil))
	if v.Level != "closed" || v.ServerName != "example.com" || len(v.Servers) != 0 || v.RestartHint != "docker compose restart synapse" {
		t.Errorf("default view %+v", v)
	}
	if err := srv.ReconcileFederation(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := allowList(t, path); len(got) != 0 {
		t.Errorf("closed allow-list %v", got)
	}
	if w := wellKnownServer(srv); w.Code != http.StatusNotFound {
		t.Errorf("closed server well-known: %d %s", w.Code, w.Body.String())
	}
}

func TestFederationLevelsServersAndWellKnown(t *testing.T) {
	srv, st, _, _, path := setupFederation(t)
	admin := loginAs(t, srv, st, "root", "admin")
	ok := func(method, p string, body any) fedView {
		t.Helper()
		return decode[fedView](t, adminDo(t, srv, admin, method, p, body))
	}
	ok("PUT", "/api/admin/federation/level", map[string]string{"level": "manual"})
	if got := allowList(t, path); len(got) != 0 {
		t.Errorf("manual with no servers is still closed: %v", got)
	}
	v := ok("POST", "/api/admin/federation/servers", map[string]string{"name": "  Partner.Example "})
	if len(v.Servers) != 1 || v.Servers[0].Name != "partner.example" {
		t.Errorf("servers %+v", v.Servers)
	}
	ok("POST", "/api/admin/federation/servers", map[string]string{"name": "partner.example"}) // idempotent
	ok("POST", "/api/admin/federation/servers", map[string]string{"name": "b.example"})
	if got := allowList(t, path); !slices.Equal(got, []string{"b.example", "partner.example"}) {
		t.Errorf("allow-list %v", got)
	}
	if w := wellKnownServer(srv); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"m.server":"matrix.example.com:443"}` ||
		w.Header().Get("Strict-Transport-Security") != "" {
		t.Errorf("well-known: %d %s %v", w.Code, w.Body.String(), w.Header())
	}

	// Closing keeps the list for later and empties Synapse's.
	v = ok("PUT", "/api/admin/federation/level", map[string]string{"level": "closed"})
	if len(v.Servers) != 2 || len(allowList(t, path)) != 0 || wellKnownServer(srv).Code != http.StatusNotFound {
		t.Errorf("closed: servers %+v, file %v", v.Servers, allowList(t, path))
	}
	ok("PUT", "/api/admin/federation/level", map[string]string{"level": "manual"})
	if got := allowList(t, path); len(got) != 2 {
		t.Errorf("reopened allow-list %v", got)
	}
	ok("DELETE", "/api/admin/federation/servers/b.example", nil)
	if got := allowList(t, path); !slices.Equal(got, []string{"partner.example"}) {
		t.Errorf("after removal %v", got)
	}
	if w := adminDo(t, srv, admin, "DELETE", "/api/admin/federation/servers/b.example", nil); w.Code != http.StatusNotFound {
		t.Errorf("removing an absent server: %d", w.Code)
	}

	recs, _, err := st.Audit().ListAuditRecords(context.Background(), 0, 50, "matrix.federation_")
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, r := range recs {
		if strings.HasPrefix(r.Action, "matrix.federation_") {
			actions = append(actions, r.Action+" "+r.Resource+" "+r.Details)
		}
	}
	for _, want := range []string{`matrix.federation_level manual outcome="changed from closed"`, `matrix.federation_server Partner.Example outcome="added"`,
		`matrix.federation_server b.example outcome="removed"`, `matrix.federation_level closed outcome="changed from manual"`} {
		if !slices.Contains(actions, want) {
			t.Errorf("audit lacks %q in %q", want, actions)
		}
	}
}

func TestFederationWideningNeedsStepUpNarrowingDoesNot(t *testing.T) {
	srv, st, _, _, path := setupFederation(t)
	admin := loginAs(t, srv, st, "root", "admin")
	decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/level", map[string]string{"level": "manual"}))
	decode[fedView](t, adminDo(t, srv, admin, "POST", "/api/admin/federation/servers", map[string]string{"name": "partner.example"}))
	api.SetFederationPresetsForTest(t, "public.example")

	stale := staleAdmin(t, st)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PUT", "/api/admin/federation/level", map[string]string{"level": "curated"}},
		{"POST", "/api/admin/federation/servers", map[string]string{"name": "other.example"}},
		{"PUT", "/api/admin/federation/presets/public.example", map[string]bool{"ticked": true}},
	} {
		if w := adminDo(t, srv, stale, c.method, c.path, c.body); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "reauthentication_required") {
			t.Errorf("stale %s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	v := decode[fedView](t, adminDo(t, srv, admin, "GET", "/api/admin/federation", nil))
	if v.Level != "manual" || len(v.Servers) != 1 || v.Presets[0].Ticked {
		t.Fatalf("a stale session widened federation: %+v", v)
	}
	// Narrowing: one click, whatever the session's age.
	decode[fedView](t, adminDo(t, srv, stale, "DELETE", "/api/admin/federation/servers/partner.example", nil))
	decode[fedView](t, adminDo(t, srv, stale, "PUT", "/api/admin/federation/level", map[string]string{"level": "closed"}))
	if got := allowList(t, path); len(got) != 0 {
		t.Errorf("after narrowing %v", got)
	}
}

func TestFederationRefusesBadServerNames(t *testing.T) {
	srv, st, _, _, path := setupFederation(t)
	admin := loginAs(t, srv, st, "root", "admin")
	decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/level", map[string]string{"level": "manual"}))
	for _, bad := range []string{"https://partner.example", "partner.example/path", "partner.example:8448", "192.0.2.1", "example.com",
		"localhost", "*.example", "a.example\nenable_registration: true", ""} {
		if w := adminDo(t, srv, admin, "POST", "/api/admin/federation/servers", map[string]string{"name": bad}); w.Code != http.StatusBadRequest {
			t.Errorf("%q: %d %s", bad, w.Code, w.Body.String())
		}
	}
	for _, body := range []any{map[string]any{"name": "a.example", "extra": 1}, map[string]int{"name": 1}} {
		if w := adminDo(t, srv, admin, "POST", "/api/admin/federation/servers", body); w.Code != http.StatusBadRequest {
			t.Errorf("%v: %d", body, w.Code)
		}
	}
	if w := adminDo(t, srv, admin, "PUT", "/api/admin/federation/level", map[string]string{"level": "open"}); w.Code != http.StatusBadRequest {
		t.Errorf("open level: %d", w.Code)
	}
	if got := allowList(t, path); len(got) != 0 {
		t.Errorf("refusals reached Synapse: %v", got)
	}
}

func TestFederationPresetsCountOnlyAtTheCuratedLevel(t *testing.T) {
	srv, st, _, _, path := setupFederation(t)
	api.SetFederationPresetsForTest(t, "public.example")
	admin := loginAs(t, srv, st, "root", "admin")
	decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/level", map[string]string{"level": "manual"}))
	v := decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/presets/public.example", map[string]bool{"ticked": true}))
	if !v.Presets[0].Ticked || len(allowList(t, path)) != 0 {
		t.Errorf("manual level let a preset in: %+v %v", v.Presets, allowList(t, path))
	}
	decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/level", map[string]string{"level": "curated"}))
	if got := allowList(t, path); !slices.Equal(got, []string{"public.example"}) {
		t.Errorf("curated allow-list %v", got)
	}
	if w := adminDo(t, srv, admin, "PUT", "/api/admin/federation/presets/unlisted.example", map[string]bool{"ticked": true}); w.Code != http.StatusNotFound {
		t.Errorf("unlisted preset: %d", w.Code)
	}
	decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/presets/public.example", map[string]bool{"ticked": false}))
	if got := allowList(t, path); len(got) != 0 {
		t.Errorf("unticked allow-list %v", got)
	}
	// A preset a later release drops stops counting even if it was ticked.
	decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/presets/public.example", map[string]bool{"ticked": true}))
	api.SetFederationPresetsForTest(t)
	if err := srv.ReconcileFederation(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := allowList(t, path); len(got) != 0 {
		t.Errorf("dropped preset still allowed: %v", got)
	}
}

func TestFederationReconcileUndoesHandEdits(t *testing.T) {
	srv, st, _, _, path := setupFederation(t)
	admin := loginAs(t, srv, st, "root", "admin")
	decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/level", map[string]string{"level": "manual"}))
	decode[fedView](t, adminDo(t, srv, admin, "POST", "/api/admin/federation/servers", map[string]string{"name": "partner.example"}))
	want, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte("federation_domain_whitelist: null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReconcileFederation(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(want) {
		t.Errorf("hand edit kept:\n%s", got)
	}
	// A corrupt stored list never reaches Synapse: the file keeps its last good content.
	if err := st.Settings().SetSetting(context.Background(), "federation_servers", "not json"); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReconcileFederation(context.Background()); err == nil {
		t.Error("a corrupt list was accepted")
	}
	if got, _ := os.ReadFile(path); string(got) != string(want) {
		t.Errorf("file changed on a settings error:\n%s", got)
	}
	if w := adminDo(t, srv, admin, "GET", "/api/admin/federation", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("corrupt settings view: %d", w.Code)
	}
}

func TestFederationStatusMapsSynapseAnswers(t *testing.T) {
	srv, st, _, f, _ := setupFederation(t)
	answers := map[string]struct {
		code int
		body string
	}{ // Synapse 1.162 answers, measured in the plan's Task 0.
		"ok.example":     {404, `{"errcode":"M_NOT_FOUND","error":"Profile was not found"}`},
		"denied.example": {403, `{"errcode":"M_FORBIDDEN","error":"Federation denied with denied.example."}`},
		"off.example":    {404, `{"errcode":"M_UNRECOGNIZED","error":"Unrecognized request"}`},
		"down.example":   {502, `{"errcode":"M_UNKNOWN","error":"Failed to fetch profile"}`},
		"odd.example":    {500, `{}`},
	}
	syn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/_matrix/client/v3/profile/@kymessages-federation-check:"
		a, ok := answers[strings.TrimPrefix(r.URL.Path, prefix)]
		if !strings.HasPrefix(r.URL.Path, prefix) || !ok || r.Header.Get("Authorization") != "Bearer console-token" {
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		w.WriteHeader(a.code)
		w.Write([]byte(a.body))
	}))
	defer syn.Close()
	api.SetHealthTargetsForTest(srv, health.Targets{Synapse: syn.URL})
	admin := loginAs(t, srv, st, "root", "admin")
	status := func() map[string]string {
		return decode[struct{ Statuses map[string]string }](t, adminDo(t, srv, admin, "GET", "/api/admin/federation/status", nil)).Statuses
	}
	if got := status(); len(got) != 0 || f.consoleRuns != 0 {
		t.Errorf("closed server probed: %v, %d console sessions", got, f.consoleRuns)
	}
	decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/level", map[string]string{"level": "manual"}))
	for name := range answers {
		decode[fedView](t, adminDo(t, srv, admin, "POST", "/api/admin/federation/servers", map[string]string{"name": name}))
	}
	got := status()
	want := map[string]string{"ok.example": "connected", "denied.example": "not_allowed", "off.example": "federation_off",
		"down.example": "unreachable", "odd.example": "unknown"}
	for n, s := range want {
		if got[n] != s {
			t.Errorf("%s: %q, want %q", n, got[n], s)
		}
	}
	if f.consoleRuns != 1 {
		t.Errorf("%d console sessions, want one for all probes", f.consoleRuns)
	}
}

func TestFederationRoutesAre404WithoutMatrix(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	for _, p := range []string{"/api/admin/federation", "/api/admin/federation/status"} {
		if w := adminDo(t, srv, admin, "GET", p, nil); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "matrix_disabled") {
			t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	if w := wellKnownServer(srv); w.Code != http.StatusNotFound {
		t.Errorf("well-known without Matrix: %d", w.Code)
	}
}

// Every shipped preset is a valid allow-list entry with its evidence recorded.
func TestShippedFederationPresetsAreWellFormed(t *testing.T) {
	presets := api.FederationPresetsForTest()
	if len(presets) == 0 {
		t.Skip("no presets in this release")
	}
	seen := map[string]bool{}
	var names []string
	for _, p := range presets {
		if seen[p.Name] || p.Operator == "" || !strings.HasPrefix(p.Source, "https://") || len(p.Checked) != len("2006-01-02") {
			t.Errorf("preset %+v", p)
		}
		seen[p.Name] = true
		names = append(names, p.Name)
	}
	if _, err := matrixinit.FederationYAML("example.com", names); err != nil {
		t.Errorf("presets do not render: %v", err)
	}
}

// The server file is public discovery, like the client file: any origin, no credentials, no HSTS.
func TestServerWellKnownIsPublicDiscovery(t *testing.T) {
	srv, st, _, _, _ := setupFederation(t)
	admin := loginAs(t, srv, st, "root", "admin")
	decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/level", map[string]string{"level": "manual"}))
	for _, method := range []string{"GET", "OPTIONS"} {
		req := httptest.NewRequest(method, "/.well-known/matrix/server", nil)
		req.Header.Set("Origin", "https://client.example")
		req.Header.Set("Access-Control-Request-Method", "GET")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "*" ||
			w.Header().Get("Access-Control-Allow-Credentials") != "" || w.Header().Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: %d %v", method, w.Code, w.Header())
		}
	}
}

// A slow probe and console session must not lose the answer to the listener's write timeout.
func TestFederationStatusOutlivesTheWriteTimeout(t *testing.T) {
	srv, st, _, _, _ := setupFederation(t)
	syn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errcode":"M_NOT_FOUND"}`))
	}))
	defer syn.Close()
	api.SetHealthTargetsForTest(srv, health.Targets{Synapse: syn.URL})
	admin := loginAs(t, srv, st, "root", "admin")
	decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/level", map[string]string{"level": "manual"}))
	decode[fedView](t, adminDo(t, srv, admin, "POST", "/api/admin/federation/servers", map[string]string{"name": "partner.example"}))
	hs := httptest.NewUnstartedServer(srv)
	hs.Config.WriteTimeout = 100 * time.Millisecond
	hs.Start()
	defer hs.Close()
	req, _ := http.NewRequest("GET", hs.URL+"/api/admin/federation/status", nil)
	req.AddCookie(admin)
	resp, err := hs.Client().Do(req)
	if err != nil {
		t.Fatalf("status lost to the write timeout: %v", err)
	}
	defer resp.Body.Close()
	var body struct{ Statuses map[string]string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Statuses["partner.example"] != "connected" {
		t.Fatalf("status %d %v %v", resp.StatusCode, body, err)
	}
}

// Slow console setup must not eat the probes' time: they run on their own fresh context.
func TestFederationStatusProbesAfterSlowSetup(t *testing.T) {
	srv, st, _, f, _ := setupFederation(t)
	syn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errcode":"M_NOT_FOUND"}`))
	}))
	defer syn.Close()
	api.SetHealthTargetsForTest(srv, health.Targets{Synapse: syn.URL})
	admin := loginAs(t, srv, st, "root", "admin")
	decode[fedView](t, adminDo(t, srv, admin, "PUT", "/api/admin/federation/level", map[string]string{"level": "manual"}))
	decode[fedView](t, adminDo(t, srv, admin, "POST", "/api/admin/federation/servers", map[string]string{"name": "partner.example"}))
	f.consoleLate = true
	got := decode[struct{ Statuses map[string]string }](t, adminDo(t, srv, admin, "GET", "/api/admin/federation/status", nil)).Statuses
	if got["partner.example"] != "connected" {
		t.Errorf("after a slow setup: %v", got)
	}
}
