package store

import (
	"context"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

// A row that cannot be read is an error, never a silently missing setting.
func TestGetAllSettingsReturnsScanErrors(t *testing.T) {
	ctx := context.Background()
	opened, err := Open(ctx, testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	s := opened.(*SQLStore)
	// The shipped column is NOT NULL; a nullable copy lets a NULL reach Scan into a string.
	for _, q := range []string{
		`DROP TABLE server_settings`,
		`CREATE TABLE server_settings (key VARCHAR(128) PRIMARY KEY, value TEXT, updated_at TIMESTAMP NOT NULL)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for _, kv := range [][2]any{{"good", "v"}, {"bad", nil}} {
		if _, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO server_settings (key, value, updated_at) VALUES (?, ?, ?)`), kv[0], kv[1], now); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.Settings().GetAllSettings(ctx); err == nil {
		t.Fatalf("unreadable row dropped silently: %v", got)
	}
}
