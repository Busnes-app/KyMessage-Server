# Evidence: KyCalendar meeting rooms (member half), 2026-11-14

Branch `feat/meeting-rooms`, PR #127. Contract: [../KYCALENDAR.md](../KYCALENDAR.md).

## Running stack (CI `matrix-acceptance`, first leg)

Run at head `10a465c` on GitHub Actions (runner, not Major: the stack does not fit Major's
shared 4 GB test budget alongside its load test). Step `meetings`, after `prove`:

- A PUT signed in bash with the harness pair secret answered 200; carol (assigned in
  KyIdentity, never signed in to chat) came back in `skipped_subs`.
- Synapse's current state for the room: `m.room.encryption` `m.megolm.v1.aes-sha2`, join rule
  `invite`, history `joined`, guest access `forbidden`; `m.room.create` sent by
  `@kymessages-console`; power-level `users` exactly `{"@alice.q_ky:kymatrix.test":50}`.
- alice and bob were `invite`, accepted in Element (both clients report the room encrypted),
  then each read the other's message: "kymatrix-meeting-alice-… decrypted and shown",
  "kymatrix-meeting-bob-… decrypted and shown" (`stack/meeting-alice.png`).
- **Finding: no call button.** In the same run Element showed no Video call button in the
  room (`stack/meeting-alice.png`, header). Element v1.12.30's `useRoomCall` hides both call
  buttons when `memberCount > 2 && !UIFeature.widgets`; `matrix-init` sets
  `UIFeature.widgets: false`, and the console is the third member. The existing `prove` group
  room has two members, which is why the suite never met this. Owner decision recorded in
  `KYCALENDAR.md` "Calls".
- An earlier run at `546506d` failed on bob not seeing alice's message, sent while he was
  only invited: with history visibility `joined` that is correct. The step now has both join
  first.

The console never needs to read content: it holds no device keys (the step asserts
`e2e_device_keys_json` has no row for it), so no member's client can share a room key with it,
and it calls only `createRoom`, `invite`, `kick`, `m.room.name`/`m.room.power_levels` state
and the admin state, close and rate-limit APIs.

## Console (throwaway server, Matrix configured, MAS unreachable)

Settings → Server before (`origin/master`) and after (this branch), 1280 and 390 px, light and
dark: `2026-11-meeting-rooms/before/`, `2026-11-meeting-rooms/after/`. The KyCalendar row sits
after KyIdentity sync. The sync and federation errors around it come from the throwaway
server having no MAS or Synapse behind it.
