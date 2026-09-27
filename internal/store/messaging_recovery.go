package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// MessagingRecoveryAuthentication is a single-use identity-reset authentication
// request, not an approval grant. The caller seals its OIDC state before storage.
// Device, subject and registry bindings are derived by Begin, never client claims.
type MessagingRecoveryAuthentication struct {
	StateHash, DeviceID, PublicKey, Subject, RegistryHash, SealedRequest string
	CreatedAt, ExpiresAt                                                 int64
}

func (m *messagingStore) recoveryBindings(ctx context.Context, tx *sql.Tx, actor MessagingActor, target string) (string, string, string, error) {
	var subject, publicKey string
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT sso_subject FROM users WHERE id = ?`), actor.UserID).Scan(&subject); err != nil {
		return "", "", "", err
	}
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT public_key FROM messaging_devices WHERE id = ? AND user_id = ? AND status = 'pending'`), target, actor.UserID).Scan(&publicKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrMessagingDenied
		}
		return "", "", "", err
	}
	rows, err := tx.QueryContext(ctx, m.store.rebind(`SELECT id, public_key, status FROM messaging_devices WHERE user_id = ? ORDER BY id`), actor.UserID)
	if err != nil {
		return "", "", "", err
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var item [3]string
		if err := rows.Scan(&item[0], &item[1], &item[2]); err != nil {
			return "", "", "", err
		}
		if err := json.NewEncoder(hash).Encode(item); err != nil {
			return "", "", "", err
		}
	}
	return subject, publicKey, hex.EncodeToString(hash.Sum(nil)), rows.Err()
}

func (m *messagingStore) BeginRecoveryAuthentication(ctx context.Context, actor MessagingActor, r MessagingRecoveryAuthentication) error {
	now := time.Now().Unix()
	digest, err := hex.DecodeString(r.StateHash)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != r.StateHash || r.SealedRequest == "" || len(r.SealedRequest) > 8192 || r.CreatedAt <= 0 || r.CreatedAt > now || r.ExpiresAt <= now || r.ExpiresAt > r.CreatedAt+300 {
		return ErrMessagingDenied
	}
	return m.transaction(ctx, actor, false, func(tx *sql.Tx, _ string) error {
		// A pending device has proved its key at enrollment, but cannot approve itself.
		var target string
		err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT id FROM messaging_devices WHERE id = ? AND user_id = ? AND token_hash = ? AND status = 'pending'`), r.DeviceID, actor.UserID, actor.DeviceTokenHash).Scan(&target)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMessagingDenied
		}
		if err != nil {
			return err
		}
		subject, key, registry, err := m.recoveryBindings(ctx, tx, actor, target)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.store.rebind(`DELETE FROM messaging_recovery_auth WHERE user_id = ? AND expires_at <= ?`), actor.UserID, now); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COUNT(*) FROM messaging_recovery_auth WHERE user_id = ?`), actor.UserID).Scan(&count); err != nil {
			return err
		}
		if count >= 4 {
			return ErrMessagingLimit
		}
		res, err := tx.ExecContext(ctx, m.store.rebind(`INSERT INTO messaging_recovery_auth (state_hash,user_id,session_hash,device_id,public_key,subject,registry_hash,sealed_request,created_at,expires_at) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT (state_hash) DO NOTHING`), r.StateHash, actor.UserID, actor.SessionHash, target, key, subject, registry, r.SealedRequest, r.CreatedAt, r.ExpiresAt)
		if err := messagingChanged(res, err); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.recovery_auth_started", target, "")
	})
}

func (m *messagingStore) recoveryAuthentication(ctx context.Context, tx *sql.Tx, actor MessagingActor, stateHash string) (MessagingRecoveryAuthentication, error) {
	var r MessagingRecoveryAuthentication
	r.StateHash = stateHash
	err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT device_id,public_key,subject,registry_hash,sealed_request,created_at,expires_at FROM messaging_recovery_auth WHERE state_hash = ? AND user_id = ? AND session_hash = ? AND expires_at > ?`), stateHash, actor.UserID, actor.SessionHash, time.Now().Unix()).Scan(&r.DeviceID, &r.PublicKey, &r.Subject, &r.RegistryHash, &r.SealedRequest, &r.CreatedAt, &r.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrMessagingDenied
	}
	if err != nil {
		return r, err
	}
	subject, key, registry, err := m.recoveryBindings(ctx, tx, actor, r.DeviceID)
	if err != nil {
		return r, err
	}
	if subject != r.Subject || key != r.PublicKey || registry != r.RegistryHash {
		return r, ErrMessagingConflict
	}
	return r, nil
}

func (m *messagingStore) RecoveryAuthentication(ctx context.Context, actor MessagingActor, stateHash string) (MessagingRecoveryAuthentication, error) {
	var r MessagingRecoveryAuthentication
	err := m.transaction(ctx, actor, false, func(tx *sql.Tx, _ string) error {
		var err error
		r, err = m.recoveryAuthentication(ctx, tx, actor, stateHash)
		return err
	})
	if err != nil {
		return MessagingRecoveryAuthentication{}, err
	}
	return r, nil
}

// CompleteRecoveryAuthentication consumes the request after successful OIDC
// verification, rechecking the live session and registry inside the same transaction.
// It audits authentication only: no device approval or reusable reset grant is issued.
func (m *messagingStore) CompleteRecoveryAuthentication(ctx context.Context, actor MessagingActor, stateHash, verifiedSubject string) error {
	return m.transaction(ctx, actor, false, func(tx *sql.Tx, _ string) error {
		r, err := m.recoveryAuthentication(ctx, tx, actor, stateHash)
		if err != nil {
			return err
		}
		if verifiedSubject != r.Subject {
			return ErrMessagingDenied
		}
		if err := messagingChanged(tx.ExecContext(ctx, m.store.rebind(`DELETE FROM messaging_recovery_auth WHERE state_hash = ?`), stateHash)); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.recovery_auth_completed", r.DeviceID, "")
	})
}
