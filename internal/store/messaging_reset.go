package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type MessagingResetReceipt struct {
	DeviceID           string `json:"device_id"`
	IdentityGeneration int64  `json:"identity_generation"`
	CompletedAt        int64  `json:"completed_at"`
}

func (m *messagingStore) resetReceipt(ctx context.Context, tx *sql.Tx, actor MessagingActor, stateHash string) (MessagingResetReceipt, error) {
	var receipt MessagingResetReceipt
	err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT device_id, identity_generation, completed_at FROM messaging_reset_receipts WHERE state_hash = ? AND user_id = ? AND session_hash = ?`), stateHash, actor.UserID, actor.SessionHash).Scan(&receipt.DeviceID, &receipt.IdentityGeneration, &receipt.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return receipt, err
}

func (m *messagingStore) ResetReceipt(ctx context.Context, actor MessagingActor, stateHash string) (MessagingResetReceipt, error) {
	var receipt MessagingResetReceipt
	err := m.transaction(ctx, actor, false, func(tx *sql.Tx, _ string) error {
		var err error
		receipt, err = m.resetReceipt(ctx, tx, actor, stateHash)
		return err
	})
	if err != nil {
		return MessagingResetReceipt{}, err
	}
	return receipt, nil
}

// ResetIdentity consumes confirmed, freshly authenticated recovery state and
// changes the account's messaging identity in the same transaction. It does not
// restore message keys or inherit old room ownership/membership.
func (m *messagingStore) ResetIdentity(ctx context.Context, actor MessagingActor, stateHash, verifiedSubject string) (MessagingResetReceipt, error) {
	var receipt MessagingResetReceipt
	err := m.transaction(ctx, actor, false, func(tx *sql.Tx, _ string) error {
		if saved, err := m.resetReceipt(ctx, tx, actor, stateHash); err == nil {
			receipt = saved
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		r, err := m.recoveryAuthentication(ctx, tx, actor, stateHash)
		if err != nil {
			return err
		}
		if !r.ResetConfirmed || verifiedSubject != r.Subject || r.IdentityGeneration >= 9007199254740991 {
			return ErrMessagingDenied
		}
		// Lock affected rooms in a deterministic order before altering their ACLs.
		rows, err := tx.QueryContext(ctx, m.store.rebind(`SELECT room_id FROM messaging_members WHERE user_id = ? AND status <> 'removed' ORDER BY room_id`), actor.UserID)
		if err != nil {
			return err
		}
		var rooms []string
		for rows.Next() {
			var room string
			if err := rows.Scan(&room); err != nil {
				rows.Close()
				return err
			}
			rooms = append(rooms, room)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, room := range rooms {
			if err := m.lockDeliveryRoom(ctx, tx, room); err != nil {
				return err
			}
		}
		if err := messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_identities SET generation = generation + 1 WHERE user_id = ? AND generation = ?`), actor.UserID, r.IdentityGeneration)); err != nil {
			return err
		}
		now := time.Now().Unix()
		if _, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_key_packages SET expires_at = ? WHERE claim_id = '' AND device_id IN (SELECT id FROM messaging_devices WHERE user_id = ?)`), now, actor.UserID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_devices SET status = 'revoked', token_hash = NULL, challenge = '', enrollment_session = '' WHERE user_id = ? AND id <> ?`), actor.UserID, r.DeviceID); err != nil {
			return err
		}
		if err := messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_devices SET status = 'approved', approved_by = '', identity_generation = ? WHERE id = ? AND user_id = ? AND status = 'pending' AND public_key = ?`), r.IdentityGeneration+1, r.DeviceID, actor.UserID, r.PublicKey)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_members SET status = 'removed' WHERE user_id = ? AND status <> 'removed'`), actor.UserID); err != nil {
			return err
		}
		// Other in-flight requests were approved against the old identity and cannot survive.
		if _, err := tx.ExecContext(ctx, m.store.rebind(`DELETE FROM messaging_recovery_auth WHERE user_id = ?`), actor.UserID); err != nil {
			return err
		}
		receipt = MessagingResetReceipt{DeviceID: r.DeviceID, IdentityGeneration: r.IdentityGeneration + 1, CompletedAt: now}
		if _, err := tx.ExecContext(ctx, m.store.rebind(`INSERT INTO messaging_reset_receipts (state_hash,user_id,session_hash,device_id,identity_generation,completed_at) VALUES (?,?,?,?,?,?)`), stateHash, actor.UserID, actor.SessionHash, receipt.DeviceID, receipt.IdentityGeneration, now); err != nil {
			return err
		}
		key, err := base64.StdEncoding.DecodeString(r.PublicKey)
		if err != nil {
			return err
		}
		fingerprint := sha256.Sum256(key)
		return m.audit(ctx, tx, actor, "messaging.identity_reset", r.DeviceID, fmt.Sprintf("old_generation=%d new_generation=%d fingerprint=%s", r.IdentityGeneration, receipt.IdentityGeneration, hex.EncodeToString(fingerprint[:])))
	})
	if err != nil {
		return MessagingResetReceipt{}, err
	}
	return receipt, nil
}
