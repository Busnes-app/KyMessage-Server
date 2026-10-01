package store

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SuspendedDevice is the admin view of an imported device awaiting its owner's resume.
type SuspendedDevice struct {
	ID, UserID, Username, Name, PublicKey string
	CreatedAt, IdentityGeneration         int64
}

const suspendedDevice = `status = 'approved' AND token_hash IS NULL`

// StartDeviceResume stores a challenge bound to this session and the proposed credential.
// The credential is not usable until ResumeDevice verifies the device key's signature.
func (m *messagingStore) StartDeviceResume(ctx context.Context, actor MessagingActor, id, tokenHash, challenge string, expiresAt int64) error {
	return m.transaction(ctx, actor, false, func(tx *sql.Tx, _ string) error {
		var generation, current int64
		err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT d.identity_generation, COALESCE(i.generation, 0) FROM messaging_devices d LEFT JOIN messaging_identities i ON i.user_id = d.user_id WHERE d.id = ? AND d.user_id = ? AND d.`+suspendedDevice), id, actor.UserID).Scan(&generation, &current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if generation != current {
			return ErrMessagingDenied
		}
		if err := messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_devices SET challenge = ?, enrollment_session = ?, expires_at = ?, resume_token_hash = ? WHERE id = ? AND user_id = ? AND `+suspendedDevice), challenge, actor.SessionHash, expiresAt, tokenHash, id, actor.UserID)); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.device_resume_started", id, "")
	})
}

// ResumeDevice sets the pending credential once the device key signs the challenge. A bad
// signature is audited and leaves the challenge in place until it expires.
func (m *messagingStore) ResumeDevice(ctx context.Context, actor MessagingActor, id string, signature []byte) (*MessagingDevice, error) {
	var device *MessagingDevice
	denied := false
	err := m.transaction(ctx, actor, false, func(tx *sql.Tx, _ string) error {
		var challenge, publicKey string
		var generation, current int64
		err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT d.challenge, d.public_key, d.identity_generation, COALESCE(i.generation, 0) FROM messaging_devices d LEFT JOIN messaging_identities i ON i.user_id = d.user_id WHERE d.id = ? AND d.user_id = ? AND d.enrollment_session = ? AND d.expires_at > ? AND d.resume_token_hash <> '' AND d.`+suspendedDevice), id, actor.UserID, actor.SessionHash, time.Now().Unix()).Scan(&challenge, &publicKey, &generation, &current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if generation != current {
			return ErrMessagingDenied
		}
		key, err := base64.StdEncoding.DecodeString(publicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid stored messaging key")
		}
		sum := sha256.Sum256(key)
		fingerprint := "fingerprint=" + hex.EncodeToString(sum[:])
		if !ed25519.Verify(key, []byte(challenge), signature) {
			// Commit the audit row; the error is returned after the transaction.
			denied = true
			return m.audit(ctx, tx, actor, "messaging.device_resume_failed", id, fingerprint)
		}
		err = messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_devices SET token_hash = resume_token_hash, resume_token_hash = '', challenge = '', enrollment_session = '' WHERE id = ? AND `+suspendedDevice), id))
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "duplicate key") {
				return ErrMessagingConflict
			}
			return err
		}
		device, err = scanMessagingDevice(tx.QueryRowContext(ctx, m.store.rebind(`SELECT `+deviceColumns+` FROM messaging_devices WHERE id = ?`), id))
		if err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.device_resumed", id, fingerprint)
	})
	if err == nil && denied {
		return nil, ErrMessagingDenied
	}
	return device, err
}

// SuspendedDevices lists up to limit suspended devices across accounts for the admin
// view, and reports whether more exist.
func (m *messagingStore) SuspendedDevices(ctx context.Context, limit int) ([]SuspendedDevice, bool, error) {
	rows, err := m.store.db.QueryContext(ctx, m.store.rebind(`SELECT d.id, d.user_id, u.username, d.name, d.public_key, d.created_at, d.identity_generation FROM messaging_devices d JOIN users u ON u.id = d.user_id WHERE d.status = 'approved' AND d.token_hash IS NULL ORDER BY d.created_at, d.id LIMIT ?`), limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	devices := []SuspendedDevice{}
	for rows.Next() {
		var d SuspendedDevice
		if err := rows.Scan(&d.ID, &d.UserID, &d.Username, &d.Name, &d.PublicKey, &d.CreatedAt, &d.IdentityGeneration); err != nil {
			return nil, false, err
		}
		devices = append(devices, d)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(devices) > limit {
		return devices[:limit], true, nil
	}
	return devices, false, nil
}

// RevokeSuspendedDevice is the owner's revocation without the ownership check, limited
// to suspended devices: an admin gains no power over a live device.
func (m *messagingStore) RevokeSuspendedDevice(ctx context.Context, adminID, ip, id string) error {
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	err = tx.QueryRowContext(ctx, m.store.rebind(`SELECT user_id FROM messaging_devices WHERE id = ? AND `+suspendedDevice), id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Serialize with the owner's messaging operations, which lock this row first.
	if _, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE users SET updated_at = updated_at WHERE id = ?`), owner); err != nil {
		return err
	}
	if err := messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_devices SET `+revokeDeviceSet+` WHERE id = ? AND user_id = ? AND `+suspendedDevice), id, owner)); err != nil {
		return err
	}
	if err := m.audit(ctx, tx, MessagingActor{UserID: adminID, IP: ip}, "messaging.device_revoked_by_admin", id, "user_id="+owner); err != nil {
		return err
	}
	return tx.Commit()
}
