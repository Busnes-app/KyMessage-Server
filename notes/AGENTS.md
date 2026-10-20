# Meeting-notes bot

## Purpose
The `@kymessages-notes` bot: joins an invited room's next Element Call, announces the
recording, transcribes each speaker locally, posts the notes and leaves. Plan:
`docs/superpowers/plans/2026-10-20-meeting-notes.md`; operator guide: `docs/MEETING-NOTES.md`.

## Ownership
Owns `src/`, `test/`, `Dockerfile`, `models.txt`, `fetch-models.sh`. The server side (account,
session route, call-grant rules, sweep) is `internal/api` (notes rules); Compose is
`docker-compose.notes.yml` (+ `docker-compose.notes-build.yml`).

## Local Contracts
- Session: `POST <NOTES_APP_URL>/api/notes/session` with the notes secret, sending its stored
  device ID only when it still has that device's crypto store; a different device back means a
  new device, so the store is wiped. 403 (off) is retried every minute. The token lives only in
  memory; a revoked session (`Session.logged_out`) ends every meeting without notes.
- Invites: accepted only when the inviter (the invite event's sender) is the room's creator or
  power level 100 (`rules.roomAdmin`, the same rule as `synapseadmin.AdminInvite`), the room is
  encrypted and fewer than `NOTES_MAX_MEETINGS` (2) meetings run; otherwise a notice and leave.
  One invite, one meeting: it waits up to `NOTES_WAIT_HOURS` (12) for a call, records it and
  leaves; a restart leaves every joined room. It never changes its profile while in a room
  except the one display name set at start (`Meeting notes`).
- Calls: MatrixRTC `join` with `manageMediaKeys` and the transport
  `{livekit_service_url: NOTES_RTC_SERVICE_URL, livekit_alias: room}` (compatibility mode, so
  identities are `@user:server:DEVICE`); the grant comes from KyMessages' `/sfu/get`; LiveKit
  is reached at `NOTES_LIVEKIT_URL`. `autoSubscribe` is off: it subscribes to audio tracks only,
  so camera (and screen) frames never reach it; it logs each kind it takes or skips. E2EE: GCM, HKDF, salt `LKFrameEncryptionKey`, ratchet
  window 10, key ring 256 (Element Call's MatrixKeyProvider). Keys are set with a hand-built FFI
  `SetKeyRequest`: rtc-node 1.1.0's `KeyProvider.setKey` drops the key. Re-check this on any
  `@livekit/rtc-node` bump.
- Notice: spoken (Kokoro) and posted on joining; spoken again for each later participant, at most
  once per `noticeIntervalMs` (60 s, `rules.Throttle`), deferred rather than dropped.
- Commands (`rules.parseCommand`): `!notes pause|resume|leave|stop|help` from a member with a
  call membership; before the call, `leave` from any member. Each is acknowledged by voice and
  in chat. Paused audio is dropped and the gap marked.
- Notes: one `rules.Segmenter` per speaker, Whisper one utterance at a time across meetings;
  Whisper's silence outputs are dropped. Posted as `m.notice`, split at 30 000 characters. The
  meeting ends 30 s after the bot is alone, on `!notes leave` or on losing the call.
- Nothing at rest but the crypto store (`/data`, 0600, written atomically every 30 s and on
  SIGTERM). Logs carry room IDs and events, never transcript text or tokens.
- Speech is offline: `env.allowRemoteModels = false`, models from `NOTES_MODELS_DIR`. The image
  fetches exactly `models.txt` (revision-pinned, SHA-256 checked) at build time and keeps only
  its own architecture's ONNX Runtime. `sharp` is overridden to a patched release; the bot never
  decodes images.

## Verification
- `npm ci --ignore-scripts && npm test` (rules). With models
  (`sh fetch-models.sh models.txt out/models`): `NOTES_MODELS_DIR=out/models node --test test/speech.test.mjs`.
- CI job "Meeting-notes bot (tests & image)": unit tests, audit, image build, offline
  speak-and-hear in the image.
- `scripts/matrix-acceptance.sh` step `notes` (see root AGENTS.md).
