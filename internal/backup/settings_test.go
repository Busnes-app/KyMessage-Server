package backup_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// sqliteInstance is a fresh SQLite store in a temp data dir, the way every backup adapter
// test needs one.
func sqliteInstance(t *testing.T) (*config.Config, store.Store) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Server.AppName = "busnes_app"
	cfg.Server.Port = 8080
	cfg.Database.Driver = "sqlite"
	cfg.Database.DataDir = dir
	cfg.Database.DSN = filepath.Join(dir, "ky_server.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	cfg.Security.EncryptionKey = bytes.Repeat([]byte{1}, 32)
	st, err := store.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return cfg, st
}

func TestSettingsAdapterMapsNotFound(t *testing.T) {
	_, st := sqliteInstance(t)
	s := backup.Settings(context.Background(), st.Settings())
	if _, err := s.Get("nope"); !errors.Is(err, recoveryclient.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	_ = s.Set("a", "1")
	if v, _ := s.Get("a"); v != "1" {
		t.Fatal("set/get")
	}
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("a"); !errors.Is(err, recoveryclient.ErrNotFound) {
		t.Fatal("delete")
	}
}

func TestSealerRoundTripUnderDeploymentKey(t *testing.T) {
	cfg := &config.Config{}
	cfg.Security.EncryptionKey = bytes.Repeat([]byte{1}, 32)
	s, err := backup.NewSealer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.Seal([]byte("tok"))
	p, err := s.Open(c)
	if err != nil || string(p) != "tok" {
		t.Fatal(err)
	}
}

// Fixture generated with v0.5.0 StoreRecoveryKey/StorePairing, a synthetic token,
// and a deployment key of 32 bytes of 0x01. No recovery private key is retained.
func TestV050PairingLoadsUnchanged(t *testing.T) {
	cfg, st := sqliteInstance(t)
	raw, err := os.ReadFile("testdata/pairing-v050.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Version   string
		Settings  map[string]string
		PublicKey []byte
		KeyID     string
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	settings := backup.Settings(context.Background(), st.Settings())
	for key, value := range fixture.Settings {
		if err := settings.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	path := recoveryclient.RecoveryKeyPath(cfg.Database.DataDir)
	if err := os.WriteFile(path, fixture.PublicKey, 0600); err != nil {
		t.Fatal(err)
	}
	sealer, err := backup.NewSealer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pairing, err := recoveryclient.LoadPairing(cfg.Database.DataDir, settings, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if pairing.URL != "https://recovery.example.test" || pairing.Token != "synthetic-v050-token" || pairing.Key.Public.ID() != fixture.KeyID || pairing.Key.Threshold != 2 || pairing.Key.TotalShares != 3 {
		t.Fatal("pairing identity changed")
	}
	after := map[string]string{}
	for key := range fixture.Settings {
		after[key], err = settings.Get(key)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !maps.Equal(after, fixture.Settings) {
		t.Fatal("loading rewrote pairing settings")
	}
	pub, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(pub, fixture.PublicKey) {
		t.Fatal("loading rewrote public key")
	}
}

func TestMessagesSettingsKeepOwnScheduleAndReceipt(t *testing.T) {
	ctx := context.Background()
	_, st := sqliteInstance(t)
	people := backup.Settings(ctx, st.Settings())
	messages := backup.MessagesSettings(ctx, st.Settings())
	if err := recoveryclient.SetInterval(people, 3600); err != nil {
		t.Fatal(err)
	}
	if err := recoveryclient.SetInterval(messages, 7200); err != nil {
		t.Fatal(err)
	}
	p, _ := recoveryclient.Interval(0, people)
	m, _ := recoveryclient.Interval(0, messages)
	if p != time.Hour || m != 2*time.Hour {
		t.Fatalf("schedules shared: people %v messages %v", p, m)
	}
	// Pairing and key pin are shared: a key pinned through one is visible through the other.
	if err := people.Set("kyrecovery_key_id", "k1"); err != nil {
		t.Fatal(err)
	}
	if v, err := messages.Get("kyrecovery_key_id"); err != nil || v != "k1" {
		t.Fatalf("key pin not shared: %q %v", v, err)
	}
	if v, err := messages.Get("backup_interval_sec"); err != nil || v != "7200" {
		t.Fatalf("messages interval: %q %v", v, err)
	}
	if v, _ := st.Settings().GetSetting(ctx, "messages_backup_interval_sec"); v != "7200" {
		t.Fatalf("stored key: %q", v)
	}
	if err := messages.Set("kyrecovery_last_deposit", "r"); err != nil {
		t.Fatal(err)
	}
	if _, err := people.Get("kyrecovery_last_deposit"); !errors.Is(err, recoveryclient.ErrNotFound) {
		t.Fatalf("receipt shared: %v", err)
	}
}

func TestMessagesLocalCopiesDoNotPrunePeople(t *testing.T) {
	ctx := context.Background()
	cfg, st := seedMessagingFixture(t)
	cfg.Backup.Dir = t.TempDir()
	cfg.Backup.Keep = 1
	people := backup.Settings(ctx, st.Settings())
	key, err := recoverykey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := recoveryclient.StoreRecoveryKey(cfg.Database.DataDir, people, recoveryclient.RecoveryKey{Public: key.Public(), Threshold: 2, TotalShares: 3}); err != nil {
		t.Fatal(err)
	}
	prc, err := backup.RunConfig(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	mrc, err := backup.MessagesRunConfig(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recoveryclient.Run(ctx, prc, people, func() (recoveryclient.Payload, error) { return backup.Collect(ctx, cfg, "test") }, nil); err != nil {
		t.Fatal(err)
	}
	messages := backup.MessagesSettings(ctx, st.Settings())
	for range 2 {
		if _, err := recoveryclient.Run(ctx, mrc, messages, func() (recoveryclient.Payload, error) { return backup.CollectMessages(ctx, cfg, "test") }, nil); err != nil {
			t.Fatal(err)
		}
	}
	app := cfg.Server.AppName
	if c, err := recoveryclient.ListLocalCopies(cfg.Backup.Dir, app); err != nil || len(c) != 1 {
		t.Fatalf("people copies: %d %v", len(c), err)
	}
	if c, err := recoveryclient.ListLocalCopies(filepath.Join(cfg.Backup.Dir, "messages"), app); err != nil || len(c) != 1 {
		t.Fatalf("messages copies: %d %v", len(c), err)
	}
}
