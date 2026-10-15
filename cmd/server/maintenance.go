package main

import (
	"context"
	"log"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

// brandTimeout bounds one Element brand reconcile: a small local file.
const brandTimeout = 10 * time.Second

// maintenanceLoop reconciles Element's brand and Synapse's federation file at its start, then
// once a minute deletes expired QR pairings and reconciles them again. brand runs
// api.Server.ReconcileBrand and ReconcileFederation (no-ops without Matrix, logging their own
// failures once per streak); nil skips it. done closes between
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

// callReconcileTimeout bounds one call reconcile: every room's LiveKit and Synapse reads under
// one console session. Above the 5 s tick so many rooms finish; a slow run delays the next.
const callReconcileTimeout = 30 * time.Second

// callMaintenanceLoop is drained alongside branding and pairings before the store closes.
func callMaintenanceLoop(ctx context.Context, reconcile func(context.Context) error, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		run, cancel := context.WithTimeout(ctx, callReconcileTimeout)
		err := reconcile(run)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("[MATRIX RTC] call reconciliation failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// agentExpiryLoop revokes agent tokens unused for 90 days (api.Server.ExpireIdleAgents) at its
// start and hourly. done closes between runs, so shutdown can wait before the store closes.
func agentExpiryLoop(ctx context.Context, expire func(context.Context, time.Time) error, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		run, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := expire(run, time.Now())
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("[AGENTS] idle token expiry incomplete (retrying hourly): %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
