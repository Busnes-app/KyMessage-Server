package backup_test

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// payloadConfig is a real SQLite store in a temp data dir: the collectors snapshot the live
// database, so there has to be one.
func payloadConfig(t *testing.T) (*config.Config, []byte) {
	t.Helper()
	cfg, _ := sqliteInstance(t)
	return cfg, cfg.Security.EncryptionKey
}

// The encryption key must ride in the capsule: users.totp_secret_enc is AES-GCM under it, so
// a restore without it hands the operator a database whose MFA secrets are gone for good.
func TestCollectCarriesTheEncryptionKey(t *testing.T) {
	cfg, key := payloadConfig(t)
	payload, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range payload.Files {
		if f.Path != "data/encryption.key" {
			continue
		}
		found = true
		if want := hex.EncodeToString(key) + "\n"; string(f.Data) != want {
			t.Errorf("content: got %q, want the lowercase hex keyfile reads", f.Data)
		}
		if f.Mode != 0600 {
			t.Errorf("mode: got %o, want 600", f.Mode)
		}
	}
	if !found {
		t.Fatal("payload has no data/encryption.key")
	}
	req, _ := payload.VerificationRecipe["required_files"].([]string)
	if !slices.Contains(req, "data/encryption.key") {
		t.Errorf("required_files: got %v, want data/encryption.key among them", req)
	}
}

// A restore must come back paired, not half-paired: recovery.pub is public and the capsule is
// sealed to that very key, so it rides along whenever the instance has one.
func TestCollectCarriesTheRecoveryPublicKeyWhenPaired(t *testing.T) {
	cfg, _ := payloadConfig(t)
	ctx := context.Background()

	unpaired, err := backup.Collect(ctx, cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if f := findFile(unpaired.Files, "data/recovery.pub"); f != nil {
		t.Error("unpaired instance shipped a recovery.pub")
	}
	if req, _ := unpaired.VerificationRecipe["required_files"].([]string); slices.Contains(req, "data/recovery.pub") {
		t.Errorf("unpaired required_files: got %v", req)
	}

	pubPath := recoveryclient.RecoveryKeyPath(cfg.Database.DataDir)
	if err := os.MkdirAll(filepath.Dir(pubPath), 0700); err != nil {
		t.Fatal(err)
	}
	pub := []byte("a-recovery-public-key")
	if err := os.WriteFile(pubPath, pub, 0600); err != nil {
		t.Fatal(err)
	}
	paired, err := backup.Collect(ctx, cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	f := findFile(paired.Files, "data/recovery.pub")
	if f == nil {
		t.Fatal("paired instance has no data/recovery.pub in the payload")
	}
	if string(f.Data) != string(pub) {
		t.Error("data/recovery.pub is not the pinned public key byte for byte")
	}
	if f.Mode != 0600 {
		t.Errorf("mode: got %o, want 600", f.Mode)
	}
	if req, _ := paired.VerificationRecipe["required_files"].([]string); !slices.Contains(req, "data/recovery.pub") {
		t.Errorf("required_files: got %v, want data/recovery.pub among them", req)
	}
}

func findFile(files []recoveryclient.File, path string) *recoveryclient.File {
	for i := range files {
		if files[i].Path == path {
			return &files[i]
		}
	}
	return nil
}

func TestCollectRefusesAShortKey(t *testing.T) {
	cfg, _ := payloadConfig(t)
	cfg.Security.EncryptionKey = cfg.Security.EncryptionKey[:16]
	if _, err := backup.Collect(context.Background(), cfg, "1.0.0"); err == nil {
		t.Fatal("a 16-byte encryption key was accepted")
	}
}

// The store runs in WAL mode, so a plain read of the main file misses every commit still in
// the -wal. The snapshot must carry a row committed moments ago and never checkpointed.
func TestSnapshotSeesUncheckpointedCommit(t *testing.T) {
	cfg, st := sqliteInstance(t)
	ctx := context.Background()
	if err := st.Settings().SetSetting(ctx, "canary", "still-in-the-wal"); err != nil {
		t.Fatal(err)
	}
	payload, err := backup.Collect(ctx, cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	f := findFile(payload.Files, "data/ky_server.db")
	if f == nil {
		t.Fatal("no data/ky_server.db in the payload")
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(restored, f.Data, 0600); err != nil {
		t.Fatal(err)
	}
	copyStore, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: restored})
	if err != nil {
		t.Fatalf("snapshot does not open: %v", err)
	}
	defer copyStore.Close()
	if got, err := copyStore.Settings().GetSetting(ctx, "canary"); err != nil || got != "still-in-the-wal" {
		t.Fatalf("snapshot lacks the uncheckpointed row: %q, %v", got, err)
	}
	if check, _ := payload.VerificationRecipe["check_sqlite_integrity"].(bool); !check {
		t.Error("recipe does not ask the drill to check the database")
	}
}

// A driver the collectors cannot snapshot must refuse, not seal a keys-and-config capsule
// that a receipt would then call a backup.
func TestCollectRefusesADriverItCannotSnapshot(t *testing.T) {
	cfg, _ := payloadConfig(t)
	cfg.Database.Driver = "postgres"
	if _, err := backup.Collect(context.Background(), cfg, "1.0.0"); !errors.Is(err, backup.ErrNoDatabaseSnapshot) {
		t.Fatalf("got %v, want ErrNoDatabaseSnapshot", err)
	}
}

// Exercise collection against realistic aggregate delivery size, not an HTTP abuse
// scenario: three rooms each below their limit, with individually bounded envelopes.
func TestSnapshotOmitsDeliveryPayloadsWithoutChangingLiveData(t *testing.T) {
	cfg, st := sqliteInstance(t)
	ctx := context.Background()
	if err := st.Settings().SetSetting(ctx, "canary", "keep-metadata"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO users (id,username,created_at,updated_at) VALUES ('owner','owner',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)")
	const count = 300
	payload := strings.Repeat("A", 80*1024) // 60 KiB decoded, below the 64 KiB envelope cap.
	for room := range 3 {
		id := fmt.Sprintf("room-%d", room)
		exec("INSERT INTO messaging_rooms (id,name,owner_id,created_at,epoch,sequence,retained_bytes) VALUES (?,?,'owner',1,1,?,?)", id, id, count, count*len(payload))
		for sequence := 1; sequence <= count; sequence++ {
			exec("INSERT INTO messaging_events (room_id,sequence,device_id,event_id,kind,epoch,roster_hash,payload,request_hash,created_at,expires_at) VALUES (?,?,'device',?,'application',1,'roster',?,'retry-hash',1,4102444800)", id, sequence, fmt.Sprint(sequence), payload)
		}
	}
	exec("INSERT INTO messaging_welcomes (room_id,sequence,device_id,payload) VALUES ('room-0',1,'device','welcome-canary')")
	exec("INSERT INTO messaging_key_packages (id,device_id,payload,expires_at) VALUES ('package','device','package-canary',4102444800)")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	collected, err := backup.Collect(ctx, cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	f := findFile(collected.Files, "data/ky_server.db")
	if f == nil {
		t.Fatal("missing database")
	}
	if int64(len(f.Data)) > recoveryclient.MaxCapsuleFileBytes {
		t.Fatalf("snapshot %d exceeds capsule file limit %d", len(f.Data), recoveryclient.MaxCapsuleFileBytes)
	}
	key, err := recoverykey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := recoveryclient.Seal(collected, recoveryclient.RecoveryKey{Public: key.Public(), Threshold: 2, TotalShares: 3}); err != nil {
		t.Fatalf("projected snapshot cannot seal: %v", err)
	}

	restored := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(restored, f.Data, 0600); err != nil {
		t.Fatal(err)
	}
	copyDB, err := sql.Open("sqlite", restored)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	assertInt := func(db *sql.DB, query string, want int) {
		t.Helper()
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %d, want %d (%v)", query, got, want, err)
		}
	}
	assertInt(db, "SELECT SUM(LENGTH(payload)) FROM messaging_events", 3*count*len(payload))
	assertInt(db, "SELECT COUNT(*) FROM messaging_welcomes", 1)
	assertInt(db, fmt.Sprintf("SELECT COUNT(*) FROM messaging_rooms WHERE retained_bytes=%d AND retained_from=1 AND sequence=300", count*len(payload)), 3)
	assertInt(db, "SELECT COUNT(*) FROM messaging_key_packages WHERE payload='package-canary' AND expires_at=4102444800", 1)
	assertInt(copyDB, "SELECT COUNT(*) FROM messaging_events WHERE payload='' AND request_hash='retry-hash'", 3*count)
	assertInt(copyDB, "SELECT COUNT(*) FROM messaging_welcomes", 0)
	assertInt(copyDB, "SELECT COUNT(*) FROM messaging_key_packages WHERE id='package' AND payload='' AND expires_at=1", 1)
	assertInt(copyDB, "SELECT COUNT(*) FROM messaging_rooms WHERE retained_bytes=0 AND retained_from=301 AND sequence=300", 3)
	var canary string
	if err := copyDB.QueryRow("SELECT value FROM server_settings WHERE key='canary'").Scan(&canary); err != nil || canary != "keep-metadata" {
		t.Fatalf("metadata: %q %v", canary, err)
	}
	if err := copyDB.QueryRow("PRAGMA integrity_check").Scan(&canary); err != nil || canary != "ok" {
		t.Fatalf("integrity: %q %v", canary, err)
	}
	rows, err := copyDB.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("snapshot foreign keys failed")
	}
}
