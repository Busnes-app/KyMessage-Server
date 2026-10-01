package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// A sweep deletes expired pairings and leaves live ones.
func TestSweepPairingsDropsOnlyExpired(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	for secret, expires := range map[string]time.Time{"old": now.Add(-time.Minute), "live": now.Add(time.Minute)} {
		if err := st.Devices().CreatePairing(ctx, &store.DevicePairing{Secret: secret, Status: "pending", CreatedAt: now, ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
	}
	sweepPairings(ctx, st)
	if _, err := st.Devices().GetPairingBySecret(ctx, "old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expired pairing survived", err)
	}
	if _, err := st.Devices().GetPairingBySecret(ctx, "live"); err != nil {
		t.Fatal("live pairing swept", err)
	}
}

// Shutdown waits on done before the store closes, so the loop must close it on cancel.
func TestMaintenanceLoopClosesDoneOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go maintenanceLoop(ctx, nil, done)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenanceLoop did not close done after its context was cancelled")
	}
}
