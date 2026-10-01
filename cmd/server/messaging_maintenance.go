package main

import (
	"context"
	"log"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Opening the store prunes once before serving. Sweep idle rooms too, without
// depending on a member returning to trigger the authenticated read-time check, and revoke
// suspended devices not resumed within 30 days.
func messagingMaintenanceLoop(ctx context.Context, st store.Store, wake func(), done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			maintainMessaging(ctx, st, wake)
		}
	}
}

func maintainMessaging(ctx context.Context, st store.Store, wake func()) {
	run, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := st.Messaging().ExpireMessages(run)
	pairErr := st.Devices().CleanExpiredPairings(run)
	revoked, deviceErr := st.Messaging().ExpireSuspendedDevices(run)
	if revoked > 0 {
		wake() // other members' streams pick up the smaller roster
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		log.Printf("[MESSAGING] ciphertext expiry sweep failed: %v", err)
	}
	if pairErr != nil {
		log.Printf("[DEVICES] expired pairing sweep failed: %v", pairErr)
	}
	if deviceErr != nil {
		log.Printf("[MESSAGING] suspended device expiry sweep failed after %d revocations: %v", revoked, deviceErr)
	}
}
