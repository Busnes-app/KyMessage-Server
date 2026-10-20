package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Busnes-app/ky-primitives/applysetup"
	"strconv"
	"time"
)

// ErrNotInstallerOwned: a valid directory event for a subject installer setup did not create.
// It is an ordinary member's, for the native directory path (sso.ApplyMemberEvent).
var ErrNotInstallerOwned = errors.New("directory identity is not owned by installer setup")

// ApplyInstallerDirectory receives authenticated, narrowed directory desired state.
// It updates only an owned subject and preserves role, local credentials and tickets.
func (s *SQLStore) ApplyInstallerDirectory(ctx context.Context, issuer, subject, event, version, digest string, active, administrator bool) error {
	revision, err := strconv.ParseUint(version, 10, 64)
	if err != nil || revision == 0 || strconv.FormatUint(revision, 10) != version {
		return errors.New("invalid directory revision")
	}
	rawConfig, e := s.Settings().GetSetting(ctx, InstallerConfigKey)
	var config applysetup.Common
	if e != nil || applysetup.DecodeRaw([]byte(rawConfig), &config) != nil || config.SSO == nil || config.SSO.IssuerURL != issuer {
		return errors.New("directory issuer not configured")
	}
	managed, _, err := s.InstallerIdentity(ctx, issuer, subject)
	if err != nil || !managed {
		return errors.New("directory issuer not configured")
	}
	hash := sha256.Sum256([]byte(issuer + "\x00" + subject))
	key := "installer.user." + hex.EncodeToString(hash[:])
	value, err := s.Settings().GetSetting(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return ErrNotInstallerOwned
	}
	if err != nil {
		return err
	}
	var owned installerIdentity
	if json.Unmarshal([]byte(value), &owned) != nil || owned.Issuer != issuer || owned.Subject != subject {
		return errors.New("directory identity binding invalid")
	}
	status := "inactive"
	switch event {
	case "user.created", "user.updated", "user.mfa_reset":
		if active && (owned.Role != "admin" || administrator) {
			status = "active"
		}
	case "user.deleted":
		status = "deleted"
	case "user.deactivated":
	default:
		return errors.New("unsupported directory event")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stateKey := "installer.directory." + hex.EncodeToString(hash[:])
	var prior string
	err = tx.QueryRowContext(ctx, s.rebind("SELECT value FROM server_settings WHERE key=?"), stateKey).Scan(&prior)
	if err == nil {
		var prev struct {
			Revision uint64
			Digest   string
		}
		if json.Unmarshal([]byte(prior), &prev) != nil {
			return errors.New("invalid retained directory state")
		}
		if prev.Revision > revision {
			return errors.New("stale directory event")
		}
		if prev.Revision == revision {
			if prev.Digest != digest {
				return errors.New("conflicting directory revision")
			}
			return nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var previousStatus string
	if err = tx.QueryRowContext(ctx, s.rebind("SELECT status FROM users WHERE id=?"), owned.UserID).Scan(&previousStatus); err != nil {
		return err
	}
	if previousStatus == "deleted" && status == "active" {
		return errors.New("deleted Matrix identity cannot be reactivated")
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, s.rebind("UPDATE users SET status=?,updated_at=? WHERE id=? AND sso_provider=? AND sso_subject=? AND role=?"), status, now, owned.UserID, "kyidentity", subject, owned.Role)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("directory identity drifted")
	}
	if status != "active" || event == "user.mfa_reset" {
		// Managed rows hold no password; a fresh snapshot fails grants fenced on the old one.
		if _, err = tx.ExecContext(ctx, s.rebind("UPDATE users SET password_hash=? WHERE id=?"), "revoked:"+rand.Text(), owned.UserID); err != nil {
			return err
		}
		for _, table := range []string{"sessions", "device_pairings", "mfa_challenges"} {
			if _, err = tx.ExecContext(ctx, s.rebind("DELETE FROM "+table+" WHERE user_id=?"), owned.UserID); err != nil {
				return err
			}
		}
	}
	next, _ := json.Marshal(struct {
		Revision uint64
		Digest   string
	}{revision, digest})
	if _, err = tx.ExecContext(ctx, s.rebind("INSERT INTO server_settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at"), stateKey, string(next), now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.rebind("INSERT INTO audit_records(user_id,action,resource,details,ip_address,created_at) VALUES(?,?,?,?,?,?)"), owned.UserID, "installer.directory_applied", subject, version, "kyidentity", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) InstallerUserAllowed(ctx context.Context, u *User) (bool, error) {
	if u.SSOProvider != "kyidentity" {
		return true, nil
	}
	raw, err := s.Settings().GetSetting(ctx, InstallerConfigKey)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var cfg struct {
		SSO struct {
			IssuerURL string `json:"issuerUrl"`
		} `json:"sso"`
	}
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		return false, errors.New("invalid owned issuer")
	}
	hash := sha256.Sum256([]byte(cfg.SSO.IssuerURL + "\x00" + u.SSOSubject))
	value, err := s.Settings().GetSetting(ctx, "installer.user."+hex.EncodeToString(hash[:]))
	if err != nil {
		return false, err
	}
	var owned installerIdentity
	if json.Unmarshal([]byte(value), &owned) != nil || owned.UserID != u.ID || owned.Role != u.Role {
		return false, errors.New("owned identity does not match session")
	}
	_, allowed, err := s.InstallerIdentity(ctx, cfg.SSO.IssuerURL, u.SSOSubject)
	return allowed, err
}
