package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/health"
)

// Synapse reads the federation file only at start: a removed server stays listed as unapplied
// until the running Synapse is seen refusing it, and a failed check keeps it listed.
func TestFederationNarrowingStaysPendingUntilSynapseRefuses(t *testing.T) {
	srv, st, _, _, path := setupFederation(t)
	var mu sync.Mutex
	running := map[string]bool{} // the allow-list the fake Synapse started with
	federates, down := true, false
	syn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth := r.Header.Get("Authorization")
		origin, _, _ := strings.Cut(strings.TrimPrefix(auth, `X-Matrix origin="`), `"`)
		switch {
		case down:
			http.Error(w, "bad gateway", http.StatusBadGateway)
		case r.URL.Path != "/_matrix/federation/v1/query/profile" || !strings.Contains(auth, `destination="example.com"`):
			http.Error(w, "unexpected", http.StatusTeapot)
		case !federates:
			w.WriteHeader(404)
			w.Write([]byte(`{"errcode":"M_UNRECOGNIZED","error":"Unrecognized request"}`))
		case !running[origin]:
			w.WriteHeader(403)
			w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"Federation denied with ` + origin + `."}`))
		default: // still allowed: the forged signature is what fails
			w.WriteHeader(401)
			w.Write([]byte(`{"errcode":"M_UNAUTHORIZED","error":"Invalid signature"}`))
		}
	}))
	defer syn.Close()
	api.SetHealthTargetsForTest(srv, health.Targets{Synapse: syn.URL})
	set := func(isDown bool) {
		mu.Lock()
		defer mu.Unlock()
		down = isDown
	}
	// restart: the fake Synapse now runs the file as it is.
	restart := func() {
		mu.Lock()
		defer mu.Unlock()
		running = map[string]bool{}
		for _, n := range allowList(t, path) {
			running[n] = true
		}
		federates = len(running) > 0
	}
	admin := loginAs(t, srv, st, "root", "admin")
	unapplied := func(method, p string, body any) []string {
		t.Helper()
		return decode[struct{ Unapplied []string }](t, adminDo(t, srv, admin, method, p, body)).Unapplied
	}
	reconcile := func() {
		t.Helper()
		if err := srv.ReconcileFederation(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	unapplied("PUT", "/api/admin/federation/level", map[string]string{"level": "manual"})
	unapplied("POST", "/api/admin/federation/servers", map[string]string{"name": "a.example"})
	if got := unapplied("POST", "/api/admin/federation/servers", map[string]string{"name": "b.example"}); got == nil || len(got) != 0 {
		t.Fatalf("widening: unapplied %v, want []", got)
	}
	restart()

	if got := unapplied("DELETE", "/api/admin/federation/servers/b.example", nil); !slices.Equal(got, []string{"b.example"}) {
		t.Fatalf("after removing b: %v", got)
	}
	if got := unapplied("GET", "/api/admin/federation", nil); !slices.Equal(got, []string{"b.example"}) {
		t.Fatalf("Synapse not restarted still allows b, got %v", got)
	}
	restart()
	set(true)
	reconcile()
	if got := unapplied("GET", "/api/admin/federation", nil); !slices.Equal(got, []string{"b.example"}) {
		t.Fatalf("an unreachable Synapse cleared the warning: %v", got)
	}
	set(false)
	if got := unapplied("GET", "/api/admin/federation", nil); len(got) != 0 {
		t.Fatalf("after the restart: %v", got)
	}

	// Closing: Synapse still open allows a; restarted closed, it does not federate at all.
	if got := unapplied("PUT", "/api/admin/federation/level", map[string]string{"level": "closed"}); !slices.Equal(got, []string{"a.example"}) {
		t.Fatalf("after closing: %v", got)
	}
	reconcile()
	if got := unapplied("GET", "/api/admin/federation", nil); !slices.Equal(got, []string{"a.example"}) {
		t.Fatalf("closed file, open Synapse: %v", got)
	}
	restart()
	reconcile()
	if got := unapplied("GET", "/api/admin/federation", nil); len(got) != 0 {
		t.Fatalf("after restarting closed: %v", got)
	}

	// Allowing a pending server again takes it off the list.
	unapplied("PUT", "/api/admin/federation/level", map[string]string{"level": "manual"})
	restart()
	unapplied("DELETE", "/api/admin/federation/servers/a.example", nil)
	if got := unapplied("POST", "/api/admin/federation/servers", map[string]string{"name": "a.example"}); len(got) != 0 {
		t.Fatalf("re-added a is still unapplied: %v", got)
	}
}
