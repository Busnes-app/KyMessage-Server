package main

import (
	"context"
	"log"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Opening the store prunes once before serving. Sweep idle rooms too, without
// depending on a member returning to trigger the authenticated read-time check.
func messagingMaintenanceLoop(ctx context.Context, st store.Store, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := st.Messaging().ExpireMessages(run)
			pairErr := st.Devices().CleanExpiredPairings(run)
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Printf("[MESSAGING] ciphertext expiry sweep failed: %v", err)
			}
			if pairErr != nil && ctx.Err() == nil {
				log.Printf("[DEVICES] expired pairing sweep failed: %v", pairErr)
			}
		}
	}
}
