package store_test

import (
	"context"
	"github.com/Busnes-app/ky-primitives/applysetup"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"testing"
	"time"
)

func messageBundle() applysetup.Common {
	return applysetup.Common{Schema: applysetup.Schema, Service: "kymessage", SetupID: "12345678-1234-1234-1234-123456789abc", SSO: &applysetup.SSO{IssuerURL: "https://identity.example", ClientID: "message", ClientSecretFile: "/data/installer/client.secret", DirectorySecretFile: "/data/installer/directory.secret", RedirectURI: "https://message.example/api/sso/kyidentity/callback"}, Admins: []applysetup.Identity{{Issuer: "https://identity.example", Subject: "owner", Username: "owner-admin"}}, Everyday: []applysetup.Identity{{Issuer: "https://identity.example", Subject: "daily", Username: "everyday"}}}
}
func TestInstallerAtomicOwnershipAndDirectoryFence(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	b := messageBundle()
	if _, err := st.ApplyInstaller(ctx, b, true); err == nil {
		t.Fatal("missing check passed")
	}
	if n, _ := st.Users().CountUsers(ctx); n != 0 {
		t.Fatal("check mutated accounts")
	}
	r, err := st.ApplyInstaller(ctx, b, false)
	if err != nil || len(r.Created) != 3 {
		t.Fatalf("apply %+v %v", r, err)
	}
	for _, check := range []bool{true, false} {
		r, err = st.ApplyInstaller(ctx, b, check)
		if err != nil || len(r.Created) != 0 || len(r.Present) != 3 {
			t.Fatalf("resume %+v %v", r, err)
		}
	}
	if _, n, err := st.Audit().ListAuditRecords(ctx, 0, 100); err != nil || n != 1 {
		t.Fatalf("audit %d %v", n, err)
	}
	u, err := st.Users().GetUserBySSO(ctx, "kyidentity", "owner")
	if err != nil || u.Role != "admin" || u.PasswordHash != "" {
		t.Fatalf("owner %+v %v", u, err)
	}
	if _, allowed, err := st.InstallerIdentity(ctx, "https://foreign.example", "owner"); err != nil || allowed {
		t.Fatalf("foreign issuer %v %v", allowed, err)
	}
	if err = st.ApplyInstallerDirectory(ctx, b.SSO.IssuerURL, "owner", "user.deactivated", "2", "digest-off", false, false); err != nil {
		t.Fatal(err)
	}
	if err = st.ApplyInstallerDirectory(ctx, b.SSO.IssuerURL, "owner", "user.deactivated", "2", "digest-off", false, false); err != nil {
		t.Fatal(err)
	}
	if err = st.ApplyInstallerDirectory(ctx, b.SSO.IssuerURL, "owner", "user.updated", "1", "digest-on", true, true); err == nil {
		t.Fatal("stale activation accepted")
	}
	if err = st.ApplyInstallerDirectory(ctx, b.SSO.IssuerURL, "owner", "user.updated", "2", "digest-other", true, true); err == nil {
		t.Fatal("conflicting revision accepted")
	}
	if err = st.ApplyInstallerDirectory(ctx, "https://foreign.example", "owner", "user.updated", "3", "digest-on", true, true); err == nil {
		t.Fatal("foreign issuer accepted")
	}
	if _, err = st.ApplyInstaller(ctx, b, false); err == nil {
		t.Fatal("setup reactivated revoked account")
	}
	if _, allowed, _ := st.InstallerIdentity(ctx, b.SSO.IssuerURL, "owner"); allowed {
		t.Fatal("offboarded owner allowed")
	}
	if _, n, err := st.Audit().ListAuditRecords(ctx, 0, 100); err != nil || n != 2 {
		t.Fatalf("duplicate audit %d %v", n, err)
	}
}
func TestInstallerForeignCollisionRollsBack(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	b := messageBundle()
	if err := st.Users().CreateUser(ctx, &store.User{ID: "foreign", Username: "EVERYDAY", Role: "admin", Status: "active", SSOProvider: "local", PasswordHash: "retained-hash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyInstaller(ctx, b, false); err == nil {
		t.Fatal("adopted foreign username")
	}
	if n, _ := st.Users().CountUsers(ctx); n != 1 {
		t.Fatal("partial creation survived")
	}
	if _, err := st.Settings().GetSetting(ctx, store.InstallerConfigKey); err != store.ErrNotFound {
		t.Fatalf("partial configuration %v", err)
	}
	u, _ := st.Users().GetUserByID(ctx, "foreign")
	if u.PasswordHash != "retained-hash" {
		t.Fatal("reset credential")
	}
}

func TestInstallerNativeWritersCannotChangeOwnedAuthority(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	b := messageBundle()
	if _, err := st.ApplyInstaller(ctx, b, false); err != nil {
		t.Fatal(err)
	}
	u, err := st.Users().GetUserBySSO(ctx, "kyidentity", "owner")
	if err != nil {
		t.Fatal(err)
	}
	u.Role = "user"
	if err = st.Users().UpdateProfile(ctx, u); err == nil {
		t.Fatal("native profile changed owned grant")
	}
	if err = st.Users().UpdateUser(ctx, u); err == nil {
		t.Fatal("native full update changed owned grant")
	}
	if err = st.Users().DeleteUser(ctx, u.ID); err == nil {
		t.Fatal("native delete removed owned account")
	}
	if err = st.Users().CreateUser(ctx, &store.User{ID: "duplicate", Username: "another-name", SSOProvider: "kyidentity", SSOSubject: "owner", Status: "active", Role: "admin"}); err == nil {
		t.Fatal("unowned managed identity created")
	}
	if err = st.Settings().DeleteSetting(ctx, store.InstallerConfigKey); err == nil {
		t.Fatal("owned settings deleted")
	}
	current, err := st.Users().GetUserByID(ctx, u.ID)
	if err != nil || current.Role != "admin" {
		t.Fatal(current, err)
	}
}

func TestInstallerDirectoryPreservesMatrixDelete(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	b := messageBundle()
	if _, err := st.ApplyInstaller(ctx, b, false); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyInstallerDirectory(ctx, b.SSO.IssuerURL, "daily", "user.deleted", "1", "deleted", false, false); err != nil {
		t.Fatal(err)
	}
	statuses, err := st.Users().DirectoryStatuses(ctx, "kyidentity")
	if err != nil || statuses["daily"] != "deleted" {
		t.Fatal(statuses, err)
	}
	if err = st.ApplyInstallerDirectory(ctx, b.SSO.IssuerURL, "daily", "user.created", "2", "recreate", true, false); err == nil {
		t.Fatal("deleted Matrix account reactivated")
	}
}

func TestInstallerMFAResetFencesInFlightPairing(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	b := messageBundle()
	if _, err := st.ApplyInstaller(ctx, b, false); err != nil {
		t.Fatal(err)
	}
	u, err := st.Users().GetUserBySSO(ctx, "kyidentity", "daily")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = st.Devices().CreatePairing(ctx, &store.DevicePairing{Secret: "in-flight", UserID: u.ID, Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Minute), AuthenticatedAt: now}, u.PasswordHash); err != nil {
		t.Fatal(err)
	}
	// Pairing is authorized: the user snapshot is read and the pairing consumed.
	snapshot, err := st.Users().GetUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Devices().ConsumePairing(ctx, "in-flight", "phone", "android", ""); err != nil {
		t.Fatal(err)
	}
	if err = st.ApplyInstallerDirectory(ctx, b.SSO.IssuerURL, "daily", "user.mfa_reset", "1", "reset", true, false); err != nil {
		t.Fatal(err)
	}
	sess := &store.Session{TokenHash: "derived", UserID: u.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err = st.Sessions().CreateSession(ctx, sess, snapshot.PasswordHash); err == nil {
		t.Fatal("session issued after MFA reset")
	}
	if _, err = st.Sessions().GetSession(ctx, "derived"); err != store.ErrNotFound {
		t.Fatalf("usable session after MFA reset: %v", err)
	}
	// A pairing initiated from a pre-reset snapshot is refused.
	if err = st.Devices().CreatePairing(ctx, &store.DevicePairing{Secret: "late", UserID: u.ID, Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, snapshot.PasswordHash); err == nil {
		t.Fatal("pairing created after MFA reset")
	}
}
