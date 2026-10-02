package main

import (
	"context"
	"log"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

// brandTimeout bounds one Element brand reconcile: a small local file.
const brandTimeout = 10 * time.Second

// maintenanceLoop reconciles Element's brand at its start, then once a minute deletes expired
// QR pairings and reconciles the brand again. brand is api.Server.ReconcileBrand (a no-op
// without Matrix, logging its own failures once per streak); nil skips it. done closes between
// ticks, so shutdown can wait for it before the store closes.
func maintenanceLoop(ctx context.Context, st store.Store, brand func(context.Context) error, done chan<- struct{}) {
	defer close(done)
	reconcileBrand(ctx, brand)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepPairings(ctx, st)
			reconcileBrand(ctx, brand)
		}
	}
}

func reconcileBrand(ctx context.Context, brand func(context.Context) error) {
	if brand == nil || ctx.Err() != nil {
		return
	}
	run, cancel := context.WithTimeout(ctx, brandTimeout)
	defer cancel()
	_ = brand(run) // the reconciler logs; the next tick retries
}

func sweepPairings(ctx context.Context, st store.Store) {
	run, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := st.Devices().CleanExpiredPairings(run); err != nil && ctx.Err() == nil {
		log.Printf("[DEVICES] expired pairing sweep failed: %v", err)
	}
}
