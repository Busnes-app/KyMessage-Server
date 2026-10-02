package store_test

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
	"github.com/google/uuid"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), testdb.Config(t))
	if err != nil {
		t.Fatalf("failed to open test store: %v", err)
	}

	t.Cleanup(func() {
		_ = st.Close()
	})

	return st
}

func TestUserStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	userID := uuid.NewString()
	user := &store.User{
		ID:           userID,
		Username:     "alice",
		Email:        "alice@busnes.app",
		DisplayName:  "Alice Admin",
		PasswordHash: "argon2id$mockedhash",
		Role:         "admin",
		Status:       "active",
		SSOProvider:  "local",
	}

	// 1. Create
	if err := st.Users().CreateUser(ctx, user); err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Duplicate should fail
	if err := st.Users().CreateUser(ctx, user); err != store.ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}

	// 2. GetByID
	got, err := st.Users().GetUserByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetUserByID error: %v", err)
	}
	if got.Username != "alice" || got.DisplayName != "Alice Admin" {
		t.Errorf("unexpected user data: %+v", got)
	}

	// 3. GetByUsername (case-insensitive)
	gotByU, err := st.Users().GetLocalUserByUsername(ctx, "ALICE")
	if err != nil {
		t.Fatalf("GetLocalUserByUsername error: %v", err)
	}
	if gotByU.ID != userID {
		t.Errorf("expected ID %s, got %s", userID, gotByU.ID)
	}

	// 4. GetByEmail
	gotByE, err := st.Users().GetUserByEmail(ctx, "alice@busnes.app")
	if err != nil {
		t.Fatalf("GetUserByEmail error: %v", err)
	}
	if gotByE.ID != userID {
		t.Errorf("expected ID %s, got %s", userID, gotByE.ID)
	}

	// 5. Update
	user.DisplayName = "Alice Operations"
	user.Role = "manager"
	if err := st.Users().UpdateUser(ctx, user); err != nil {
		t.Fatalf("UpdateUser error: %v", err)
	}
	gotUpdated, _ := st.Users().GetUserByID(ctx, userID)
	if gotUpdated.DisplayName != "Alice Operations" || gotUpdated.Role != "manager" {
		t.Errorf("update not reflected: %+v", gotUpdated)
	}

	// 6. List & Count
	users, count, err := st.Users().ListUsers(ctx, 0, 10, store.UserFilter{Field: store.UserFieldUsername, Value: "ALICE"})
	if err != nil {
		t.Fatalf("ListUsers error: %v", err)
	}
	if count != 1 || len(users) != 1 {
		t.Errorf("expected 1 user, got count=%d len=%d", count, len(users))
	}

	// 7. Delete
	if err := st.Users().DeleteUser(ctx, userID); err != nil {
		t.Fatalf("DeleteUser error: %v", err)
	}
	if _, err := st.Users().GetUserByID(ctx, userID); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound after deletion, got %v", err)
	}
}

func TestSessionStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	userID := uuid.NewString()
	user := &store.User{
		ID:       userID,
		Username: "bob",
		Role:     "user",
		Status:   "active",
	}
	_ = st.Users().CreateUser(ctx, user)

	tokenHash := "mockhash12345"
	sess := &store.Session{
		TokenHash: tokenHash,
		UserID:    userID,
		UserAgent: "Mozilla/5.0 BusnesApp",
		IPAddress: "127.0.0.1",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
	}

	if err := st.Sessions().CreateSession(ctx, sess, user.PasswordHash); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	got, err := st.Sessions().GetSession(ctx, tokenHash)
	if err != nil {
		t.Fatalf("GetSession error: %v", err)
	}
	if got.UserID != userID {
		t.Errorf("expected userID %s, got %s", userID, got.UserID)
	}

	if err := st.Sessions().DeleteSession(ctx, tokenHash); err != nil {
		t.Fatalf("DeleteSession error: %v", err)
	}
	if _, err := st.Sessions().GetSession(ctx, tokenHash); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestDevicePairingLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	pairing := &store.DevicePairing{
		Secret:     "secret-pairing-token-abc",
		DeviceName: "Yoshi's Pixel 9",
		Platform:   "android",
		Status:     "pending",
		CreatedAt:  time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(90 * time.Second),
	}

	if err := st.Devices().CreatePairing(ctx, pairing); err != nil {
		t.Fatalf("CreatePairing error: %v", err)
	}

	bySecret, err := st.Devices().GetPairingBySecret(ctx, "secret-pairing-token-abc")
	if err != nil {
		t.Fatalf("GetPairingBySecret error: %v", err)
	}
	if bySecret.Status != "pending" {
		t.Errorf("unexpected status: %s", bySecret.Status)
	}

	if err := st.Devices().ConsumePairing(ctx, pairing.Secret, "Pixel", "android", "fcm-token-xyz"); err != nil {
		t.Fatalf("ConsumePairing error: %v", err)
	}

	updated, _ := st.Devices().GetPairingBySecret(ctx, pairing.Secret)
	if updated.Status != "consumed" || updated.PushToken != "fcm-token-xyz" {
		t.Errorf("pairing update failed: %+v", updated)
	}
}

func TestGroupStoreAndMembers(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	u1 := &store.User{ID: uuid.NewString(), Username: "u1", Role: "user", Status: "active"}
	u2 := &store.User{ID: uuid.NewString(), Username: "u2", Role: "user", Status: "active"}
	_ = st.Users().CreateUser(ctx, u1)
	_ = st.Users().CreateUser(ctx, u2)

	grp := &store.Group{
		ID:          uuid.NewString(),
		DisplayName: "Engineering",
		ExternalID:  "okta-grp-eng-001",
	}

	if err := st.Groups().CreateGroup(ctx, grp); err != nil {
		t.Fatalf("CreateGroup error: %v", err)
	}

	_ = st.Groups().AddGroupMember(ctx, grp.ID, u1.ID)
	_ = st.Groups().AddGroupMember(ctx, grp.ID, u2.ID)

	got, err := st.Groups().GetGroupByID(ctx, grp.ID)
	if err != nil {
		t.Fatalf("GetGroupByID error: %v", err)
	}
	if len(got.Members) != 2 {
		t.Errorf("expected 2 members, got %d", len(got.Members))
	}

	u1Groups, err := st.Groups().GetUserGroups(ctx, u1.ID)
	if err != nil {
		t.Fatalf("GetUserGroups error: %v", err)
	}
	if len(u1Groups) != 1 || u1Groups[0].DisplayName != "Engineering" {
		t.Errorf("unexpected user groups: %+v", u1Groups)
	}
}

func TestAuditAndSettings(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// Audit log
	rec := &store.AuditRecord{
		UserID:    "user-1",
		Action:    "auth.login",
		Resource:  "session",
		Details:   `{"method":"totp"}`,
		IPAddress: "127.0.0.1",
	}
	if err := st.Audit().LogAudit(ctx, rec); err != nil {
		t.Fatalf("LogAudit error: %v", err)
	}

	records, count, err := st.Audit().ListAuditRecords(ctx, 0, 10)
	if err != nil || count != 1 || len(records) != 1 {
		t.Fatalf("ListAuditRecords failed: count=%d, err=%v", count, err)
	}

	// Settings
	if err := st.Settings().SetSetting(ctx, "theme_default", "patina"); err != nil {
		t.Fatalf("SetSetting error: %v", err)
	}
	val, err := st.Settings().GetSetting(ctx, "theme_default")
	if err != nil || val != "patina" {
		t.Fatalf("GetSetting failed: val=%s, err=%v", val, err)
	}
}

func TestSpendTOTPCounterRefusesReplay(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u := &store.User{ID: "usr_t", Username: "t", Role: "user", Status: "active", SSOProvider: "local"}
	if err := st.Users().CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().SpendTOTPCounter(ctx, u.ID, 100); err != nil {
		t.Fatalf("first spend: %v", err)
	}
	if err := st.Users().SpendTOTPCounter(ctx, u.ID, 100); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("replay: got %v, want ErrAlreadyExists", err)
	}
	if err := st.Users().SpendTOTPCounter(ctx, u.ID, 99); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("older counter: got %v, want ErrAlreadyExists", err)
	}
	if err := st.Users().SpendTOTPCounter(ctx, u.ID, 101); err != nil {
		t.Fatalf("next counter: %v", err)
	}
	got, _ := st.Users().GetUserByID(ctx, u.ID)
	if got.TOTPLastCounter != 101 {
		t.Fatalf("stored counter %d, want 101", got.TOTPLastCounter)
	}
}

func TestDeleteSettingIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Settings().DeleteSetting(ctx, "never"); err != nil {
		t.Fatal(err)
	}
	_ = st.Settings().SetSetting(ctx, "k", "v")
	_ = st.Settings().DeleteSetting(ctx, "k")
	if _, err := st.Settings().GetSetting(ctx, "k"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDirectoryStatuses(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mk := func(sub, status string, rev int64) *store.User {
		u := &store.User{ID: "usr_" + sub, Username: sub, Role: "user", Status: status, SSOProvider: "kyidentity", SSOSubject: sub}
		if _, err := st.Users().CreateDirectoryUser(ctx, u, store.DirectoryEvent{ID: "c-" + sub, Revision: rev}); err != nil {
			t.Fatal(err)
		}
		return u
	}
	mk("on", "active", 1)
	mk("off", "inactive", 1)
	gone := mk("gone", "active", 1)
	if _, err := st.Users().DeleteDirectoryUser(ctx, gone, store.DirectoryEvent{ID: "d-gone", Revision: 2}); err != nil {
		t.Fatal(err)
	}
	// A suite sign-in with no webhook yet: a user row and no order row.
	if err := st.Users().CreateUser(ctx, &store.User{ID: "usr_oidc", Username: "oidc", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "oidc"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().CreateUser(ctx, &store.User{ID: "usr_local", Username: "local", Role: "user", Status: "active", SSOProvider: "local"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Users().DirectoryStatuses(ctx, "kyidentity")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"on": "active", "off": "inactive", "gone": "deleted", "oidc": "active"}
	if !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Nothing makes a subject unique, so two rows for one subject must fail closed whichever
// order the database returns them in.
func TestDirectoryStatusesDuplicateSubjectFailsClosed(t *testing.T) {
	ctx := context.Background()
	for _, order := range [][2]string{{"active", "inactive"}, {"inactive", "active"}} {
		st := newTestStore(t)
		for i, status := range order {
			id := "usr_dup" + strconv.Itoa(i)
			if err := st.Users().CreateUser(ctx, &store.User{ID: id, Username: id, Role: "user", Status: status, SSOProvider: "kyidentity", SSOSubject: "dup"}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := st.Users().DirectoryStatuses(ctx, "kyidentity")
		if err != nil {
			t.Fatal(err)
		}
		if got["dup"] != "inactive" {
			t.Errorf("rows %v: dup = %q, want inactive", order, got["dup"])
		}
	}
}
