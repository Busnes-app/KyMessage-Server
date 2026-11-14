# internal/kycalendar

## Purpose
The one definition of what KyCalendar sends KyMessages about meeting rooms, and the live pair
it is verified under. The contract is [docs/KYCALENDAR.md](../../docs/KYCALENDAR.md);
KyCalendar implements the other side.

## Local Contracts
- **Event types**, one per route: `kycalendar.meeting.put` (`PUT /api/meetings/{id}`) and
  `kycalendar.meeting.close` (`DELETE`). `Verify` refuses any other type as a bad signature
  before checking the MAC, so a request is never accepted on the other route.
- **`Verify`** reads at most `MaxBody` (64 KiB), then `syncauth.Verify` with the caller's replay
  guard. A verified replay is `replayed` with no error; an error from the guard itself is
  returned as the guard wrapped it, so the caller can answer 503 rather than 401.
- **`DecodePut`**: `sub`, `issuer` and `title` are 1 to 255 (issuer 512) bytes of UTF-8 without
  control characters, at most `MaxMembers` (100) `member_subs`, each one the same rule. A
  non-empty `guests` is `ErrGuests`; unknown fields (`windows`) are ignored.
- **`ValidID`**: `[A-Za-z0-9_-]{16,64}`.
- **`Link`** holds a `Peer` (https origin, secret, source `environment` or `kyidentity`) swapped
  atomically, nil while unpaired. The secret lives only there.

## Verification
- `go test -race ./internal/kycalendar/`
