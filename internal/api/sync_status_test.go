package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/google/uuid"
)

const syncSecret = "4f1c2a9e8b7d6c5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e"

type syncStatusBody struct {
	Webhook       *sso.WebhookRecord      `json:"webhook"`
	Rejected      sso.Rejections          `json:"rejected"`
	Sweep         *matrixsync.SweepRecord `json:"sweep"`
	KyIdentityURL string                  `json:"kyidentity_url"`
}

func TestSyncStatusShowsRecordsAndRejections(t *testing.T) {
	ctx := context.Background()
	_, st, cfg := setupTestServer(t)
	cfg.SSO.KyIdentityHMACSecret = syncSecret
	cfg.SSO.KyIdentityIssuer = "https://id.example.com"
	srv := api.NewServer(cfg, st) // the KyIdentity client copies the secret at construction
	srv.SetMatrixAdmin(&fakeMAS{finished: map[string]bool{}})
	admin := loginAs(t, srv, st, "root", "admin")
	get := func() syncStatusBody {
		t.Helper()
		w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/sync-status", nil)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control %q", w.Header().Get("Cache-Control"))
		}
		return decode[syncStatusBody](t, w)
	}
	if s := get(); s.Webhook != nil || s.Sweep != nil || s.Rejected.Count != 0 || s.KyIdentityURL != "https://id.example.com" {
		t.Fatalf("before any delivery: %+v", s)
	}
	body := []byte(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"id":"kid-9","userName":"nine","roles":[],"active":true,"meta":{"resourceType":"User","version":"W/\"1\""}}`)
	post := func(h syncauth.Headers) int {
		req := httptest.NewRequest("POST", "/api/sso/kyidentity/sync", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/scim+json")
		h.Apply(req)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	signed, err := syncauth.Sign([]byte(syncSecret), time.Now(), "user.created", uuid.NewString(), body)
	if err != nil {
		t.Fatal(err)
	}
	if code := post(signed); code != http.StatusOK {
		t.Fatalf("signed delivery: %d", code)
	}
	bad := signed
	bad.Signature = "v1=" + strings.Repeat("0", 64)
	if code := post(bad); code != http.StatusUnauthorized {
		t.Fatalf("badly signed delivery: %d", code)
	}
	if s := get(); s.Webhook == nil || s.Webhook.Kind != "user.created" || s.Rejected.Count != 1 ||
		s.Rejected.LastReason != sso.RejectBadSignature || s.Rejected.LastAt == nil {
		t.Fatalf("after one accepted and one refused delivery: %+v", s)
	}
	since := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	rec, _ := json.Marshal(matrixsync.SweepRecord{FinishedAt: time.Now().UTC(), Error: "MAS GET /api/admin/v1/users: HTTP 500", Failed: 1, FailingSince: &since})
	if err := st.Settings().SetSetting(ctx, matrixsync.SweepRecordKey, string(rec)); err != nil {
		t.Fatal(err)
	}
	if s := get(); s.Sweep == nil || s.Sweep.OK || s.Sweep.FailingSince == nil || !s.Sweep.FailingSince.Equal(since) {
		t.Fatalf("failing sweep: %+v", s.Sweep)
	}
	// An unreadable record shows as none, never as a 500.
	if err := st.Settings().SetSetting(ctx, matrixsync.SweepRecordKey, "{"); err != nil {
		t.Fatal(err)
	}
	if s := get(); s.Sweep != nil {
		t.Fatalf("unreadable record shown: %+v", s.Sweep)
	}
}

// Review Focus 1: the sync records have their own route and stay out of every page load.
func TestSettingsHideTheSyncRecords(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := setupTestServer(t)
	for _, k := range []string{sso.WebhookRecordKey, matrixsync.SweepRecordKey} {
		if err := st.Settings().SetSetting(ctx, k, `{"marker":"sync-record"}`); err != nil {
			t.Fatal(err)
		}
	}
	body := do(t, srv, "GET", "/api/settings", loginAs(t, srv, st, "root", "admin")).Body.String()
	if strings.Contains(body, "sync-record") || !strings.Contains(body, "extra_settings") {
		t.Fatalf("settings: %s", body)
	}
}
