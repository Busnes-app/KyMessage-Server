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

var (
	ErrMessagingDenied   = errors.New("messaging access denied")
	ErrMessagingConflict = errors.New("messaging state conflict")
	ErrMessagingLimit    = errors.New("messaging capacity reached")
)

// MessagingActor comes from the authenticated HTTP session, never from JSON.
// A device credential supplements that session; it cannot replace it.
type MessagingActor struct {
	UserID, SessionHash, DeviceTokenHash, IP string
}

type MessagingDevice struct {
	ID, UserID, Name, PublicKey, Status, ApprovedBy string
	CreatedAt                                       int64
}

type MessagingEnrollment struct {
	Device    MessagingDevice
	Challenge string // exact server-issued UTF-8 string to sign with Ed25519
	ExpiresAt int64
	TokenHash string // SHA-256 of a client-generated 256-bit device credential
}

type MessagingRoom struct {
	ID, Name, OwnerID, Membership string
	CreatedAt                     int64
}

type MessagingMember struct{ UserID, Status string }

type MessagingStore interface {
	BeginRecoveryAuthentication(context.Context, MessagingActor, MessagingRecoveryAuthentication) error
	RecoveryAuthentication(context.Context, MessagingActor, string) (MessagingRecoveryAuthentication, error)
	CompleteRecoveryAuthentication(context.Context, MessagingActor, string, string) error
	EnrollDevice(context.Context, MessagingActor, MessagingEnrollment) error
	VerifyDevice(context.Context, MessagingActor, string, []byte) (*MessagingDevice, error)
	ListDevices(context.Context, MessagingActor) ([]MessagingDevice, error)
	ApproveDevice(context.Context, MessagingActor, string) error
	RevokeDevice(context.Context, MessagingActor, string) error
	CreateRoom(context.Context, MessagingActor, MessagingRoom) error
	ListRooms(context.Context, MessagingActor, int) ([]MessagingRoom, error)
	ListMembers(context.Context, MessagingActor, string) ([]MessagingMember, error)
	InviteMember(context.Context, MessagingActor, string, string) error
	AcceptInvite(context.Context, MessagingActor, string) error
	RemoveMember(context.Context, MessagingActor, string, string) error
	DeliveryState(context.Context, MessagingActor, string) (MessagingDeliveryState, error)
	AppendEvent(context.Context, MessagingActor, string, MessagingEventInput) (MessagingReceipt, error)
	ReadEvents(context.Context, MessagingActor, string, int64) (MessagingEventPage, error)
	PublishKeyPackage(context.Context, MessagingActor, MessagingKeyPackage) error
	ClaimKeyPackage(context.Context, MessagingActor, string, string, string) (MessagingKeyPackage, error)
}

type messagingStore struct{ store *SQLStore }

// Account operations really share one bootstrap decision and device registry.
// Use a database row lock, not a process-local mutex, so independent connections
// and server processes cannot approve two first devices or race a revocation.
func (m *messagingStore) transaction(ctx context.Context, actor MessagingActor, needsDevice bool, apply func(*sql.Tx, string) error) error {
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE users SET updated_at = updated_at WHERE id = ? AND status = 'active' AND sso_provider = 'kysignon' AND sso_subject <> '' AND password_hash = '' AND must_change_password = ?`), actor.UserID, false)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrMessagingDenied
	}
	query := `SELECT token_hash FROM sessions WHERE token_hash = ? AND user_id = ? AND expires_at > ?`
	if m.store.driver == "postgres" {
		query += " FOR UPDATE"
	}
	var token string
	err = tx.QueryRowContext(ctx, m.store.rebind(query), actor.SessionHash, actor.UserID, time.Now().UTC()).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMessagingDenied
	}
	if err != nil {
		return err
	}
	var deviceID string
	if needsDevice {
		err = tx.QueryRowContext(ctx, m.store.rebind(`SELECT id FROM messaging_devices WHERE user_id = ? AND token_hash = ? AND status = 'approved'`), actor.UserID, actor.DeviceTokenHash).Scan(&deviceID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMessagingDenied
		}
		if err != nil {
			return err
		}
	}
	if err := apply(tx, deviceID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *messagingStore) audit(ctx context.Context, tx *sql.Tx, actor MessagingActor, action, resource, details string) error {
	_, err := tx.ExecContext(ctx, m.store.rebind(`INSERT INTO audit_records (user_id, action, resource, details, ip_address, created_at) VALUES (?, ?, ?, ?, ?, ?)`), actor.UserID, action, resource, details, actor.IP, time.Now().UTC())
	return err
}

func messagingChanged(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

func (m *messagingStore) EnrollDevice(ctx context.Context, actor MessagingActor, enrollment MessagingEnrollment) error {
	return m.transaction(ctx, actor, false, func(tx *sql.Tx, _ string) error {
		// Only never-verified expired requests are disposable. Verified/revoked keys
		// remain tombstones so losing all devices cannot reset the first-device rule.
		if _, err := tx.ExecContext(ctx, m.store.rebind(`DELETE FROM messaging_devices WHERE user_id = ? AND verified_at IS NULL AND expires_at <= ?`), actor.UserID, time.Now().Unix()); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COUNT(*) FROM messaging_devices WHERE user_id = ?`), actor.UserID).Scan(&count); err != nil {
			return err
		}
		// ponytail: bounded registry including tombstones; add archival identities
		// and explicit reset semantics before raising this 32-device lifetime cap.
		if count >= 32 {
			return ErrMessagingLimit
		}
		d := enrollment.Device
		_, err := tx.ExecContext(ctx, m.store.rebind(`INSERT INTO messaging_devices (id, user_id, name, public_key, status, challenge, enrollment_session, expires_at, created_at, token_hash) VALUES (?, ?, ?, ?, 'unverified', ?, ?, ?, ?, ?)`), d.ID, actor.UserID, d.Name, d.PublicKey, enrollment.Challenge, actor.SessionHash, enrollment.ExpiresAt, d.CreatedAt, enrollment.TokenHash)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "duplicate key") {
				return ErrMessagingConflict
			}
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.device_requested", d.ID, "")
	})
}

const deviceColumns = `id, user_id, name, public_key, status, approved_by, created_at`

func scanMessagingDevice(row interface{ Scan(...any) error }) (*MessagingDevice, error) {
	var d MessagingDevice
	err := row.Scan(&d.ID, &d.UserID, &d.Name, &d.PublicKey, &d.Status, &d.ApprovedBy, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &d, err
}

func (m *messagingStore) VerifyDevice(ctx context.Context, actor MessagingActor, id string, signature []byte) (*MessagingDevice, error) {
	var device *MessagingDevice
	err := m.transaction(ctx, actor, false, func(tx *sql.Tx, _ string) error {
		var challenge, publicKey string
		err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT challenge, public_key FROM messaging_devices WHERE id = ? AND user_id = ? AND enrollment_session = ? AND status = 'unverified' AND expires_at > ?`), id, actor.UserID, actor.SessionHash, time.Now().Unix()).Scan(&challenge, &publicKey)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		key, err := base64.StdEncoding.DecodeString(publicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid stored messaging key")
		}
		if !ed25519.Verify(key, []byte(challenge), signature) {
			return ErrMessagingDenied
		}
		var count int
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COUNT(*) FROM messaging_devices WHERE user_id = ? AND verified_at IS NOT NULL`), actor.UserID).Scan(&count); err != nil {
			return err
		}
		status := "pending"
		if count == 0 {
			status = "approved"
		}
		if _, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_devices SET status = ?, verified_at = ?, challenge = '', enrollment_session = '' WHERE id = ?`), status, time.Now().Unix(), id); err != nil {
			return err
		}
		device, err = scanMessagingDevice(tx.QueryRowContext(ctx, m.store.rebind(`SELECT `+deviceColumns+` FROM messaging_devices WHERE id = ?`), id))
		if err != nil {
			return err
		}
		fingerprint := sha256.Sum256(key)
		return m.audit(ctx, tx, actor, "messaging.device_verified", id, "status="+status+" fingerprint="+hex.EncodeToString(fingerprint[:]))
	})
	return device, err
}

func (m *messagingStore) ListDevices(ctx context.Context, actor MessagingActor) ([]MessagingDevice, error) {
	devices := []MessagingDevice{}
	err := m.transaction(ctx, actor, false, func(tx *sql.Tx, _ string) error {
		rows, err := tx.QueryContext(ctx, m.store.rebind(`SELECT `+deviceColumns+` FROM messaging_devices WHERE user_id = ? ORDER BY created_at, id`), actor.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanMessagingDevice(rows)
			if err != nil {
				return err
			}
			devices = append(devices, *d)
		}
		return rows.Err()
	})
	return devices, err
}

func (m *messagingStore) ApproveDevice(ctx context.Context, actor MessagingActor, id string) error {
	return m.transaction(ctx, actor, true, func(tx *sql.Tx, approver string) error {
		if err := messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_devices SET status = 'approved', approved_by = ? WHERE id = ? AND user_id = ? AND status = 'pending'`), approver, id, actor.UserID)); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.device_approved", id, "approved_by="+approver)
	})
}

// A live owner SSO session can revoke a lost device without possessing that device.
// It cannot approve a replacement or reset the bootstrap history.
func (m *messagingStore) RevokeDevice(ctx context.Context, actor MessagingActor, id string) error {
	return m.transaction(ctx, actor, false, func(tx *sql.Tx, _ string) error {
		if err := messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_devices SET status = 'revoked', token_hash = NULL, challenge = '', enrollment_session = '' WHERE id = ? AND user_id = ? AND status <> 'revoked'`), id, actor.UserID)); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.device_revoked", id, "")
	})
}

func (m *messagingStore) CreateRoom(ctx context.Context, actor MessagingActor, room MessagingRoom) error {
	return m.transaction(ctx, actor, true, func(tx *sql.Tx, _ string) error {
		var count int
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COUNT(*) FROM messaging_rooms WHERE owner_id = ?`), actor.UserID).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return ErrMessagingLimit
		}
		if _, err := tx.ExecContext(ctx, m.store.rebind(`INSERT INTO messaging_rooms (id, name, owner_id, created_at) VALUES (?, ?, ?, ?)`), room.ID, room.Name, actor.UserID, room.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.store.rebind(`INSERT INTO messaging_members (room_id, user_id, status) VALUES (?, ?, 'active')`), room.ID, actor.UserID); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.room_created", room.ID, "")
	})
}

func (m *messagingStore) ListRooms(ctx context.Context, actor MessagingActor, offset int) ([]MessagingRoom, error) {
	rooms := []MessagingRoom{}
	err := m.transaction(ctx, actor, true, func(tx *sql.Tx, _ string) error {
		rows, err := tx.QueryContext(ctx, m.store.rebind(`SELECT r.id, r.name, r.owner_id, r.created_at, m.status FROM messaging_rooms r JOIN messaging_members m ON m.room_id = r.id WHERE m.user_id = ? AND m.status IN ('invited', 'active') ORDER BY r.created_at, r.id LIMIT 100 OFFSET ?`), actor.UserID, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var room MessagingRoom
			if err := rows.Scan(&room.ID, &room.Name, &room.OwnerID, &room.CreatedAt, &room.Membership); err != nil {
				return err
			}
			rooms = append(rooms, room)
		}
		return rows.Err()
	})
	return rooms, err
}

// Room mutations serialize on the room row AFTER the acting account row. They
// never take a second account write lock, avoiding cross-invitation deadlocks.
func (m *messagingStore) ownRoom(ctx context.Context, tx *sql.Tx, actor MessagingActor, room string) error {
	return messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_rooms SET id = id WHERE id = ? AND owner_id = ?`), room, actor.UserID))
}

func (m *messagingStore) InviteMember(ctx context.Context, actor MessagingActor, room, user string) error {
	return m.transaction(ctx, actor, true, func(tx *sql.Tx, _ string) error {
		if err := m.ownRoom(ctx, tx, actor, room); err != nil {
			return err
		}
		var eligible int
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COUNT(*) FROM users WHERE id = ? AND status = 'active' AND sso_provider = 'kysignon' AND sso_subject <> '' AND password_hash = '' AND must_change_password = ?`), user, false).Scan(&eligible); err != nil {
			return err
		}
		if eligible != 1 {
			return ErrNotFound
		}
		var count int
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COUNT(*) FROM messaging_members WHERE room_id = ?`), room).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return ErrMessagingLimit
		}
		result, err := tx.ExecContext(ctx, m.store.rebind(`INSERT INTO messaging_members (room_id, user_id, status) VALUES (?, ?, 'invited') ON CONFLICT (room_id, user_id) DO UPDATE SET status = 'invited' WHERE messaging_members.status = 'removed'`), room, user)
		if err := messagingChanged(result, err); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.member_invited", room, "user_id="+user)
	})
}

func (m *messagingStore) AcceptInvite(ctx context.Context, actor MessagingActor, room string) error {
	return m.transaction(ctx, actor, true, func(tx *sql.Tx, _ string) error {
		if err := m.lockDeliveryRoom(ctx, tx, room); err != nil {
			return err
		}
		if err := messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_members SET status = 'active', generation = generation + 1 WHERE room_id = ? AND user_id = ? AND status = 'invited'`), room, actor.UserID)); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.member_joined", room, "")
	})
}

func (m *messagingStore) RemoveMember(ctx context.Context, actor MessagingActor, room, user string) error {
	return m.transaction(ctx, actor, true, func(tx *sql.Tx, _ string) error {
		if err := m.ownRoom(ctx, tx, actor, room); err != nil {
			return err
		}
		if user == actor.UserID {
			return ErrMessagingConflict
		}
		if err := messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_members SET status = 'removed' WHERE room_id = ? AND user_id = ? AND status <> 'removed'`), room, user)); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.member_removed", room, "user_id="+user)
	})
}

func (m *messagingStore) ListMembers(ctx context.Context, actor MessagingActor, room string) ([]MessagingMember, error) {
	members := []MessagingMember{}
	err := m.transaction(ctx, actor, true, func(tx *sql.Tx, _ string) error {
		// Lock this ACL row against concurrent removal before fetching the roster.
		if err := messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_members SET status = status WHERE room_id = ? AND user_id = ? AND status = 'active'`), room, actor.UserID)); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, m.store.rebind(`SELECT user_id, status FROM messaging_members WHERE room_id = ? AND status <> 'removed' ORDER BY user_id`), room)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var member MessagingMember
			if err := rows.Scan(&member.UserID, &member.Status); err != nil {
				return err
			}
			members = append(members, member)
		}
		return rows.Err()
	})
	return members, err
}
