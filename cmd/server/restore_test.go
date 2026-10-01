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
	"github.com/Busnes-app/ky_server_base/internal/backup"
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
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO messaging_identities (user_id) VALUES ('alice')`,
		`INSERT INTO messaging_devices (id,user_id,name,public_key,status,challenge,enrollment_session,expires_at,token_hash,created_at,verified_at) VALUES ('old-device','alice','old','synthetic-public-key','approved','','',1,'old-device-token',1,1)`,
		`INSERT INTO messaging_rooms (id,name,owner_id,created_at,epoch,sequence,retained_bytes) VALUES ('old-room','old','alice',1,1,1,8)`,
		`INSERT INTO messaging_members (room_id,user_id,status,generation) VALUES ('old-room','alice','active',1)`,
		`INSERT INTO messaging_events (room_id,sequence,device_id,event_id,kind,epoch,roster_hash,payload,request_hash,created_at) VALUES ('old-room',1,'old-device','old-event','commit',1,'synthetic','b2xk','synthetic',1)`,
		`INSERT INTO messaging_welcomes (room_id,sequence,device_id,payload) VALUES ('old-room',1,'old-device','b2xk')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
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
	for _, query := range []string{
		`SELECT COUNT(*) FROM messaging_devices WHERE status <> 'revoked' OR token_hash IS NOT NULL`,
		`SELECT COUNT(*) FROM messaging_members WHERE status <> 'removed'`,
		`SELECT COUNT(*) FROM messaging_rooms WHERE owner_identity_generation <> 0 OR retained_bytes <> 0`,
		`SELECT COUNT(*) FROM messaging_events WHERE payload <> ''`,
		`SELECT COUNT(*) FROM messaging_welcomes`,
	} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("restored stale state: %s = %d", query, count)
		}
	}
	if !strings.Contains(out.String(), "busnes_app") {
		t.Fatalf("manifest not printed: %s", out.String())
	}
	// A pre-split capsule leaves messaging rows, which restore-messages refuses.
	if strings.Contains(out.String(), "run `restore-messages") || !strings.Contains(out.String(), "restore-messages cannot run on this target") {
		t.Fatalf("old capsule got the wrong restore-messages hint:\n%s", out.String())
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

// messagesFixture seals a people capsule and a messages capsule from one live instance to one
// throwaway kit. carol is deleted between the two, as if the messages capsule were older.
func messagesFixture(t *testing.T) (people, messages string, shares []string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Server.AppName = "busnes_app"
	cfg.Database = config.DatabaseConfig{Driver: "sqlite", DataDir: dir, DSN: filepath.Join(dir, "ky_server.db")}
	cfg.Security.EncryptionKey = bytes.Repeat([]byte{1}, 32)
	st, err := store.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db, err := sql.Open("sqlite", cfg.Database.DSN+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, u := range []string{"alice", "bob", "carol"} {
		exec(`INSERT INTO users (id,username,created_at,updated_at) VALUES ('` + u + `','` + u + `',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
		exec(`INSERT INTO messaging_identities (user_id) VALUES ('` + u + `')`)
		exec(`INSERT INTO messaging_devices (id,user_id,name,public_key,status,challenge,enrollment_session,expires_at,token_hash,created_at,verified_at) VALUES ('` + u + `-dev','` + u + `','d','pk-` + u + `','approved','','',1,'tok-` + u + `',1,1)`)
	}
	exec(`INSERT INTO messaging_rooms (id,name,owner_id,created_at,epoch,sequence,retained_bytes,retention_days) VALUES ('room','r','alice',1,1,2,8,0)`)
	for _, u := range []string{"alice", "bob", "carol"} {
		exec(`INSERT INTO messaging_members (room_id,user_id,status,generation) VALUES ('room','` + u + `','active',1)`)
	}
	exec(`INSERT INTO messaging_events (room_id,sequence,device_id,event_id,kind,epoch,roster_hash,payload,request_hash,created_at) VALUES ('room',1,'alice-dev','e1','commit',1,'h','b2xk','h',1)`)
	exec(`INSERT INTO messaging_events (room_id,sequence,device_id,event_id,kind,epoch,roster_hash,payload,request_hash,created_at) VALUES ('room',2,'alice-dev','e2','application',1,'h','b2xk','h',1)`)
	exec(`INSERT INTO messaging_welcomes (room_id,sequence,device_id,payload) VALUES ('room',1,'bob-dev','w')`)

	key, shares := testKit(t)
	payload, err := backup.CollectMessages(ctx, cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	messages = sealTo(t, key, payload)
	exec(`DELETE FROM users WHERE id = 'carol'`)
	if payload, err = backup.Collect(ctx, cfg, "test"); err != nil {
		t.Fatal(err)
	}
	return sealTo(t, key, payload), messages, shares
}

func TestRestoreMessagesAfterPeople(t *testing.T) {
	people, messages, shares := messagesFixture(t)
	target := filepath.Join(t.TempDir(), "restored with spaces?#")
	var restored bytes.Buffer
	if err := restore(people, target, "busnes_app", shares, &restored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(restored.String(), "run `restore-messages -capsule <file> -into "+target+"`") {
		t.Fatalf("people restore lacks the restore-messages hint:\n%s", restored.String())
	}
	// The people capsule is not a messages capsule.
	if err := restoreMessages(context.Background(), people, target, "busnes_app", shares, &bytes.Buffer{}); err == nil {
		t.Fatal("restore-messages accepted a people capsule")
	}
	var out bytes.Buffer
	if err := restoreMessages(context.Background(), messages, target, "busnes_app", shares, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"busnes_app", "rooms=1", "dropped_rooms=0", "dropped_members=1", "dropped_devices=1", "events=2", "suspended"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "messages-") {
			t.Errorf("opened capsule left behind: %s", e.Name())
		}
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.Join(target, "data", "ky_server.db"), RawQuery: "mode=ro"}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var suspended, events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messaging_devices WHERE status = 'approved' AND token_hash IS NULL`).Scan(&suspended); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM messaging_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if suspended != 2 || events != 2 {
		t.Fatalf("suspended=%d events=%d", suspended, events)
	}

	dbFile := filepath.Join(target, "data", "ky_server.db")
	before, err := os.ReadFile(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreMessages(context.Background(), messages, target, "busnes_app", shares, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "already has messaging data") {
		t.Fatalf("second run: %v", err)
	}
	if after, err := os.ReadFile(dbFile); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("second run changed the database (%v)", err)
	}
}

func TestRestoreMessagesNeedsAPeopleRestore(t *testing.T) {
	_, messages, shares := messagesFixture(t)
	err := restoreMessages(context.Background(), messages, t.TempDir(), "busnes_app", shares, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "people restore") {
		t.Fatalf("got %v", err)
	}
}

func TestRestoreRefusesAMessagesCapsule(t *testing.T) {
	_, messages, shares := messagesFixture(t)
	target := filepath.Join(t.TempDir(), "restored")
	err := restore(messages, target, "busnes_app", shares, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "this is a messages capsule") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, backup.MessagesDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("decrypted messages left behind: %v", err)
	}
}
