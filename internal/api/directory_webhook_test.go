package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/google/uuid"
)

// The route must accept exactly what KyIdentity's dispatcher sends: a SCIM User body with
// application/scim+json and syncauth headers, answering 200 so its outbox marks it done.
func TestDirectoryWebhookRouteSpeaksKyIdentity(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	cfg.SSO.KySignOnHMACSecret = "4f1c2a9e8b7d6c5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e"
	srv := api.NewServer(cfg, st)
	post := func(body []byte, sign bool) int {
		req := httptest.NewRequest("POST", "/api/sso/kysignon/sync", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/scim+json")
		if sign {
			h, err := syncauth.Sign([]byte(cfg.SSO.KySignOnHMACSecret), time.Now(), "user.created", uuid.NewString(), body)
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
	if _, err := st.Users().GetUserBySSO(context.Background(), "kysignon", "kid-1"); err != nil {
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
