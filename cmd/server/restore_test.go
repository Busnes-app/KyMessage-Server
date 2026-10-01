package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// testKit is a throwaway 2-of-3 recovery key and two of its shares.
func testKit(t *testing.T) (recoveryclient.RecoveryKey, []string) {
	t.Helper()
	priv, err := recoverykey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	shares, err := recoverykey.Split(priv, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	// Non-consecutive indices: {1,2} would let an XOR-shaped bug pass.
	return recoveryclient.RecoveryKey{Public: priv.Public(), Threshold: 2, TotalShares: 3}, []string{shares[0].String(), shares[2].String()}
}

func sealTo(t *testing.T, key recoveryclient.RecoveryKey, payload recoveryclient.Payload) string {
	t.Helper()
	raw, _, err := recoveryclient.Seal(payload, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x.kycap")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func sealFixture(t *testing.T, service string) (string, []string) {
	t.Helper()
	key, shares := testKit(t)
	dbPath := filepath.Join(t.TempDir(), "source.db")
	st, err := store.Open(context.Background(), config.DatabaseConfig{Driver: "sqlite", DSN: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Users().CreateUser(context.Background(), &store.User{ID: "alice", Username: "alice", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Sessions().CreateSession(context.Background(), &store.Session{TokenHash: "old-session", UserID: "alice", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	payload := recoveryclient.Payload{ServiceName: service, AppVersion: "1.0.0",
		Files: []recoveryclient.File{{Path: "data/ky_server.db", Data: dbBytes, Mode: 0600}, {Path: "data/encryption.key", Data: []byte(strings.Repeat("01", 32)), Mode: 0600}}}
	return sealTo(t, key, payload), shares
}

func TestRestoreExtractsWithTwoShares(t *testing.T) {
	path, shares := sealFixture(t, "busnes_app")
	target := filepath.Join(t.TempDir(), "restored with spaces?#")
	var out bytes.Buffer
	if err := restore(path, target, "busnes_app", shares, &out); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.Join(target, "data", "ky_server.db"), RawQuery: "mode=ro"}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sessions, audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_records WHERE action = 'restore.grants_invalidated'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || audits != 1 {
		t.Fatalf("restored grants: sessions=%d audits=%d", sessions, audits)
	}
	if !strings.Contains(out.String(), "busnes_app") {
		t.Fatalf("manifest not printed: %s", out.String())
	}
}

func TestRestoreRefusesAnotherService(t *testing.T) {
	path, shares := sealFixture(t, "someone_else")
	err := restore(path, t.TempDir(), "busnes_app", shares, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "someone_else") {
		t.Fatalf("got %v, want a service-name refusal naming the capsule's service", err)
	}
}

func TestRestoreRefusesTheWrongKit(t *testing.T) {
	path, _ := sealFixture(t, "busnes_app")
	_, otherShares := sealFixture(t, "busnes_app")
	err := restore(path, t.TempDir(), "busnes_app", otherShares, &bytes.Buffer{})
	if !errors.Is(err, capsule.ErrWrongRecoveryKey) {
		t.Fatalf("got %v, want ErrWrongRecoveryKey", err)
	}
}

func TestRestoreRefusesOneShare(t *testing.T) {
	path, shares := sealFixture(t, "busnes_app")
	if err := restore(path, t.TempDir(), "busnes_app", shares[:1], &bytes.Buffer{}); err == nil {
		t.Fatal("one share of a 2-of-3 kit was accepted")
	}
}

// A restore that fails after extraction must not leave the decrypted payload behind.
func TestRestoreFailureRemovesExtractedFiles(t *testing.T) {
	key, shares := testKit(t)
	path := sealTo(t, key, recoveryclient.Payload{ServiceName: "busnes_app", AppVersion: "1.0.0",
		Files: []recoveryclient.File{{Path: "data/encryption.key", Data: []byte(strings.Repeat("01", 32)), Mode: 0600}}})
	absent := filepath.Join(t.TempDir(), "restored")
	if err := restore(path, absent, "busnes_app", shares, &bytes.Buffer{}); err == nil {
		t.Fatal("restore without a database succeeded")
	}
	if _, err := os.Lstat(absent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent target left behind: %v", err)
	}
	empty := t.TempDir()
	if err := restore(path, empty, "busnes_app", shares, &bytes.Buffer{}); err == nil {
		t.Fatal("restore without a database succeeded")
	}
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Fatalf("empty target not emptied: %v %v", entries, err)
	}
}

// noDatabaseCapsule extracts but fails preparation: it has no data/ky_server.db.
func noDatabaseCapsule(t *testing.T) (string, []string) {
	t.Helper()
	key, shares := testKit(t)
	return sealTo(t, key, recoveryclient.Payload{ServiceName: "busnes_app", AppVersion: "1.0.0",
		Files: []recoveryclient.File{{Path: "data/encryption.key", Data: []byte(strings.Repeat("01", 32)), Mode: 0600}}}), shares
}

// A target swapped for a symlink between extraction and cleanup must not redirect the cleanup.
func TestRestoreFailureIgnoresAReplacedTarget(t *testing.T) {
	path, shares := noDatabaseCapsule(t)
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := t.TempDir()
	sentinel := filepath.Join(victim, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "moved")
	afterExtract = func(string) {
		if err := os.Rename(target, moved); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(victim, target); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterExtract = func(string) {} })
	err := restore(path, target, "busnes_app", shares, &bytes.Buffer{})
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("cleanup followed the swapped target: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("got %v, want a replaced-target error", err)
	}
	if entries, err := os.ReadDir(moved); err != nil || len(entries) != 0 {
		t.Fatalf("extracted files left in the real target: %v %v", entries, err)
	}
}

func TestRestoreRefusesASymlinkTarget(t *testing.T) {
	path, shares := noDatabaseCapsule(t)
	real := t.TempDir()
	target := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, target); err != nil {
		t.Fatal(err)
	}
	err := restore(path, target, "busnes_app", shares, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("got %v, want a symlink refusal", err)
	}
	if entries, _ := os.ReadDir(real); len(entries) != 0 {
		t.Fatalf("extracted through the symlink: %v", entries)
	}
}

func TestRestoreChecksTheTargetParent(t *testing.T) {
	path, shares := sealFixture(t, "busnes_app")
	for _, mode := range []os.FileMode{0o770, 0o707} {
		parent := t.TempDir()
		if err := os.Chmod(parent, mode); err != nil {
			t.Fatal(err)
		}
		err := restore(path, filepath.Join(parent, "target"), "busnes_app", shares, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "sticky") {
			t.Fatalf("mode %v: got %v, want a writable-parent refusal", mode, err)
		}
	}
	sticky := t.TempDir()
	if err := os.Chmod(sticky, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if err := restore(path, filepath.Join(sticky, "target"), "busnes_app", shares, &bytes.Buffer{}); err != nil {
		t.Fatalf("sticky world-writable parent refused: %v", err)
	}
}

func TestRestoreParentRefusal(t *testing.T) {
	me := uint32(os.Getuid())
	other := me + 1
	if other == 0 {
		other = 2
	}
	cases := []struct {
		uid  uint32
		mode os.FileMode
		want string
	}{
		{me, os.ModeDir | 0o700, ""},
		{0, os.ModeDir | 0o755, ""},
		{0, os.ModeDir | os.ModeSticky | 0o777, ""},
		{me, os.ModeDir | os.ModeSticky | 0o777, ""},
		{me, os.ModeDir | 0o770, "sticky"},
		{other, os.ModeDir | 0o755, "owned by uid"},
		{other, os.ModeDir | os.ModeSticky | 0o777, "owned by uid"},
	}
	for _, c := range cases {
		err := parentRefusal("/p", c.uid, c.mode)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("uid %d mode %v: got %v, want %q", c.uid, c.mode, err, c.want)
		}
	}
}
