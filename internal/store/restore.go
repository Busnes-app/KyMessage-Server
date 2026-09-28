package store

import (
	"context"
	"time"
)

// InvalidateRestoredGrants is an offline restore operation, before accepting any
// traffic. A snapshot cannot prove which sessions or devices were later revoked.
// Retire every restored room rather than trying to rewind surviving MLS clients.
func (s *SQLStore) InvalidateRestoredGrants(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`DELETE FROM messaging_recovery_auth`,
		`DELETE FROM messaging_reset_receipts`,
		`DELETE FROM sessions`,
		`DELETE FROM mfa_challenges`,
		`DELETE FROM device_pairings`,
		`UPDATE messaging_devices SET status = 'revoked', token_hash = NULL, challenge = '', enrollment_session = ''`,
		`UPDATE messaging_key_packages SET expires_at = 1`,
		`UPDATE messaging_members SET status = 'removed'`,
		// Identity generations are positive. Zero permanently retires ownership,
		// including if a later identity reset reaches a generation used pre-restore.
		`UPDATE messaging_rooms SET owner_identity_generation = 0`,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO audit_records (action, details, created_at) VALUES (?, ?, ?)`),
		"restore.grants_invalidated", "Sessions, challenges and pairings cleared; messaging devices revoked and restored rooms retired. Fresh identity recovery and new rooms required.", time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}
