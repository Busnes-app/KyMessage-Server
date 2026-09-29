package store_test

import (
	"context"
	"errors"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"sync"
	"testing"
	"time"
)

func TestPasswordReplacementIsAtomicAndSingleUse(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	u := &store.User{ID: "reset", Username: "reset", PasswordHash: "old", Role: "admin", Status: "active", SSOProvider: "local", MustChangePassword: true}
	if err := st.Users().CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	sess := &store.Session{TokenHash: "session", UserID: u.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.Sessions().CreateSession(ctx, sess, "old"); err != nil {
		t.Fatal(err)
	}
	challenge := &store.MFAChallenge{TokenHash: "challenge", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.Sessions().CreateMFAChallenge(ctx, challenge, "old"); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, hash := range []string{"new-one", "new-two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- st.Users().CompletePasswordChange(ctx, u.ID, "old", hash, "127.0.0.1")
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("%d successful changes", success)
	}
	if _, err := st.Sessions().GetSession(ctx, sess.TokenHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("session survived", err)
	}
	if _, _, err := st.Sessions().ConsumeMFAChallenge(ctx, challenge.TokenHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("challenge survived", err)
	}
	if err := st.Sessions().CreateSession(ctx, sess, "old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("stale session creation", err)
	}
	if err := st.Sessions().CreateMFAChallenge(ctx, challenge, "old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("stale MFA creation", err)
	}
	audits, n, err := st.Audit().ListAuditRecords(ctx, 0, 10)
	if err != nil || n != 1 || audits[0].Action != "auth.password_changed" {
		t.Fatal("audit", n, err)
	}
}

func TestAdminPasswordResetRevokesGrants(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	u := &store.User{ID: "reset", Username: "reset", PasswordHash: "old", Role: "admin", Status: "active", SSOProvider: "local", MustChangePassword: false}
	if err := st.Users().CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	sess := &store.Session{TokenHash: "session", UserID: u.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.Sessions().CreateSession(ctx, sess, "old"); err != nil {
		t.Fatal(err)
	}
	challenge := &store.MFAChallenge{TokenHash: "challenge", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.Sessions().CreateMFAChallenge(ctx, challenge, "old"); err != nil {
		t.Fatal(err)
	}
	if err := st.Devices().CreatePairing(ctx, &store.DevicePairing{Secret: "pair", UserID: u.ID, Status: "pending", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	u.Status = "disabled"
	if err := st.Users().UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().ResetAdminPassword(ctx, u.ID, "reset-hash"); err != nil {
		t.Fatal(err)
	}
	updated, err := st.Users().GetUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.MustChangePassword || updated.PasswordHash != "reset-hash" || updated.Status != "active" || updated.Role != "admin" {
		t.Fatalf("unexpected reset state: %+v", updated)
	}
	if _, err := st.Devices().GetPairingBySecret(ctx, "pair"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("pairing survived", err)
	}

	if _, err := st.Sessions().GetSession(ctx, sess.TokenHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("session survived", err)
	}
	if _, _, err := st.Sessions().ConsumeMFAChallenge(ctx, challenge.TokenHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("challenge survived", err)
	}
	if err := st.Sessions().CreateSession(ctx, sess, "old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("stale session creation", err)
	}
	if err := st.Sessions().CreateMFAChallenge(ctx, challenge, "old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("stale MFA creation", err)
	}
	audits, n, err := st.Audit().ListAuditRecords(ctx, 0, 10)
	if err != nil || n != 1 || audits[0].Action != "auth.password_changed" {
		t.Fatal("audit", n, err)
	}
}

// Provisioning reads a user, edits it and writes it back. That stale copy must not undo a
// password change or a spent recovery code that landed in between.
func TestProfileUpdateKeepsConcurrentCredentialChanges(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.Users().CreateUser(ctx, &store.User{
		ID: "usr_race", Username: "race", PasswordHash: "old", RecoveryCodesHash: `["c1","c2"]`,
		Role: "admin", Status: "active", SSOProvider: "local", MustChangePassword: true,
	}); err != nil {
		t.Fatal(err)
	}
	stale, err := st.Users().GetUserByID(ctx, "usr_race")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Users().CompletePasswordChange(ctx, "usr_race", "old", "new", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().UpdateRecoveryCodes(ctx, "usr_race", `["c1","c2"]`, `["c2"]`); err != nil {
		t.Fatal(err)
	}
	stale.DisplayName = "Race Condition"
	if err := st.Users().UpdateProfile(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, err := st.Users().GetUserByID(ctx, "usr_race")
	if err != nil {
		t.Fatal(err)
	}
	if got.PasswordHash != "new" || got.MustChangePassword || got.RecoveryCodesHash != `["c2"]` || got.DisplayName != "Race Condition" {
		t.Fatalf("profile write reverted credentials: %+v", got)
	}
}
