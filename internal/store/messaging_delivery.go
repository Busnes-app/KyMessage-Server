package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// These are delivery-service declarations, not proof of MLS transcript validity.
// Receiving clients must authenticate/decrypt MLS and check its roster and epoch.
type MessagingRosterDevice struct {
	ID, UserID, PublicKey          string
	Generation, IdentityGeneration int64
}
type MessagingDeliveryState struct {
	Epoch, Sequence, RetentionDays, RetainedFrom int64
	RosterHash                                   string
	Paused                                       bool
	Devices                                      []MessagingRosterDevice
}
type MessagingEventInput struct {
	ID, Kind, RosterHash, Payload string
	Epoch                         int64
	Welcomes                      map[string]string
}
type MessagingReceipt struct{ Sequence, Epoch int64 }
type MessagingEvent struct {
	ID, DeviceID, Kind, RosterHash, Payload, Welcome string
	Sequence, Epoch, CreatedAt, ExpiresAt            int64
}
type MessagingEventPage struct {
	Events              []MessagingEvent
	Next, StartSequence int64
}

// A room's accepted transcript has one order. This database lock also covers
// membership mutations; it works across processes and both database engines.
func (m *messagingStore) lockDeliveryRoom(ctx context.Context, tx *sql.Tx, room string) error {
	return messagingChanged(tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_rooms SET sequence = sequence WHERE id = ?`), room))
}

func (m *messagingStore) deliveryState(ctx context.Context, tx *sql.Tx, actor MessagingActor, room string) (MessagingDeliveryState, string, int64, error) {
	state := MessagingDeliveryState{Devices: []MessagingRosterDevice{}}
	if err := m.lockDeliveryRoom(ctx, tx, room); err != nil {
		return state, "", 0, err
	}
	var member int
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COUNT(*) FROM messaging_members WHERE room_id = ? AND user_id = ? AND status = 'active'`), room, actor.UserID).Scan(&member); err != nil {
		return state, "", 0, err
	}
	if member != 1 {
		return state, "", 0, ErrNotFound
	}
	if err := m.expireRoom(ctx, tx, room, time.Now().Unix()); err != nil {
		return state, "", 0, err
	}
	var committed, owner string
	var retained int64
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT epoch, sequence, roster_hash, owner_id, retained_bytes, retention_days, retained_from FROM messaging_rooms WHERE id = ?`), room).Scan(&state.Epoch, &state.Sequence, &committed, &owner, &retained, &state.RetentionDays, &state.RetainedFrom); err != nil {
		return state, "", 0, err
	}
	rows, err := tx.QueryContext(ctx, m.store.rebind(`SELECT d.id, d.user_id, d.public_key, m.generation, d.identity_generation FROM messaging_devices d JOIN messaging_members m ON m.user_id = d.user_id JOIN users u ON u.id = d.user_id WHERE m.room_id = ? AND m.status = 'active' AND m.identity_generation = d.identity_generation AND d.status = 'approved' AND u.status = 'active' AND u.sso_provider = 'kysignon' AND u.sso_subject <> '' AND u.password_hash = '' AND u.must_change_password = ? ORDER BY d.id`), room, false)
	if err != nil {
		return state, "", 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var d MessagingRosterDevice
		if err := rows.Scan(&d.ID, &d.UserID, &d.PublicKey, &d.Generation, &d.IdentityGeneration); err != nil {
			return state, "", 0, err
		}
		state.Devices = append(state.Devices, d)
	}
	if err := rows.Err(); err != nil {
		return state, "", 0, err
	}
	// Fixed fields, ordered devices and a domain/room binding make this a stable
	// change token. Clients use the returned token, not their own JSON serializer.
	wire, err := json.Marshal(struct {
		Domain, Room string
		Devices      []MessagingRosterDevice
	}{"KyMessages roster v2", room, state.Devices})
	if err != nil {
		return state, "", 0, err
	}
	digest := sha256.Sum256(wire)
	state.RosterHash = hex.EncodeToString(digest[:])
	state.Paused = state.Epoch == 0 || committed != state.RosterHash
	return state, owner, retained, nil
}

func (m *messagingStore) DeliveryState(ctx context.Context, actor MessagingActor, room string) (MessagingDeliveryState, error) {
	var state MessagingDeliveryState
	err := m.transaction(ctx, actor, true, func(tx *sql.Tx, _ string) error {
		var err error
		state, _, _, err = m.deliveryState(ctx, tx, actor, room)
		return err
	})
	return state, err
}

type epochDevice struct{ generation, joined int64 }

func (m *messagingStore) epochDevices(ctx context.Context, tx *sql.Tx, room string) (map[string]epochDevice, error) {
	rows, err := tx.QueryContext(ctx, m.store.rebind(`SELECT device_id, generation, joined_sequence FROM messaging_epoch_devices WHERE room_id = ?`), room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := map[string]epochDevice{}
	for rows.Next() {
		var id string
		var d epochDevice
		if err := rows.Scan(&id, &d.generation, &d.joined); err != nil {
			return nil, err
		}
		devices[id] = d
	}
	return devices, rows.Err()
}

func rosterGeneration(state MessagingDeliveryState, device string) (int64, bool) {
	for _, d := range state.Devices {
		if d.ID == device {
			return d.Generation, true
		}
	}
	return 0, false
}

// Inputs are bounded/canonicalized at the HTTP boundary. All authorization and
// transcript checks happen here in the same transaction as the append and audit.
func (m *messagingStore) AppendEvent(ctx context.Context, actor MessagingActor, room string, input MessagingEventInput) (MessagingReceipt, error) {
	var receipt MessagingReceipt
	err := m.transaction(ctx, actor, true, func(tx *sql.Tx, device string) error {
		state, owner, retained, err := m.deliveryState(ctx, tx, actor, room)
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
		if !eligible || (!included && !(state.Epoch == 0 && owner == actor.UserID && input.Kind == "commit")) {
			return ErrMessagingDenied
		}
		wire, err := json.Marshal(input)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(wire)
		requestHash := hex.EncodeToString(sum[:])
		var priorHash string
		err = tx.QueryRowContext(ctx, m.store.rebind(`SELECT sequence, epoch, request_hash FROM messaging_events WHERE room_id = ? AND device_id = ? AND event_id = ?`), room, device, input.ID).Scan(&receipt.Sequence, &receipt.Epoch, &priorHash)
		if err == nil {
			if priorHash != requestHash {
				return ErrMessagingConflict
			}
			return nil // Retry returns the durable receipt even after later commits.
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if input.Epoch != state.Epoch || input.RosterHash != state.RosterHash {
			return ErrMessagingConflict
		}
		receipt = MessagingReceipt{Sequence: state.Sequence + 1, Epoch: state.Epoch}
		if input.Kind == "application" {
			if state.Paused || len(input.Welcomes) != 0 {
				return ErrMessagingConflict
			}
		} else if input.Kind == "commit" {
			receipt.Epoch++
			added := 0
			for _, d := range state.Devices {
				prior, exists := old[d.ID]
				if exists && prior.generation == d.Generation {
					continue
				}
				// The initial committer owns the initial local group state.
				if state.Epoch == 0 && d.ID == device {
					continue
				}
				if input.Welcomes[d.ID] == "" {
					return ErrMessagingConflict
				}
				added++
			}
			if len(input.Welcomes) != added {
				return ErrMessagingConflict
			}
		} else {
			return ErrMessagingConflict
		}
		size := int64(len(input.Payload))
		for _, welcome := range input.Welcomes {
			size += int64(len(welcome))
		}
		// Bound active ciphertext separately from permanent retry receipts.
		var active int
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COUNT(*) FROM messaging_events WHERE room_id = ? AND payload <> ''`), room).Scan(&active); err != nil {
			return err
		}
		if active >= 4096 || state.Sequence >= 1_000_000 || retained+size > 32*1024*1024 {
			return ErrMessagingLimit
		}
		now := time.Now().Unix()
		expires := now + state.RetentionDays*86400
		var previousExpiry int64
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COALESCE(MAX(expires_at), 0) FROM messaging_events WHERE room_id = ? AND sequence = ?`), room, state.Sequence).Scan(&previousExpiry); err != nil {
			return err
		}
		// Keep expiry ordered if the wall clock moves back.
		if expires < previousExpiry {
			expires = previousExpiry
		}
		_, err = tx.ExecContext(ctx, m.store.rebind(`INSERT INTO messaging_events (room_id, sequence, device_id, event_id, kind, epoch, roster_hash, payload, request_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), room, receipt.Sequence, device, input.ID, input.Kind, receipt.Epoch, input.RosterHash, input.Payload, requestHash, now, expires)
		if err != nil {
			return err
		}
		if input.Kind == "commit" {
			if _, err := tx.ExecContext(ctx, m.store.rebind(`DELETE FROM messaging_epoch_devices WHERE room_id = ?`), room); err != nil {
				return err
			}
			for _, d := range state.Devices {
				joined := receipt.Sequence
				if prior, exists := old[d.ID]; exists && prior.generation == d.Generation {
					joined = prior.joined
				}
				if _, err := tx.ExecContext(ctx, m.store.rebind(`INSERT INTO messaging_epoch_devices (room_id, device_id, generation, joined_sequence) VALUES (?, ?, ?, ?)`), room, d.ID, d.Generation, joined); err != nil {
					return err
				}
			}
			for id, welcome := range input.Welcomes {
				if _, err := tx.ExecContext(ctx, m.store.rebind(`INSERT INTO messaging_welcomes (room_id, sequence, device_id, payload) VALUES (?, ?, ?, ?)`), room, receipt.Sequence, id, welcome); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_rooms SET roster_hash = ? WHERE id = ?`), input.RosterHash, room); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_rooms SET epoch = ?, sequence = ?, retained_bytes = retained_bytes + ? WHERE id = ?`), receipt.Epoch, receipt.Sequence, size, room); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.event_accepted", room, fmt.Sprintf("device_id=%s sequence=%d kind=%s", device, receipt.Sequence, input.Kind))
	})
	return receipt, err
}

func (m *messagingStore) ReadEvents(ctx context.Context, actor MessagingActor, room string, after int64) (MessagingEventPage, error) {
	page := MessagingEventPage{Events: []MessagingEvent{}, Next: after}
	err := m.transaction(ctx, actor, true, func(tx *sql.Tx, device string) error {
		state, _, _, err := m.deliveryState(ctx, tx, actor, room)
		if err != nil {
			return err
		}
		old, err := m.epochDevices(ctx, tx, room)
		if err != nil {
			return err
		}
		generation, eligible := rosterGeneration(state, device)
		prior, included := old[device]
		if !eligible || !included || prior.generation != generation {
			return ErrMessagingDenied
		}
		if after > state.Sequence {
			return ErrMessagingConflict
		}
		if after < state.RetainedFrom-1 && prior.joined < state.RetainedFrom {
			return ErrMessagingHistoryGone
		}
		page.StartSequence = prior.joined
		if page.Next < prior.joined-1 {
			page.Next = prior.joined - 1
		}
		rows, err := tx.QueryContext(ctx, m.store.rebind(`SELECT e.sequence, e.device_id, e.event_id, e.kind, e.epoch, e.roster_hash, e.payload, e.created_at, e.expires_at, COALESCE(w.payload, '') FROM messaging_events e LEFT JOIN messaging_welcomes w ON w.room_id = e.room_id AND w.sequence = e.sequence AND w.device_id = ? WHERE e.room_id = ? AND e.sequence > ? AND e.payload <> '' ORDER BY e.sequence LIMIT 50`), device, room, page.Next)
		if err != nil {
			return err
		}
		defer rows.Close()
		size := 0
		for rows.Next() {
			var e MessagingEvent
			if err := rows.Scan(&e.Sequence, &e.DeviceID, &e.ID, &e.Kind, &e.Epoch, &e.RosterHash, &e.Payload, &e.CreatedAt, &e.ExpiresAt, &e.Welcome); err != nil {
				return err
			}
			size += len(e.Payload) + len(e.Welcome)
			if size > 1024*1024 && len(page.Events) > 0 {
				break
			}
			page.Events = append(page.Events, e)
			page.Next = e.Sequence
		}
		return rows.Err()
	})
	return page, err
}
