package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/google/uuid"
)

// The route must accept exactly what KyIdentity's dispatcher sends: a SCIM User body with
// application/scim+json and syncauth headers, answering 200 so its outbox marks it done.
func TestDirectoryWebhookRouteSpeaksKyIdentity(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	cfg.SSO.KyIdentityHMACSecret = "4f1c2a9e8b7d6c5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e"
	srv := api.NewServer(cfg, st)
	post := func(body []byte, sign bool) int {
		req := httptest.NewRequest("POST", "/api/sso/kyidentity/sync", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/scim+json")
		if sign {
			h, err := syncauth.Sign([]byte(cfg.SSO.KyIdentityHMACSecret), time.Now(), "user.created", uuid.NewString(), body)
			if err != nil {
				t.Fatal(err)
			}
			h.Apply(req)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	user := []byte(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"id":"kid-1","externalId":"kid-1","userName":"one","roles":[],"active":true,"meta":{"resourceType":"User","version":"W/\"1\""}}`)
	if code := post(user, true); code != http.StatusOK {
		t.Fatalf("signed KyIdentity delivery: %d", code)
	}
	if _, err := st.Users().GetUserBySSO(context.Background(), "kyidentity", "kid-1"); err != nil {
		t.Fatalf("user not provisioned: %v", err)
	}
	if code := post(user, true); code != http.StatusOK {
		t.Fatalf("redelivered duplicate: %d, want 200 so the sender stops retrying", code)
	}
	if code := post(user, false); code != http.StatusUnauthorized {
		t.Fatalf("unsigned delivery: %d", code)
	}
	if code := post([]byte(`{"id":"kid-2","active":true}`), true); code != http.StatusBadRequest {
		t.Fatalf("delivery without meta.version: %d", code)
	}
}

func TestDirectoryWebhookWakesOffboarding(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	cfg.SSO.KyIdentityHMACSecret = "4f1c2a9e8b7d6c5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e"
	srv := api.NewServer(cfg, st)
	woke := make(chan struct{}, 10)
	srv.OnDirectoryChange(func() { woke <- struct{}{} })
	body := []byte(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"id":"kid-5","externalId":"kid-5","userName":"five","roles":[],"active":false,"meta":{"resourceType":"User","version":"W/\"2\""}}`)
	post := func(sign bool) int {
		req := httptest.NewRequest("POST", "/api/sso/kyidentity/sync", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/scim+json")
		if sign {
			h, err := syncauth.Sign([]byte(cfg.SSO.KyIdentityHMACSecret), time.Now(), "user.updated", uuid.NewString(), body)
			if err != nil {
				t.Fatal(err)
			}
			h.Apply(req)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	if code := post(true); code != http.StatusOK {
		t.Fatalf("signed delivery: %d", code)
	}
	if len(woke) != 1 {
		t.Fatalf("wakes after one acknowledged event: %d, want 1", len(woke))
	}
	if code := post(false); code != http.StatusUnauthorized {
		t.Fatalf("unsigned delivery: %d", code)
	}
	if len(woke) != 1 {
		t.Fatalf("a refused delivery woke the sweep: %d wakes", len(woke))
	}
}

// A sender still configured with a retired or mistyped API path must never be acknowledged:
// the SPA fallback answered 200 with HTML, so a signed deactivation was "delivered" while the
// account and its sessions stayed live. Unknown /api/ paths now fail with a JSON 404.
func TestUnknownAPIPathIsNotAcknowledged(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	cfg.SSO.KyIdentityHMACSecret = "4f1c2a9e8b7d6c5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e"
	srv := api.NewServer(cfg, st)
	deactivate := []byte(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"id":"kid-9","externalId":"kid-9","userName":"nine","roles":[],"active":false,"meta":{"resourceType":"User","version":"W/\"2\""}}`)
	for _, path := range []string{"/api/sso/kysignon/sync", "/api/no-such-route"} {
		req := httptest.NewRequest("POST", path, bytes.NewReader(deactivate))
		req.Header.Set("Content-Type", "application/scim+json")
		h, err := syncauth.Sign([]byte(cfg.SSO.KyIdentityHMACSecret), time.Now(), "user.updated", uuid.NewString(), deactivate)
		if err != nil {
			t.Fatal(err)
		}
		h.Apply(req)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("POST %s = %d %q, want a JSON 404, never an acknowledged 200", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("SPA route /dashboard = %d, want the app shell", w.Code)
	}
}

// KyIdentity fences a 500 as an uncertain write and stops delivering to the system, so a
// username held by a local account must be a received, explained 409 instead.
func TestDirectoryWebhookUsernameConflictIs409(t *testing.T) {
	ctx := context.Background()
	_, st, cfg := setupTestServer(t)
	cfg.SSO.KyIdentityHMACSecret = "4f1c2a9e8b7d6c5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e"
	srv := api.NewServer(cfg, st)
	woke := 0
	srv.OnDirectoryChange(func() { woke++ })
	if err := st.Users().CreateUser(ctx, &store.User{ID: "usr_local_admin", Username: "admin", Role: "admin", Status: "active", SSOProvider: "local"}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"id":"kid-root","userName":"admin","roles":[{"value":"admin","primary":true}],"active":true,"meta":{"resourceType":"User","version":"W/\"1\""}}`)
	req := httptest.NewRequest("POST", "/api/sso/kyidentity/sync", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/scim+json")
	h, err := syncauth.Sign([]byte(cfg.SSO.KyIdentityHMACSecret), time.Now(), "user.created", uuid.NewString(), body)
	if err != nil {
		t.Fatal(err)
	}
	h.Apply(req)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `\"admin\"`) || strings.Contains(w.Body.String(), cfg.SSO.KyIdentityHMACSecret) {
		t.Fatalf("clash: %d %s", w.Code, w.Body.String())
	}
	if woke != 0 {
		t.Fatal("a refused delivery woke the sweep")
	}
}
