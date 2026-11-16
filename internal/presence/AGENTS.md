# internal/presence

## Purpose
The one definition of the suite status contract ([docs/PRESENCE.md](../../docs/PRESENCE.md)):
event types, limits, strict body decoding, and `Status`, which works a member's coarse state
out from stored inputs at read time.

## Local Contracts
- Every body is decoded strictly (`DisallowUnknownFields`, exactly one object): a title,
  attendees or reply text is `ErrBadBody`. Keep it strict; a new field is a contract change on
  both sides.
- `DecodeBusy(body, now)`: `revision > 0`, `busy` required, at most `MaxBusy` (200) blocks at
  offset zero, `start < end`, `end <= now + BusyAhead` (49 h); ended blocks dropped, the rest
  sorted by start.
- `DecodeOOO`: `away:false` ignores the period; `away:true` needs `start < end`, at most
  `MaxOOO` (366 days).
- `DecodeMe(body, now)`: `manual` absent leaves it, `null` clears, else `state` in
  `available|busy|away` and `until` in `(now, now + MaxManual]` (7 days). `share:false` with a
  manual status is `ErrBadBody`.
- `DecodeLookup`: `subs` required, `ErrTooMany` over `MaxLookup` (50).
- `Status`: unknown (no row, or sharing off) → unexpired manual → out-of-office in
  `[start, end)` → in a busy block (overlapping or touching blocks merge; `until` their end) →
  available if the calendar push is under `Fresh` (24 h) old → unknown. `until` only for
  `in_a_meeting` and `out_of_office`.
- No last-active time, title or name ever enters this package.

## Verification
- `go test -race ./internal/presence/`
