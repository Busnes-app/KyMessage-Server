package sso_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func TestOAuthAuthorizationURLUsesDiscoveryAndPKCE(t *testing.T) {
	var issuer string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
			"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
		})
	}))
	defer idp.Close()
	issuer = idp.URL

	client := sso.NewKySignOnClient(config.SSOConfig{KySignOnIssuer: issuer, KySignOnClientID: "client"}, nil)
	authURL, err := client.BuildAuthURL(context.Background(), "https://app.example/callback", "state", "verifier", "nonce", false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Path != "/authorize" || query.Get("state") != "state" || query.Get("nonce") != "nonce" || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		t.Fatalf("unexpected authorization URL: %s", authURL)
	}
	if query.Has("max_age") || query.Has("prompt") {
		t.Fatal("ordinary login unexpectedly forces reauthentication")
	}
	fresh, err := client.BuildAuthURL(context.Background(), "https://app.example/callback", "state", "verifier", "nonce", true)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, _ := url.Parse(fresh); parsed.Query().Get("prompt") != "login" || parsed.Query().Get("max_age") != "0" {
		t.Fatalf("fresh login does not force a credential interaction: %s", fresh)
	}
}

func TestKySignOnWebhookSync(t *testing.T) {
	st, err := store.Open(context.Background(), testdb.Config(t))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	hmacSecret := "webhook-secret-999"
	client := sso.NewKySignOnClient(config.SSOConfig{
		KySignOnHMACSecret: hmacSecret,
	}, st)

	payload := sso.KySignOnSyncPayload{
		Event:       "user.created",
		ID:          "ext-usr-456",
		Username:    "bob",
		Email:       "bob@busnes.app",
		DisplayName: "Bob Engineer",
		Role:        "user",
		Status:      "active",
		Timestamp:   time.Now().Unix(),
	}
	body, _ := json.Marshal(payload)
	sig := crypto.ComputeHMACSHA256(body, hmacSecret)

	// 1. Sync create user
	if err := client.HandleSyncWebhook(context.Background(), body, sig); err != nil {
		t.Fatalf("HandleSyncWebhook failed: %v", err)
	}

	created, err := st.Users().GetUserBySSO(context.Background(), "kysignon", "ext-usr-456")
	if err != nil {
		t.Fatalf("GetUserBySSO failed: %v", err)
	}
	if created.Username != "bob" || created.DisplayName != "Bob Engineer" {
		t.Errorf("unexpected created user: %+v", created)
	}

	// 2. Sync deactivation
	payload.Event = "user.deactivated"
	body, _ = json.Marshal(payload)
	sig = crypto.ComputeHMACSHA256(body, hmacSecret)

	if err := client.HandleSyncWebhook(context.Background(), body, sig); err != nil {
		t.Fatalf("HandleSyncWebhook deactivation failed: %v", err)
	}

	updated, _ := st.Users().GetUserBySSO(context.Background(), "kysignon", "ext-usr-456")
	if updated.Status != "inactive" {
		t.Errorf("expected inactive status, got %s", updated.Status)
	}
}

func TestSAMLServiceProvider(t *testing.T) {
	sp := sso.NewSAMLServiceProvider("https://app.busnes.app/saml/metadata", "https://app.busnes.app/saml/acs")

	metadata := sp.GenerateMetadata()
	if len(metadata) == 0 || !testing.Verbose() && len(metadata) < 50 {
		if len(metadata) == 0 {
			t.Errorf("expected non-empty metadata")
		}
	}

}

// A captured or delayed delivery must not undo a later one, even after a restart: replaying an
// old promotion after a demotion would restore admin.
func TestKySignOnWebhookIgnoresReplayAndStaleUpdates(t *testing.T) {
	st, err := store.Open(context.Background(), testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const secret = "webhook-secret"
	cfg := config.SSOConfig{KySignOnHMACSecret: secret}
	sign := func(subject, role string, ts int64) ([]byte, string) {
		body, _ := json.Marshal(sso.KySignOnSyncPayload{Event: "user.updated", ID: subject, Username: subject, Role: role, Status: "active", Timestamp: ts})
		return body, crypto.ComputeHMACSHA256(body, secret)
	}
	deliver := func(client *sso.KySignOnClient, body []byte, sig string) {
		t.Helper()
		if err := client.HandleSyncWebhook(context.Background(), body, sig); err != nil {
			t.Fatal(err)
		}
	}
	roleOf := func(subject string) string {
		t.Helper()
		u, err := st.Users().GetUserBySSO(context.Background(), "kysignon", subject)
		if err != nil {
			t.Fatal(err)
		}
		return u.Role
	}
	now := time.Now().Unix()

	client := sso.NewKySignOnClient(cfg, st)
	promote, promoteSig := sign("carol", "admin", now-2)
	deliver(client, promote, promoteSig)
	demote, demoteSig := sign("carol", "user", now-1)
	deliver(client, demote, demoteSig)
	// A restarted process keeps the order, because it lives with the user row.
	restarted := sso.NewKySignOnClient(cfg, st)
	deliver(restarted, promote, promoteSig)
	delayed, delayedSig := sign("carol", "admin", now-3)
	deliver(restarted, delayed, delayedSig)
	if got := roleOf("carol"); got != "user" {
		t.Fatalf("role is %q after replayed and stale promotions, want user", got)
	}

	// Same-second updates cannot be ordered, so a tie may only lower privilege, in either
	// delivery order.
	for _, order := range [][2]string{{"admin", "user"}, {"user", "admin"}} {
		subject := "dave-" + order[0]
		base, baseSig := sign(subject, "user", now-10)
		deliver(client, base, baseSig)
		for _, role := range order {
			body, sig := sign(subject, role, now)
			deliver(client, body, sig)
		}
		if got := roleOf(subject); got != "user" {
			t.Errorf("same-second updates delivered as %v left role %q, want user", order, got)
		}
	}
	later, laterSig := sign("dave-admin", "admin", now+1)
	deliver(client, later, laterSig)
	if got := roleOf("dave-admin"); got != "admin" {
		t.Fatalf("a strictly newer promotion was refused: %q", got)
	}
}

// Deleting an account must not delete its ordering record: a superseded creation or update,
// replayed later or after a restart, would otherwise bring the account back.
func TestKySignOnWebhookCannotResurrectDeletedSubject(t *testing.T) {
	st, err := store.Open(context.Background(), testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const secret = "webhook-secret"
	cfg := config.SSOConfig{KySignOnHMACSecret: secret}
	send := func(client *sso.KySignOnClient, event, subject, role string, ts int64) {
		t.Helper()
		body, _ := json.Marshal(sso.KySignOnSyncPayload{Event: event, ID: subject, Username: subject, Role: role, Status: "active", Timestamp: ts})
		if err := client.HandleSyncWebhook(context.Background(), body, crypto.ComputeHMACSHA256(body, secret)); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(subject string) bool {
		_, err := st.Users().GetUserBySSO(context.Background(), "kysignon", subject)
		return err == nil
	}
	now := time.Now().Unix()

	client := sso.NewKySignOnClient(cfg, st)
	send(client, "user.created", "erin", "admin", now-3)
	send(client, "user.deleted", "erin", "", now-1)
	restarted := sso.NewKySignOnClient(cfg, st)
	send(restarted, "user.created", "erin", "admin", now-3)
	send(restarted, "user.updated", "erin", "admin", now-2)
	send(restarted, "user.created", "erin", "admin", now-1) // a tie never recreates
	if exists("erin") {
		t.Fatal("a superseded update recreated a deleted subject")
	}
	// A deletion for a subject never seen here still blocks an older creation.
	send(restarted, "user.deleted", "frank", "", now-1)
	send(restarted, "user.created", "frank", "admin", now-2)
	if exists("frank") {
		t.Fatal("an older creation beat a recorded deletion")
	}
	send(restarted, "user.created", "erin", "user", now)
	if !exists("erin") {
		t.Fatal("a newer creation after deletion was refused")
	}
}
