package sso_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
	"github.com/google/uuid"
)

// secret is shaped like the one KyIdentity shows once when pairing a suite webhook: 64 hex
// characters, used as ASCII key bytes.
const webhookSecret = "4f1c2a9e8b7d6c5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e"

type directory struct {
	t      *testing.T
	st     store.Store
	client *sso.KyIdentityClient
}

func newDirectory(t *testing.T) *directory {
	t.Helper()
	st, err := store.Open(context.Background(), testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &directory{t: t, st: st, client: sso.NewKyIdentityClient(config.SSOConfig{KyIdentityHMACSecret: webhookSecret}, st)}
}

// restart is a new process on the same database.
func (d *directory) restart() {
	d.client = sso.NewKyIdentityClient(config.SSOConfig{KyIdentityHMACSecret: webhookSecret}, d.st)
}

// scimUser is the body KyIdentity's dispatcher sends (FormatUserAsSCIM plus meta.version).
func scimUser(subject, role string, active bool, revision int64) []byte {
	body, _ := json.Marshal(map[string]any{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"id":          subject,
		"externalId":  subject,
		"userName":    subject,
		"displayName": "Name " + subject,
		"name":        map[string]string{"formatted": "Name " + subject},
		"emails":      []map[string]any{{"value": subject + "@example.com", "type": "work", "primary": true}},
		"roles":       []map[string]any{{"value": role, "primary": true}},
		"active":      active,
		"meta":        map[string]any{"resourceType": "User", "version": fmt.Sprintf(`W/"%d"`, revision)},
	})
	return body
}

func (d *directory) send(eventType string, body []byte) error {
	return d.sendID(uuid.NewString(), eventType, body)
}

// sendID signs a delivery with a given event ID, as KyIdentity's outbox does on every retry.
func (d *directory) sendID(eventID, eventType string, body []byte) error {
	h, err := syncauth.Sign([]byte(webhookSecret), time.Now(), eventType, eventID, body)
	if err != nil {
		d.t.Fatal(err)
	}
	return d.client.HandleSyncWebhook(context.Background(), h, body)
}

func (d *directory) must(eventType string, body []byte) {
	d.t.Helper()
	if err := d.send(eventType, body); err != nil {
		d.t.Fatalf("%s: %v", eventType, err)
	}
}

func (d *directory) user(subject string) *store.User {
	d.t.Helper()
	u, err := d.st.Users().GetUserBySSO(context.Background(), "kyidentity", subject)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		d.t.Fatal(err)
	}
	return u
}

func TestDirectoryWebhookAppliesKyIdentityEvents(t *testing.T) {
	d := newDirectory(t)
	d.must("user.created", scimUser("kid-bob", "user", true, 1))
	u := d.user("kid-bob")
	if u == nil || u.Username != "kid-bob" || u.Email != "kid-bob@example.com" || u.DisplayName != "Name kid-bob" || u.Role != "user" || u.Status != "active" {
		t.Fatalf("created user: %+v", u)
	}
	d.must("user.updated", scimUser("kid-bob", "admin", true, 2))
	if u := d.user("kid-bob"); u.Role != "admin" {
		t.Fatalf("promotion not applied: %+v", u)
	}
	// KyIdentity deactivates with user.updated and active:false.
	d.must("user.updated", scimUser("kid-bob", "admin", false, 3))
	if u := d.user("kid-bob"); u.Status != "inactive" {
		t.Fatalf("deactivation not applied: %+v", u)
	}
	// Roles other than admin (for example KyIdentity per-app roles) map to user.
	d.must("user.updated", scimUser("kid-bob", "editor", true, 4))
	if u := d.user("kid-bob"); u.Role != "user" || u.Status != "active" {
		t.Fatalf("unknown role not mapped to user: %+v", u)
	}
	d.must("user.deleted", []byte(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"id":"kid-bob","externalId":"kid-bob","userName":"","active":false,"roles":[],"meta":{"resourceType":"User","version":"W/\"5\""}}`))
	if d.user("kid-bob") != nil {
		t.Fatal("user.deleted did not delete")
	}
	d.must("group.updated", []byte(`{"id":"g1","meta":{"version":"W/\"1\""}}`)) // not for this product
}

func TestDirectoryWebhookRejectsUnauthenticatedAndMalformed(t *testing.T) {
	d := newDirectory(t)
	body := scimUser("kid-eve", "admin", true, 1)
	h, _ := syncauth.Sign([]byte(webhookSecret), time.Now(), "user.created", uuid.NewString(), body)
	for name, tc := range map[string]struct {
		h    syncauth.Headers
		body []byte
	}{
		"tampered body":     {h, scimUser("kid-eve", "admin", true, 2)},
		"retyped event":     {syncauth.Headers{Signature: h.Signature, Timestamp: h.Timestamp, EventType: "user.deleted", EventID: h.EventID}, body},
		"no signature":      {syncauth.Headers{Timestamp: h.Timestamp, EventType: h.EventType, EventID: h.EventID}, body},
		"stale timestamp":   {signAt(t, time.Now().Add(-10*time.Minute), body), body},
		"old custom format": {syncauth.Headers{Signature: "deadbeef"}, []byte(`{"event":"user.created","id":"kid-eve","role":"admin"}`)},
	} {
		if err := d.client.HandleSyncWebhook(context.Background(), tc.h, tc.body); !errors.Is(err, sso.ErrSyncUnauthorized) {
			t.Errorf("%s: got %v, want ErrSyncUnauthorized", name, err)
		}
	}
	for name, body := range map[string][]byte{
		"no revision": []byte(`{"id":"kid-eve","userName":"eve","active":true,"roles":[{"value":"admin","primary":true}]}`),
		"bad version": []byte(`{"id":"kid-eve","active":true,"meta":{"version":"1"}}`),
		"no id":       []byte(`{"active":true,"meta":{"version":"W/\"1\""}}`),
	} {
		if err := d.send("user.created", body); !errors.Is(err, sso.ErrSyncMalformed) {
			t.Errorf("%s: got %v, want ErrSyncMalformed", name, err)
		}
	}
	if d.user("kid-eve") != nil {
		t.Fatal("a refused event created a user")
	}
	unconfigured := sso.NewKyIdentityClient(config.SSOConfig{}, d.st)
	if err := unconfigured.HandleSyncWebhook(context.Background(), h, body); !errors.Is(err, sso.ErrSyncUnauthorized) {
		t.Fatalf("no secret: got %v", err)
	}
}

func signAt(t *testing.T, at time.Time, body []byte) syncauth.Headers {
	h, err := syncauth.Sign([]byte(webhookSecret), at, "user.created", uuid.NewString(), body)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// KyIdentity's outbox retries with new signatures, so the same revision can arrive twice
// and older revisions can arrive late. Neither may undo a newer state, including after a
// restart; the order is the per-user revision, not arrival or timestamp.
func TestDirectoryWebhookOrdersByRevision(t *testing.T) {
	d := newDirectory(t)
	d.must("user.created", scimUser("kid-carol", "admin", true, 1))
	d.must("user.updated", scimUser("kid-carol", "user", true, 2))
	d.restart()
	d.must("user.updated", scimUser("kid-carol", "admin", true, 1)) // delayed older revision
	d.must("user.updated", scimUser("kid-carol", "admin", true, 2)) // same revision, different body
	if u := d.user("kid-carol"); u.Role != "user" {
		t.Fatalf("an old or duplicate revision changed the role: %+v", u)
	}
	d.must("user.updated", scimUser("kid-carol", "admin", true, 3))
	if u := d.user("kid-carol"); u.Role != "admin" {
		t.Fatalf("a newer revision was refused: %+v", u)
	}
	// After its own restore KyIdentity resends state at -1 and counts up from there.
	d.must("user.updated", scimUser("kid-carol", "user", true, -1))
	d.must("user.updated", scimUser("kid-carol", "admin", true, 1))
	if u := d.user("kid-carol"); u.Role != "admin" {
		t.Fatalf("revisions after a -1 resend did not apply: %+v", u)
	}
}

// The order record outlives the account, so a superseded creation cannot resurrect it.
func TestDirectoryWebhookCannotResurrectDeletedSubject(t *testing.T) {
	d := newDirectory(t)
	d.must("user.created", scimUser("kid-erin", "admin", true, 1))
	d.must("user.deleted", scimUser("kid-erin", "", false, 3))
	d.restart()
	d.must("user.created", scimUser("kid-erin", "admin", true, 1))
	d.must("user.updated", scimUser("kid-erin", "admin", true, 2))
	d.must("user.created", scimUser("kid-erin", "admin", true, 3))
	if d.user("kid-erin") != nil {
		t.Fatal("a superseded event recreated a deleted subject")
	}
	// KyIdentity deletes a never-delivered user at revision 0; that still blocks nothing newer.
	d.must("user.deleted", scimUser("kid-frank", "", false, 0))
	d.must("user.created", scimUser("kid-frank", "user", true, 1))
	if d.user("kid-frank") == nil {
		t.Fatal("a newer creation after a revision-0 deletion was refused")
	}
	d.must("user.created", scimUser("kid-erin", "user", true, 4))
	if d.user("kid-erin") == nil {
		t.Fatal("a newer creation after deletion was refused")
	}
}

// A -1 resend resets the order, so a redelivered copy after a later demotion must not restore
// the old role, including after a restart. A different restore event still applies.
func TestDirectoryWebhookAppliesEachRestoreEventOnce(t *testing.T) {
	d := newDirectory(t)
	d.must("user.created", scimUser("kid-gail", "user", true, 5))
	restore := uuid.NewString()
	if err := d.sendID(restore, "user.updated", scimUser("kid-gail", "admin", true, -1)); err != nil {
		t.Fatal(err)
	}
	d.must("user.updated", scimUser("kid-gail", "user", true, 1)) // demotion after the restore
	d.restart()
	if err := d.sendID(restore, "user.updated", scimUser("kid-gail", "admin", true, -1)); err != nil {
		t.Fatalf("a duplicate must still be acknowledged: %v", err)
	}
	if u := d.user("kid-gail"); u.Role != "user" {
		t.Fatalf("a redelivered restore event undid the demotion: %+v", u)
	}
	if err := d.sendID(uuid.NewString(), "user.updated", scimUser("kid-gail", "admin", true, -1)); err != nil {
		t.Fatal(err)
	}
	if u := d.user("kid-gail"); u.Role != "admin" {
		t.Fatalf("a new restore event was refused: %+v", u)
	}
}

// A -1 reset lowers the stored revision, so revision order alone would let an earlier event
// apply again. Every delivered event ID stays spent: one applied before the reset, and one
// refused as stale before it, both remain ineffective across the reset and a restart.
func TestDirectoryWebhookEventsStaySpentAcrossRevisionReset(t *testing.T) {
	d := newDirectory(t)
	promote, stale := uuid.NewString(), uuid.NewString()
	d.must("user.created", scimUser("kid-hana", "user", true, 1))
	if err := d.sendID(promote, "user.updated", scimUser("kid-hana", "admin", true, 3)); err != nil {
		t.Fatal(err)
	}
	d.must("user.updated", scimUser("kid-hana", "user", true, 4))
	if err := d.sendID(stale, "user.updated", scimUser("kid-hana", "admin", true, 2)); err != nil {
		t.Fatal(err)
	}
	d.must("user.updated", scimUser("kid-hana", "user", true, -1)) // KyIdentity restored
	d.restart()
	for _, id := range []string{promote, stale} {
		if err := d.sendID(id, "user.updated", scimUser("kid-hana", "admin", true, map[string]int64{promote: 3, stale: 2}[id])); err != nil {
			t.Fatalf("a spent event must still be acknowledged: %v", err)
		}
	}
	if u := d.user("kid-hana"); u.Role != "user" {
		t.Fatalf("an event from before the reset applied again: %+v", u)
	}
	d.must("user.updated", scimUser("kid-hana", "admin", true, 1)) // new events still apply
	if u := d.user("kid-hana"); u.Role != "admin" {
		t.Fatalf("a new event after the reset was refused: %+v", u)
	}
}
