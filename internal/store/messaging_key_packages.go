package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The delivery service stores opaque MLS KeyPackages. The receiving MLS client
// validates their credential, signature key, lifetime and signatures before use.
type MessagingKeyPackage struct {
	ID, DeviceID, Payload, RoomID string
	ExpiresAt                     int64
}

func (m *messagingStore) PublishKeyPackage(ctx context.Context, actor MessagingActor, kp MessagingKeyPackage) error {
	return m.transaction(ctx, actor, true, func(tx *sql.Tx, device string) error {
		if kp.RoomID != "" {
			if _, _, _, err := m.deliveryState(ctx, tx, actor, kp.RoomID); err != nil {
				return err
			}
		}
		var owner, payload, claim, publicationRoom string
		var expires int64
		err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT device_id, payload, expires_at, claim_id, publication_room FROM messaging_key_packages WHERE id = ?`), kp.ID).Scan(&owner, &payload, &expires, &claim, &publicationRoom)
		if err == nil {
			if owner != device || publicationRoom != kp.RoomID || payload != kp.Payload || expires != kp.ExpiresAt || claim != "" {
				return ErrMessagingConflict
			}
			return nil // A publication retry must never put a claimed package back.
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var total, available int
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN claim_id = '' AND expires_at > ? THEN 1 ELSE 0 END), 0) FROM messaging_key_packages WHERE device_id = ?`), time.Now().Unix(), device).Scan(&total, &available); err != nil {
			return err
		}
		// ponytail: preserve expired/claimed hashes under a bounded lifetime quota.
		// A production archival policy must retain anti-republication tombstones.
		if total >= 128 || available >= 16 {
			return ErrMessagingLimit
		}
		_, err = tx.ExecContext(ctx, m.store.rebind(`INSERT INTO messaging_key_packages (id, device_id, payload, expires_at, publication_room) VALUES (?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`), kp.ID, device, kp.Payload, kp.ExpiresAt, kp.RoomID)
		if err != nil {
			return err
		}
		// Another account may concurrently publish the same digest. Check ownership
		// before returning success; its payload cannot become this device's package.
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT device_id FROM messaging_key_packages WHERE id = ?`), kp.ID).Scan(&owner); err != nil {
			return err
		}
		if owner != device {
			return ErrMessagingConflict
		}
		return m.audit(ctx, tx, actor, "messaging.key_package_published", kp.ID, "device_id="+device)
	})
}

func (m *messagingStore) ClaimKeyPackage(ctx context.Context, actor MessagingActor, room, target, requestID string) (MessagingKeyPackage, error) {
	var kp MessagingKeyPackage
	err := m.transaction(ctx, actor, true, func(tx *sql.Tx, device string) error {
		state, owner, _, err := m.deliveryState(ctx, tx, actor, room)
		if err != nil {
			return err
		}
		old, err := m.epochDevices(ctx, tx, room)
		if err != nil {
			return err
		}
		generation, eligible := rosterGeneration(state, device)
		previous, included := old[device]
		included = included && previous.generation == generation
		if !eligible || (!included && !(state.Epoch == 0 && owner == actor.UserID)) {
			return ErrMessagingDenied
		}
		targetGeneration, eligible := rosterGeneration(state, target)
		if !eligible || target == device {
			return ErrNotFound
		}
		var claimedRoom string
		var claimedGeneration int64
		err = tx.QueryRowContext(ctx, m.store.rebind(`SELECT id, device_id, payload, expires_at, claim_room, member_generation, publication_room FROM messaging_key_packages WHERE claimant_device = ? AND claim_id = ?`), device, requestID).Scan(&kp.ID, &kp.DeviceID, &kp.Payload, &kp.ExpiresAt, &claimedRoom, &claimedGeneration, &kp.RoomID)
		if err == nil {
			if kp.DeviceID != target || claimedRoom != room || claimedGeneration != targetGeneration || kp.ExpiresAt <= time.Now().Unix() {
				return ErrMessagingConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if prior, exists := old[target]; exists && prior.generation == targetGeneration {
			return ErrMessagingConflict
		}
		query := `SELECT id, device_id, payload, expires_at, publication_room FROM messaging_key_packages WHERE device_id = ? AND claim_id = '' AND expires_at > ? AND (publication_room = ? OR publication_room = '') ORDER BY CASE WHEN publication_room = '' THEN 1 ELSE 0 END, expires_at, id LIMIT 1`
		if m.store.driver == "postgres" {
			query += " FOR UPDATE SKIP LOCKED"
		}
		err = tx.QueryRowContext(ctx, m.store.rebind(query), target, time.Now().Unix(), room).Scan(&kp.ID, &kp.DeviceID, &kp.Payload, &kp.ExpiresAt, &kp.RoomID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_key_packages SET claim_room = ?, claimant_device = ?, claim_id = ?, member_generation = ? WHERE id = ? AND claim_id = ''`), room, device, requestID, targetGeneration, kp.ID)); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.key_package_claimed", kp.ID, "room_id="+room+" device_id="+device)
	})
	return kp, err
}
