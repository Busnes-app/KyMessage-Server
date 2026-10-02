package sso_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/google/uuid"
)

func TestDeliveriesAreRecordedAndRejectionsCountedByReason(t *testing.T) {
	ctx := context.Background()
	d := newDirectory(t)
	if r := d.client.Rejections(); r.Count != 0 || r.LastAt != nil || r.LastReason != "" {
		t.Fatalf("fresh counters: %+v", r)
	}
	if _, err := d.st.Settings().GetSetting(ctx, sso.WebhookRecordKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a record before any delivery: %v", err)
	}
	d.must("user.created", scimUser("kid-1", "user", true, 1))
	raw, err := d.st.Settings().GetSetting(ctx, sso.WebhookRecordKey)
	var rec sso.WebhookRecord
	if err != nil || json.Unmarshal([]byte(raw), &rec) != nil || rec.Kind != "user.created" || time.Since(rec.At) > time.Minute {
		t.Fatalf("record %q: %v", raw, err)
	}
	body := scimUser("kid-eve", "admin", true, 1)
	h, err := syncauth.Sign([]byte(webhookSecret), time.Now(), "user.updated", uuid.NewString(), body)
	if err != nil {
		t.Fatal(err)
	}
	malformed := []byte(`{"id":"x"}`)
	for i, tc := range []struct {
		name string
		h    syncauth.Headers
		body []byte
		want string
	}{
		{"tampered body", h, scimUser("kid-eve", "admin", true, 2), sso.RejectBadSignature},
		{"no signature", syncauth.Headers{Timestamp: h.Timestamp, EventType: h.EventType, EventID: h.EventID}, body, sso.RejectBadSignature},
		{"stale", signAt(t, time.Now().Add(-10*time.Minute), body), body, sso.RejectStale},
		{"no event ID", syncauth.Headers{Signature: h.Signature, Timestamp: h.Timestamp, EventType: h.EventType}, body, sso.RejectBadHeaders},
		{"bad timestamp", syncauth.Headers{Signature: h.Signature, Timestamp: "yesterday", EventType: h.EventType, EventID: h.EventID}, body, sso.RejectBadHeaders},
		{"signed but malformed", signAt(t, time.Now(), malformed), malformed, sso.RejectMalformed},
	} {
		if err := d.client.HandleSyncWebhook(ctx, tc.h, tc.body); err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
		if r := d.client.Rejections(); r.Count != int64(i+1) || r.LastReason != tc.want || r.LastAt == nil {
			t.Errorf("%s: %+v, want reason %s", tc.name, r, tc.want)
		}
	}
	for _, secret := range []string{"", "too-short"} {
		c := sso.NewKyIdentityClient(config.SSOConfig{KyIdentityHMACSecret: secret}, d.st)
		_ = c.HandleSyncWebhook(ctx, h, body)
		if r := c.Rejections(); r.Count != 1 || r.LastReason != sso.RejectNotConfigured {
			t.Errorf("secret %q: %+v", secret, r)
		}
	}
	if again, _ := d.st.Settings().GetSetting(ctx, sso.WebhookRecordKey); again != raw {
		t.Fatalf("a refused delivery changed the record to %q", again)
	}
	d.restart()
	if r := d.client.Rejections(); r.Count != 0 {
		t.Fatalf("counters survived a restart: %+v", r)
	}
}

type countingSettings struct {
	store.SettingsStore
	mu     sync.Mutex
	writes int
}

func (c *countingSettings) SetSetting(ctx context.Context, k, v string) error {
	c.mu.Lock()
	c.writes++
	c.mu.Unlock()
	return c.SettingsStore.SetSetting(ctx, k, v)
}

type countingStore struct {
	store.Store
	settings *countingSettings
}

func (c countingStore) Settings() store.SettingsStore { return c.settings }

// Refused deliveries are unauthenticated: they cost no database write.
func TestRejectedDeliveriesWriteNoSetting(t *testing.T) {
	ctx := context.Background()
	d := newDirectory(t)
	cs := &countingSettings{SettingsStore: d.st.Settings()}
	client := sso.NewKyIdentityClient(config.SSOConfig{KyIdentityHMACSecret: webhookSecret}, countingStore{Store: d.st, settings: cs})
	body := scimUser("kid-r", "user", true, 1)
	for range 5 {
		_ = client.HandleSyncWebhook(ctx, syncauth.Headers{Signature: "v1=bad", Timestamp: time.Now().UTC().Format(time.RFC3339),
			EventType: "user.created", EventID: uuid.NewString()}, body)
	}
	if cs.writes != 0 || client.Rejections().Count != 5 {
		t.Fatalf("after 5 refusals: %d setting writes, %+v", cs.writes, client.Rejections())
	}
	h, err := syncauth.Sign([]byte(webhookSecret), time.Now(), "user.created", uuid.NewString(), body)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.HandleSyncWebhook(ctx, h, body); err != nil {
		t.Fatal(err)
	}
	if cs.writes != 1 {
		t.Fatalf("an accepted delivery wrote %d settings, want its one record", cs.writes)
	}
}
