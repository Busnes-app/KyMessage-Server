# Thread Auto-Purge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each thread's owner chooses Off, 1, 7, 30 or 90 days (default 90). Messages older than the window are deleted outright: ciphertext, Welcomes, retry receipts and per-event audit rows.

**Architecture:**
- `messaging_rooms.retention_days` gains 0 (Off) and 90. Expiry is derived from a per-room monotonic `created_at` instead of the stored `messaging_events.expires_at`, which is dropped.
- A prefix purge (`purgeRoom`) replaces payload blanking (`expireRoom`). It runs under the room lock on every room operation and in the one-minute sweep.
- Owners change the window with `PATCH /api/messaging/rooms/{room}`.
- Per-room caps become quotas: 100,000 active events and 512 MiB.

**Tech Stack:** Go (`net/http`, `database/sql`, SQLite via modernc and PostgreSQL 17), TypeScript isolated client (`mls-proof`, Playwright).

**Spec:** `docs/superpowers/specs/2026-09-29-thread-purge-and-split-backups-design.md`, section 1. Section 2 (backups) is a separate plan.

## Global Constraints

- Retention values: `0` (Off), `1`, `7`, `30`, `90`. The default for new rooms is `90`.
- Who may change retention:
  - Group room: the owner at the current identity generation.
  - Direct room (`direct_peer_id <> ''`): the owner or the peer.
  - Either way the caller needs an approved device and an active membership.
- Purge deletes the event row, its Welcomes and its `messaging.event_accepted` audit row, then advances `retained_from`. Purges are always a sequence prefix.
- Room quotas: `MessagingActiveEventLimit = 100_000` and `MessagingRetainedByteLimit = 512 << 20`. `MessagingReceiptLimit = 1_000_000` stays and counts lifetime appends (`sequence`).
- Clients discard a pending send older than its room window.
- Both database engines: every store test runs on SQLite by default and on Postgres with `KY_TEST_POSTGRES_DSN`.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- Follow the DOX chain: read `AGENTS.md` at the repo root, `internal/store`, `internal/api` and `mls-proof` before editing, and update them after.

## Review Focus

1. **Clock moving backwards.** A later event gets an earlier `created_at`, and the purge must still delete a prefix and never leave a hole. Pin it in Task 1 (monotonic `created_at`).
2. **Shortening from Off to 1 day on a large room.** The first purge deletes many rows in one transaction and must not break live readers. Pin it in Task 2: switch Off to 1 with 3 aged events and check the floor and the 410 response.
3. **Old clients sending `retention_days: 30` explicitly, or omitting it.** An omitted value must become 90, and an explicit 0 must mean Off (not "default"). Pin it in Task 3.
4. **Direct-room peer who has not accepted the invite.** A peer who is still only invited must not change retention (404, like any non-member); once they accept, they may. Pin it in Task 2.
5. **Off rooms and local copies.** A client in an Off room must never compute `expiresAt = now` and drop messages. Pin it in Task 4 by parsing retention 0 and giving local messages no expiry.

---

### Task 1: Schema migration and prefix purge in the store

**Files:**
- Modify: `internal/store/migrations/migrations.go`: add migration 17.
- Modify: `internal/store/messaging_retention.go`: replace `expireRoom` with `purgeRoom`; rewrite `ExpireMessages`.
- Modify: `internal/store/messaging_delivery.go`:
  - `deliveryState` (~line 60): call `purgeRoom`.
  - `AppendEvent` (~205-226): monotonic `created_at`, quota by counters, no `expires_at`.
  - `ReadEvents` (~285-293): derive `ExpiresAt`.
- Modify: `internal/store/messaging_usage.go:6-8`: raise the limits.
- Modify: `internal/store/messaging.go:290-291`: default retention 90.
- Test: `internal/store/messaging_retention_test.go`. Rewrite the aging helpers to backdate `created_at` instead of setting `expires_at`.

**Interfaces:**
- Produces:
  - `func (m *messagingStore) purgeRoom(ctx context.Context, tx *sql.Tx, room string, now int64) error`. The caller holds the room lock.
  - `store.MessagingEvent.ExpiresAt` is `0` when the room is Off, otherwise `created_at + days*86400`.
  - The constants `MessagingActiveEventLimit`, `MessagingRetainedByteLimit` and `MessagingReceiptLimit` keep their names.

- [ ] **Step 1: Write the migration.** Append to the registry after version 16:

```go
	// Retention becomes owner-mutable and may be Off (0) or 90 days, and purging deletes
	// events outright, so expiry derives from created_at instead of a stored column.
	// Column-level CHECKs drop with their column; a table rebuild would cascade-delete rooms.
	{Version: 17, Name: "thread_auto_purge", SQLite: threadAutoPurge, Postgres: threadAutoPurge},
```

and the constant, identical for both engines:

```go
const threadAutoPurge = `
ALTER TABLE messaging_rooms ADD COLUMN purge_days BIGINT NOT NULL DEFAULT 90 CHECK(purge_days IN (0, 1, 7, 30, 90));
UPDATE messaging_rooms SET purge_days = retention_days;
ALTER TABLE messaging_rooms DROP COLUMN retention_days;
ALTER TABLE messaging_rooms RENAME COLUMN purge_days TO retention_days;
DROP INDEX messaging_events_expiry;
DROP INDEX messaging_events_room_expiry;
ALTER TABLE messaging_events DROP COLUMN expires_at;
DELETE FROM messaging_events WHERE payload = '';
CREATE INDEX messaging_events_room_created ON messaging_events(room_id, created_at);
`
```

`DELETE ... payload = ''` removes metadata left behind by the old payload-blanking expiry. Their Welcomes were already deleted, and `retained_from` is already past them.

- [ ] **Step 2: Write failing store tests.** At the top of `internal/store/messaging_retention_test.go`, add shared helpers. Then replace every `UPDATE messaging_events SET expires_at = ...` in the file with `age(t, db, driver, "<room>", 400*86400)`.

Also change the existing assertion in `TestMessagingRetentionPreservesReceiptsAndRequiresRejoin` that says `expired retry appended again`. Purge now deletes the receipt, and the join commit's epoch is stale, so a retry is refused:

```go
	if _, err := st.Messaging().AppendEvent(ctx, a, "retained", join); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatal("a purged stale-epoch retry must be refused", err)
	}
```

Delete the following "changed retry" block, because it asserts the same thing. Also replace `SELECT COUNT(*) FROM messaging_events WHERE payload <> ''` with `SELECT COUNT(*) FROM messaging_events`, since rows are now deleted rather than blanked.

```go
// retentionDB opens the store and a raw handle on the same database.
func retentionDB(t *testing.T) (context.Context, store.Store, *sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	cfg := testdb.Config(t)
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	driver := cfg.Driver
	if driver == "postgres" {
		driver = "pgx"
	}
	db, err := sql.Open(driver, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return ctx, st, db, driver
}

// age moves every event in room back by seconds, as if written that long ago.
func age(t *testing.T, db *sql.DB, driver, room string, seconds int64) {
	t.Helper()
	q := `UPDATE messaging_events SET created_at = created_at - ? WHERE room_id = ?`
	if driver == "pgx" {
		q = `UPDATE messaging_events SET created_at = created_at - $1 WHERE room_id = $2`
	}
	if _, err := db.Exec(q, seconds, room); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *sql.DB, driver, query, room string) int64 {
	t.Helper()
	if driver == "pgx" {
		query = strings.ReplaceAll(query, "?", "$1")
	}
	var n int64
	if err := db.QueryRow(query, room).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// purgeRoomFixture creates a room with the given retention, a member bob with a Welcome,
// and three events (join commit, then two applications).
func purgeRoomFixture(t *testing.T, st store.Store, room string, days int64) (a, b store.MessagingActor) {
	t.Helper()
	ctx := context.Background()
	a, _ = deliveryDevice(t, st, messagingActor(t, st, "alice"))
	b, bid := deliveryDevice(t, st, messagingActor(t, st, "bob"))
	if err := st.Messaging().CreateRoom(ctx, a, store.MessagingRoom{ID: room, Name: room, RetentionDays: days}); err != nil {
		t.Fatal(err)
	}
	appendDelivery(t, st, a, room, deliveryInput(t, st, a, room, "commit"))
	if err := st.Messaging().InviteMember(ctx, a, room, b.UserID); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().AcceptInvite(ctx, b, room); err != nil {
		t.Fatal(err)
	}
	join := deliveryInput(t, st, a, room, "commit")
	join.Welcomes[bid] = "d2VsY29tZQ=="
	appendDelivery(t, st, a, room, join)
	appendDelivery(t, st, a, room, deliveryInput(t, st, a, room, "application"))
	return a, b
}

func TestPurgeDeletesEventsWelcomesAndAuditRows(t *testing.T) {
	ctx, st, db, driver := retentionDB(t)
	_, b := purgeRoomFixture(t, st, "purged", 1)
	age(t, db, driver, "purged", 2*86400)
	backdateAudit := `UPDATE audit_records SET created_at = ? WHERE resource = ?`
	if driver == "pgx" {
		backdateAudit = `UPDATE audit_records SET created_at = $1 WHERE resource = $2`
	}
	if _, err := db.Exec(backdateAudit, time.Now().UTC().Add(-48*time.Hour), "purged"); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().ExpireMessages(ctx); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int64{
		`SELECT COUNT(*) FROM messaging_events WHERE room_id = ?`:                                          0,
		`SELECT COUNT(*) FROM messaging_welcomes WHERE room_id = ?`:                                        0,
		`SELECT COUNT(*) FROM audit_records WHERE action = 'messaging.event_accepted' AND resource = ?`:     0,
		`SELECT retained_bytes FROM messaging_rooms WHERE id = ?`:                                          0,
		`SELECT retained_from FROM messaging_rooms WHERE id = ?`:                                           4,
		`SELECT COUNT(*) FROM audit_records WHERE action = 'messaging.room_created' AND resource = ?`:      1,
	} {
		if got := count(t, db, driver, query, "purged"); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
	if _, err := st.Messaging().ReadEvents(ctx, b, "purged", 0); !errors.Is(err, store.ErrMessagingHistoryGone) {
		t.Fatal("a reader behind the purged prefix must see the gap", err)
	}
}

func TestPurgeOffKeepsEverything(t *testing.T) {
	ctx, st, db, driver := retentionDB(t)
	purgeRoomFixture(t, st, "kept", 0)
	age(t, db, driver, "kept", 400*86400)
	if err := st.Messaging().ExpireMessages(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, driver, `SELECT COUNT(*) FROM messaging_events WHERE room_id = ?`, "kept"); got != 3 {
		t.Fatalf("Off purged events: %d left", got)
	}
}

func TestCreatedAtStaysMonotonicWhenClockMovesBack(t *testing.T) {
	_, st, db, driver := retentionDB(t)
	a, _ := purgeRoomFixture(t, st, "clock", 1)
	future := `UPDATE messaging_events SET created_at = ? WHERE room_id = ? AND sequence = 3`
	if driver == "pgx" {
		future = `UPDATE messaging_events SET created_at = $1 WHERE room_id = $2 AND sequence = 3`
	}
	ahead := time.Now().Unix() + 3600
	if _, err := db.Exec(future, ahead, "clock"); err != nil {
		t.Fatal(err)
	}
	appendDelivery(t, st, a, "clock", deliveryInput(t, st, a, "clock", "application"))
	if got := count(t, db, driver, `SELECT created_at FROM messaging_events WHERE room_id = ? AND sequence = 4`, "clock"); got < ahead {
		t.Fatalf("created_at went backwards: %d < %d", got, ahead)
	}
}
```

Add `"strings"` to the imports.

- [ ] **Step 3: Run the tests and see them fail.**

Run: `go test ./internal/store/ -run 'Purge|CreatedAtStaysMonotonic' -v`
Expected: FAIL. Either the column doesn't exist yet, or the events still exist because only the payload was blanked.

- [ ] **Step 4: Implement `purgeRoom` and the sweep.** Replace the body of `internal/store/messaging_retention.go` after `ErrMessagingHistoryGone`:

```go
// purgeRoom deletes the room's events older than its retention window: rows, Welcomes and
// their per-event audit rows. created_at is monotonic per room, so this is a sequence prefix
// and retained_from stays an exact floor. The caller holds the room row lock.
func (m *messagingStore) purgeRoom(ctx context.Context, tx *sql.Tx, room string, now int64) error {
	var days int64
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT retention_days FROM messaging_rooms WHERE id = ?`), room).Scan(&days); err != nil || days == 0 {
		return err
	}
	cutoff := now - days*86400
	var last, bytes int64
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COALESCE(MAX(sequence), 0), COALESCE(SUM(LENGTH(payload)), 0) FROM messaging_events WHERE room_id = ? AND created_at <= ?`), room, cutoff).Scan(&last, &bytes); err != nil || last == 0 {
		return err
	}
	var welcomes int64
	if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COALESCE(SUM(LENGTH(payload)), 0) FROM messaging_welcomes WHERE room_id = ? AND sequence <= ?`), room, last).Scan(&welcomes); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM messaging_welcomes WHERE room_id = ? AND sequence <= ?`,
		`DELETE FROM messaging_events WHERE room_id = ? AND sequence <= ?`,
	} {
		if _, err := tx.ExecContext(ctx, m.store.rebind(q), room, last); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, m.store.rebind(`DELETE FROM audit_records WHERE action = 'messaging.event_accepted' AND resource = ? AND created_at <= ?`), room, time.Unix(cutoff, 0).UTC()); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_rooms SET retained_bytes = retained_bytes - ?, retained_from = CASE WHEN retained_from < ? THEN ? ELSE retained_from END WHERE id = ?`), bytes+welcomes, last+1, last+1, room)
	return err
}

// ExpireMessages purges every room with events past its window, in short per-room
// transactions. Room operations also purge under their own lock.
func (m *messagingStore) ExpireMessages(ctx context.Context) error {
	for {
		now := time.Now().Unix()
		rows, err := m.store.db.QueryContext(ctx, m.store.rebind(`SELECT r.id FROM messaging_rooms r WHERE r.retention_days > 0 AND EXISTS (SELECT 1 FROM messaging_events e WHERE e.room_id = r.id AND e.created_at <= ? - r.retention_days * 86400) LIMIT 100`), now)
		if err != nil {
			return err
		}
		rooms := []string{}
		for rows.Next() {
			var room string
			if err = rows.Scan(&room); err != nil {
				break
			}
			rooms = append(rooms, room)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil || len(rooms) == 0 {
			return err
		}
		for _, room := range rooms {
			if err := m.purgeOne(ctx, room, now); err != nil {
				return err
			}
		}
	}
}
```

Replace `expireRoomTransaction` with:

```go
func (m *messagingStore) purgeOne(ctx context.Context, room string, now int64) error {
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := m.lockDeliveryRoom(ctx, tx, room); err != nil {
		// Room deletion can win after discovery.
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if err := m.purgeRoom(ctx, tx, room, now); err != nil {
		return err
	}
	return tx.Commit()
}
```

Delete `expireRoom` and `expireRoomTransaction`.

- [ ] **Step 5: Update the delivery code.** In `messaging_delivery.go`:
  - `deliveryState`: replace `m.expireRoom(ctx, tx, room, time.Now().Unix())` with `m.purgeRoom(ctx, tx, room, time.Now().Unix())`.
  - `AppendEvent`: replace the active `COUNT(*)` query, the limit check and the expiry block, lines ~207-225, with:

```go
		// The ordered retained prefix makes sequence - retained_from + 1 the active count.
		if state.Sequence-state.RetainedFrom+1 >= MessagingActiveEventLimit || state.Sequence >= MessagingReceiptLimit || retained+size > MessagingRetainedByteLimit {
			return ErrMessagingLimit
		}
		now := time.Now().Unix()
		// Keep created_at monotonic per room if the wall clock moves back, so a purge by
		// age is always a sequence prefix.
		var previous int64
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COALESCE(MAX(created_at), 0) FROM messaging_events WHERE room_id = ? AND sequence = ?`), room, state.Sequence).Scan(&previous); err != nil {
			return err
		}
		if now < previous {
			now = previous
		}
		_, err = tx.ExecContext(ctx, m.store.rebind(`INSERT INTO messaging_events (room_id, sequence, device_id, event_id, kind, epoch, roster_hash, payload, request_hash, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), room, receipt.Sequence, device, input.ID, input.Kind, receipt.Epoch, input.RosterHash, input.Payload, requestHash, now)
```

  - `ReadEvents`: change the SELECT to drop `e.expires_at` and the `AND e.payload <> ''` filter. Scan into `&e.CreatedAt` only, then after the scan:

```go
			if state.RetentionDays > 0 {
				e.ExpiresAt = e.CreatedAt + state.RetentionDays*86400
			}
```

- [ ] **Step 6: Raise the quotas and the default.**
  - In `messaging_usage.go`, set `MessagingActiveEventLimit = 100_000` and `MessagingRetainedByteLimit = 512 << 20`.
  - In `messaging.go` `CreateRoom`, delete the `if room.RetentionDays == 0 { room.RetentionDays = 30 }` block. The API now always supplies the value, and 0 means Off.
  - In `internal/store/AGENTS.md`, replace the "4,096 events/32 MiB" wording with the new quotas.

- [ ] **Step 7: Fix the remaining fixtures that aged via `expires_at`.**
  - `internal/api/messaging_retention_test.go:71`
  - `mls-proof/server/main.go:88`: change it to `UPDATE messaging_events SET created_at = created_at - 400*86400 WHERE room_id = ?`. The room must then be non-Off, and the browser test creates it with 1 day.
  - `cmd/server/restore_test.go`, `internal/backup/payload_test.go` and `internal/api/api_test.go`: grep each for `expires_at` and replace with the same backdating.

- [ ] **Step 8: Run the store, API and backup tests on both engines.**

Run: `go test ./internal/store/ ./internal/api/ ./internal/backup/ ./cmd/server/`
Expected: PASS. Then run the same with `KY_TEST_POSTGRES_DSN` set against a disposable `postgres:17-alpine`: PASS.

- [ ] **Step 9: Commit.**

```bash
git add internal/store internal/api cmd/server internal/backup mls-proof/server
git commit -m "messaging: purge old events outright; retention Off/1/7/30/90"
```

---

### Task 2: Owner-mutable retention in the store

**Files:**
- Modify: `internal/store/messaging.go`: add `SetRoomRetention`.
- Modify: `internal/store/store.go`: add to the `MessagingStore` interface.
- Test: `internal/store/messaging_retention_test.go`.

**Interfaces:**
- Consumes: `purgeRoom` (Task 1), `ownRoom` and `transaction` (existing).
- Produces: `SetRoomRetention(ctx context.Context, actor MessagingActor, room string, days int64) error`. It returns `ErrMessagingDenied` for a caller who is not the owner or peer, and `ErrNotFound` for a non-member.

- [ ] **Step 1: Write the failing tests** in `internal/store/messaging_retention_test.go`, using the Task 1 helpers:

```go
func TestSetRoomRetentionAuthority(t *testing.T) {
	ctx, st, db, driver := retentionDB(t)
	a, b := purgeRoomFixture(t, st, "group", 30)
	if err := st.Messaging().SetRoomRetention(ctx, b, "group", 7); !errors.Is(err, store.ErrMessagingDenied) {
		t.Fatalf("member changed retention: %v", err)
	}
	if err := st.Messaging().SetRoomRetention(ctx, a, "group", 5); !errors.Is(err, store.ErrMessagingConflict) {
		t.Fatalf("invalid choice accepted: %v", err)
	}
	if err := st.Messaging().SetRoomRetention(ctx, a, "group", 7); err != nil {
		t.Fatal(err)
	}
	state, err := st.Messaging().DeliveryState(ctx, a, "group")
	if err != nil || state.RetentionDays != 7 {
		t.Fatalf("retention not stored: %+v %v", state, err)
	}
	if got := count(t, db, driver, `SELECT COUNT(*) FROM audit_records WHERE action = 'messaging.retention_changed' AND resource = ? AND details = 'retention_days=7'`, "group"); got != 1 {
		t.Fatalf("retention change audit rows: %d", got)
	}

	// Direct room: an invited peer is not yet a member; after accepting they may change it.
	c, _ := deliveryDevice(t, st, messagingActor(t, st, "carol"))
	if err := st.Messaging().CreateRoom(ctx, a, store.MessagingRoom{ID: "direct", Name: "direct", PeerUserID: c.UserID, RetentionDays: 90}); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().SetRoomRetention(ctx, c, "direct", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("invited peer changed retention: %v", err)
	}
	if err := st.Messaging().AcceptInvite(ctx, c, "direct"); err != nil {
		t.Fatal(err)
	}
	if err := st.Messaging().SetRoomRetention(ctx, c, "direct", 0); err != nil {
		t.Fatalf("direct peer refused: %v", err)
	}
}

func TestShorteningRetentionPurgesImmediately(t *testing.T) {
	ctx, st, db, driver := retentionDB(t)
	a, b := purgeRoomFixture(t, st, "shorten", 0)
	age(t, db, driver, "shorten", 2*86400)
	if err := st.Messaging().SetRoomRetention(ctx, a, "shorten", 1); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, driver, `SELECT COUNT(*) FROM messaging_events WHERE room_id = ?`, "shorten"); got != 0 {
		t.Fatalf("%d events survived shortening", got)
	}
	if got := count(t, db, driver, `SELECT retained_from FROM messaging_rooms WHERE id = ?`, "shorten"); got != 4 {
		t.Fatalf("floor %d, want 4", got)
	}
	if _, err := st.Messaging().ReadEvents(ctx, b, "shorten", 0); !errors.Is(err, store.ErrMessagingHistoryGone) {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run them and see them fail.** Run `go test ./internal/store/ -run 'SetRoomRetention|ShorteningRetention' -v`. Expected: FAIL, because `SetRoomRetention` is undefined.

- [ ] **Step 3: Implement.** In `messaging.go`:

```go
var messagingRetentionChoices = map[int64]bool{0: true, 1: true, 7: true, 30: true, 90: true}

// SetRoomRetention changes how long the server keeps a room's events. The current-generation
// owner may change it; in a direct room so may the peer. Shortening purges at once.
func (m *messagingStore) SetRoomRetention(ctx context.Context, actor MessagingActor, room string, days int64) error {
	if !messagingRetentionChoices[days] {
		return ErrMessagingConflict
	}
	return m.transaction(ctx, actor, true, func(tx *sql.Tx, _ string) error {
		if err := m.lockDeliveryRoom(ctx, tx, room); err != nil {
			return err
		}
		var member int
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT COUNT(*) FROM messaging_members WHERE room_id = ? AND user_id = ? AND status = 'active' AND identity_generation = (SELECT generation FROM messaging_identities WHERE user_id = ?)`), room, actor.UserID, actor.UserID).Scan(&member); err != nil {
			return err
		}
		if member != 1 {
			return ErrNotFound
		}
		var peer string
		if err := tx.QueryRowContext(ctx, m.store.rebind(`SELECT direct_peer_id FROM messaging_rooms WHERE id = ?`), room).Scan(&peer); err != nil {
			return err
		}
		if peer != actor.UserID {
			if err := m.ownRoom(ctx, tx, actor, room); err != nil {
				return ErrMessagingDenied
			}
		}
		if _, err := tx.ExecContext(ctx, m.store.rebind(`UPDATE messaging_rooms SET retention_days = ? WHERE id = ?`), days, room); err != nil {
			return err
		}
		if err := m.purgeRoom(ctx, tx, room, time.Now().Unix()); err != nil {
			return err
		}
		return m.audit(ctx, tx, actor, "messaging.retention_changed", room, fmt.Sprintf("retention_days=%d", days))
	})
}
```

Add `SetRoomRetention(ctx context.Context, actor MessagingActor, room string, days int64) error` to the `MessagingStore` interface in `store.go`.

- [ ] **Step 4: Run the tests and see them pass,** on SQLite and on Postgres.

- [ ] **Step 5: Commit.**

```bash
git add internal/store && git commit -m "messaging: owners (or direct peers) change room retention"
```

---

### Task 3: HTTP API for retention

**Files:**
- Modify: `internal/api/messaging_handlers.go`:
  - `handleMessagingCreateRoom` (~246-275): pointer default 90, the new choices.
  - `messagingRoutes`: add the `PATCH` route.
  - Add a new handler `handleMessagingSetRetention`.
- Modify: `internal/api/messaging_delivery.go:103`: omit `expires_at` when 0.
- Test: `internal/api/messaging_retention_test.go`.
- Docs:
  - `docs/MESSAGING-API.md`: the route table (~line 173) and "Ciphertext retention" (~374-395).
  - `internal/api/AGENTS.md`: the room-creation bullet.

**Interfaces:**
- Consumes: `store.MessagingStore.SetRoomRetention` (Task 2).
- Produces: `PATCH /api/messaging/rooms/{room}` taking `{"retention_days": n}` and returning 200 `{"retention_days": n}`. Errors: 400 for a value outside the choices, 403 not owner or peer, 404 not a member.

- [ ] **Step 1: Write the failing API tests.**

```go
func TestRoomRetentionAPI(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	a := messagingLogin(t, st, "alice")
	ad := verifyEnrollment(t, srv, a, requestEnrollment(t, srv, a))
	// Omitted retention -> 90.
	w := messagingRequest(t, srv, "POST", "/api/messaging/rooms", a, ad.Token, map[string]any{"name": "Default"})
	messagingCode(t, w, 201)
	if !strings.Contains(w.Body.String(), `"retention_days":90`) { t.Fatal(w.Body.String()) }
	// Explicit 0 -> Off, not the default.
	w = messagingRequest(t, srv, "POST", "/api/messaging/rooms", a, ad.Token, map[string]any{"name": "Keep", "retention_days": 0})
	messagingCode(t, w, 201)
	if !strings.Contains(w.Body.String(), `"retention_days":0`) { t.Fatal(w.Body.String()) }
	var room struct{ ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &room)
	messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/rooms", a, ad.Token, map[string]any{"name": "Bad", "retention_days": 14}), 400)
	// Owner changes it.
	w = messagingRequest(t, srv, "PATCH", "/api/messaging/rooms/"+room.ID, a, ad.Token, map[string]any{"retention_days": 7})
	messagingCode(t, w, 200)
	messagingCode(t, messagingRequest(t, srv, "PATCH", "/api/messaging/rooms/"+room.ID, a, ad.Token, map[string]any{"retention_days": 2}), 400)
	// Non-member.
	b := messagingLogin(t, st, "bob")
	bd := verifyEnrollment(t, srv, b, requestEnrollment(t, srv, b))
	messagingCode(t, messagingRequest(t, srv, "PATCH", "/api/messaging/rooms/"+room.ID, b, bd.Token, map[string]any{"retention_days": 0}), 404)
}
```

Also add an assertion to the existing events test in this file: with retention Off, event JSON has no `expires_at` key; with 1 day it equals `created_at + 86400`.

- [ ] **Step 2: Run and see it fail.** Run `go test ./internal/api/ -run TestRoomRetentionAPI -v`. Expected: FAIL (`retention_days` is 30, and the PATCH route returns 404).

- [ ] **Step 3: Implement.** In `handleMessagingCreateRoom`, change the field to `RetentionDays *int64 \`json:"retention_days"\`` and replace the default and validation block with:

```go
	days := int64(90)
	if request.RetentionDays != nil {
		days = *request.RetentionDays
	}
	if !messagingRetention(days) {
		s.writeError(w, http.StatusBadRequest, "Retention must be 0 (off), 1, 7, 30 or 90 days")
		return
	}
```

using `RetentionDays: days` in the `store.MessagingRoom`. Add:

```go
func messagingRetention(days int64) bool {
	return days == 0 || days == 1 || days == 7 || days == 30 || days == 90
}

func (s *Server) handleMessagingSetRetention(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	var request struct {
		RetentionDays *int64 `json:"retention_days"`
	}
	if !s.messagingJSON(w, r, &request) {
		return
	}
	if request.RetentionDays == nil || !messagingRetention(*request.RetentionDays) {
		s.writeError(w, http.StatusBadRequest, "Retention must be 0 (off), 1, 7, 30 or 90 days")
		return
	}
	room := r.PathValue("room")
	if err := s.store.Messaging().SetRoomRetention(r.Context(), actor, room, *request.RetentionDays); err != nil {
		s.messagingError(w, err)
		return
	}
	s.wakeMessaging(room)
	s.writeJSON(w, http.StatusOK, map[string]any{"retention_days": *request.RetentionDays})
}
```

Register it in `messagingRoutes` next to the other room routes:

```go
	s.mux.HandleFunc("PATCH /api/messaging/rooms/{room}", s.requireMessaging(s.handleMessagingSetRetention))
```

Confirm that `messagingError` maps `ErrMessagingDenied` to 403 and `ErrNotFound` to 404, by reading it; add the mapping if it's missing. In `messaging_delivery.go:103`, build the event map, then set `"expires_at"` only when `e.ExpiresAt > 0`.

- [ ] **Step 4: Run and see it pass.** Run `go test ./internal/api/ -v -run 'Retention|Delivery'`. Expected: PASS.

- [ ] **Step 5: Update the docs.**
  - In `docs/MESSAGING-API.md`, add a row to the route table:
    `| PATCH /rooms/{room} | {retention_days} → {retention_days} | Approved device; owner (current generation) or direct peer |`
  - Rewrite "Ciphertext retention":
    - Retention is 0 (Off), 1, 7, 30 or 90 days, default 90. Omitted means 90; 0 means Off.
    - The owner or direct peer can change it at any time. Shortening purges at once.
    - A purge deletes event rows, Welcomes, retry receipts and `messaging.event_accepted` audit rows, as a prefix. `retained_from` stays the floor.
    - `expires_at` is `created_at + window` and is absent when Off.
    - Retries of purged events append again, so clients drop pending sends older than the window.
    - Room quotas are 100,000 active events and 512 MiB.
  - Update the retention paragraphs in `docs/PRODUCT.md` (~222-240) the same way.
  - Update the room-creation bullet in `internal/api/AGENTS.md`.

- [ ] **Step 6: Commit.**

```bash
git add internal/api docs && git commit -m "api: room retention Off/1/7/30/90 (default 90) and PATCH to change it"
```

---

### Task 4: Isolated client (mls-proof)

**Files:**
- Modify: `mls-proof/src/delivery-wire.ts:22-25` (`retentionDays`), `:91` (pending gains `createdAt`).
- Modify: `mls-proof/src/delivery.ts`:
  - `createRoom` and `directRoom` default 90.
  - Local expiry (~539).
  - `submit` (~510).
  - Add a new `setRetention`.
- Modify: `mls-proof/src/chat.ts`:
  - ~288, the retention text.
  - ~447-448, the forms.
  - Add a new retention-change form handler.
- Modify: `mls-proof/chat.html:43`: the options. Add a change form under `#retention-state`.
- Test: `mls-proof/tests/chat.spec.ts` (~637).

**Interfaces:**
- Consumes: `PATCH /api/messaging/rooms/{room}` (Task 3), and events without `expires_at` when Off.
- Produces: `delivery.setRetention(days: number): Promise<void>`.

- [ ] **Step 1: Write the failing browser assertions.** In `tests/chat.spec.ts`, where the test at ~637 checks `#retention-state` for "24 hours", add:
  - After creating a room with default retention, `#retention-state` contains `90 days`.
  - The owner selects `Off` in `#room-retention` and submits `#retention-form`. `#retention-state` then contains `off`, and a member browser sees the same after refresh.
  - Keep the existing 1-day expiry flow. It uses the `expire-room` fixture, which now backdates `created_at`.

- [ ] **Step 2: Run and see it fail.** Run `cd mls-proof && npx playwright test tests/chat.spec.ts -g retention`. Expected: FAIL.

- [ ] **Step 3: Implement.** In `delivery-wire.ts`:

```ts
export type RetentionDays = 0 | 1 | 7 | 30 | 90;
export function retentionDays(value: unknown = 90): RetentionDays {
  if (value !== 0 && value !== 1 && value !== 7 && value !== 30 && value !== 90) throw new Error('Expected 0 (off), 1, 7, 30 or 90 retention days');
  return value;
}
```

In `connection()`, change the pending parse to also read `createdAt: p.createdAt === undefined ? null : integer(p.createdAt)`. Where `d.pending` is assigned in `delivery.ts` (~493 and ~506), add `createdAt: Math.floor(Date.now()/1000)`.

In `delivery.ts` `submit`, before sending:

```ts
      // The server forgets purged events, so an older retry would append a duplicate.
      if (d.retentionDays > 0 && d.pending.createdAt !== null && d.pending.createdAt < Math.floor(Date.now()/1000) - d.retentionDays*86400) {
        d.pending = null;
        throw new Error('This message is older than the conversation keeps messages and was not sent');
      }
```

For local expiry at ~539:

```ts
        const local = d.retentionDays === 0 ? Infinity : Math.floor(Date.now()/1000) + d.retentionDays*86400;
        const bound = Math.min(e.expiresAt ?? Infinity, local);
        const expiresAt = bound === Infinity ? null : bound;
```

Update the comparisons that follow (`expiresAt > Date.now()/1000`) to `expiresAt === null || expiresAt > Date.now()/1000`. Change the `createRoom` and `directRoom` default parameters from 30 to 90. Add:

```ts
  async setRetention(days: number) {
    await transaction(async (_r,d) => {
      if (!d.room) throw new Error('Open a conversation first');
      await api('/rooms/' + encodeURIComponent(d.room),d.token,'PATCH',{retention_days:retentionDays(days)});
      d.retentionDays = retentionDays(days);
    });
  },
```

In `chat.html`, change the select options to `Off` (0), `24 hours` (1), `7 days` (7), `30 days` (30) and `90 days` (90), with 90 `selected`. Below `#retention-state` add:

```html
<form id="retention-form" class="inline"><label>Keep messages <select id="room-retention"><option value="0">Off (keep)</option><option value="1">24 hours</option><option value="7">7 days</option><option value="30">30 days</option><option value="90">90 days</option></select></label><button type="submit">Change</button></form>
```

In `chat.ts` ~288:

```ts
  element('retention-state').textContent = s.room ? (s.retentionDays === 0 ? 'Server retention: off. Messages stay until the owner turns purging on.' : `Server retention: ${s.retentionDays === 1 ? '24 hours' : s.retentionDays + ' days'}. Older messages are deleted from the server; downloaded copies and backups may outlive them.`) : '';
```

and register:

```ts
form('retention-form',async () => { await delivery.setRetention(Number(field('room-retention').value)); await refresh(); },'Retention changed.');
```

- [ ] **Step 4: Build and run the client suites.**

Run: `cd mls-proof && npm run build && npx playwright test && npx playwright test -c playwright.oidc.config.mjs`
Expected: PASS on Chromium and Firefox. WebKit may not launch on hosts missing its system libraries; CI runs Chromium and Firefox.

- [ ] **Step 5: Update `mls-proof/AGENTS.md`** for retention choices, the change form and the pending-send expiry, then commit:

```bash
git add mls-proof && git commit -m "mls-proof: retention Off/90, owner change form, drop stale pending sends"
```

---

### Task 5: Operator console and full verification

**Files:**
- Modify: `web/src/pages/MessagingUsage.tsx`, only if it hard-codes the old limits. It reads the limits from `/api/admin/messaging/usage`, so check and change nothing if it doesn't.
- Rebuild: `web/dist` (committed; CI diffs it).

- [ ] **Step 1:** `grep -n "4096\|32 MiB\|33554432" web/src` finds nothing. If it finds something, replace it with the values from the API.
- [ ] **Step 2:** Run `make ci`. Expected: `==> Local CI checks passed`.
- [ ] **Step 3:** Run `KY_TEST_POSTGRES_DSN=... go test -count=1 ./...` on a disposable Postgres 17. Expected: all `ok`.
- [ ] **Step 4:** Run `python3 scripts/backup-acceptance.py`. Expected: all PASS lines.
- [ ] **Step 5:** Do the DOX pass: root `AGENTS.md` (the maintenance-loop sentence says "sweeps expired ciphertext"; make it "purges events past each room's retention"), `internal/store/AGENTS.md` (migration 17 and purge semantics), `internal/backup/AGENTS.md` (the snapshot notes about blanked payloads still hold). Commit:

```bash
git add -A && git commit -m "docs: thread auto-purge contracts"
```
