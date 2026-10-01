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
