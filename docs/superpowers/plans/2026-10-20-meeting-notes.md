# Meeting notes bot

Reopens D7 of `2026-10-14-agent-accounts.md` for one server-owned account. Spike evidence:
branch `spike/meeting-bot` (`scripts/meeting-bot-spike/README.md`): the bot receives Element
Call's media keys over encrypted to-device, decrypts participants, and Element decrypts the bot;
a withheld key yields nothing intelligible.

## Owner decisions (Yoshi, 2026-10-20)

- The bot joins a meeting only when invited by the room's creator or a room admin.
- On joining it speaks (Kokoro) that the meeting is recorded for note taking and posts the same
  notice in the room. When someone joins later it says so again, at most once a minute.
- Anyone in the call can pause it, resume it or make it leave.
- Voice first. Shared-screen snapshots later, as a separate opt-in. Never full video or webcams.

## Design decisions (made here; reversible)

- **D1 — One account, off by default.** `@kymessages-notes:<server>`, created and checked like
  `@kymessages-console` (unlinked, unlocked, not a MAS admin). Settings > Meeting notes turns it
  on (fresh sign-in, audited). Off locks the MAS user and revokes its session; on unlocks it.
- **D2 — One invite, one meeting.** After the call it was invited to ends, the bot posts the
  notes and leaves the room. Invited with no call, it waits up to 12 hours, then leaves. Each
  meeting needs a new invite, so an old invite never records a later meeting.
- **D3 — Inviter rule, checked twice.** The inviter is the room creator (`m.room.create`
  sender, or `additional_creators`) or has power level 100. The bot refuses other invites. The
  server independently checks the rule, from room state, before every call grant and on every
  call sweep; failing it refuses or evicts the bot.
- **D4 — Server-issued grant.** The bot requests its LiveKit grant from KyMessages like Element
  Call does (`/sfu/get`, OpenID). `activeCallUsers` admits the notes account only while the
  feature is on and its MAS user is unlocked; the room-membership check stays.
- **D5 — Session delivery.** `matrix-init` writes `secrets/notes_api_secret`. The bot calls
  `POST /api/notes/session` with it to get a personal session for its device (no expiry; the
  previous session is revoked). 403 while the feature is off. Nobody copies a token.
- **D6 — Nothing at rest.** Audio is transcribed in memory and dropped. The transcript lives in
  memory until posted as encrypted `m.notice` messages to the room. The bot's only disk state
  is its crypto store (device keys). It is not in backups: after a restore the bot gets a new
  device.
- **D7 — Local speech, offline.** Kokoro-82M (q8) and Whisper base.en (q8) are baked into the
  image at build time, pinned by revision and checksum; the container never downloads. English
  only.
- **D8 — Commands.** `!notes pause`, `!notes resume`, `!notes leave`, from a member who is in
  the call; each is acknowledged by voice and in chat. Paused audio is dropped; the notes mark
  the gap.
- **D9 — Separate service and image.** `notes/` (Node), image
  `ghcr.io/busnes-app/kymessage-notes`, Compose overlay `docker-compose.notes.yml`. The overlay
  plus the console switch are both needed; the console shows whether the bot has checked in.
- **D10 — Speaker labels.** Each participant's track is transcribed separately and labelled
  with their room display name.

## Tasks

0. Gate: prove the inviter is readable from Synapse admin room state (member event
   `unsigned.prev_sender`/`replaces_state`), else redesign D3.
1. Server: notes account (ensure/lock/unlock), setting + routes + audit, `/api/notes/session`,
   `matrix-init` secret, inviter check (`synapseadmin` room state), `activeCallUsers` and the
   sweep, MAS sweep skip. Go tests for each.
2. Bot (`notes/`): Matrix client and crypto store, invite rule, MatrixRTC join and keys, LiveKit
   with the rtc-node `setKey` workaround, notice throttle, per-speaker VAD and Whisper, commands,
   notes posting, leave rules. `node:test` for the pure parts.
3. Image and Compose: Dockerfile (non-root, read-only root, models baked), overlay, build
   overlay, release workflow publishes and attests the image.
4. Console: Settings > Meeting notes (switch, bot status).
5. Acceptance stage `notes`: the bot container on the project network, owner invites, call,
   both directions heard, notice in chat, pause/resume, notes posted with the owner's sentence,
   bot leaves; a member's invite refused; disabled refuses the session.
6. Docs: PRODUCT.md (replaces "No call recording"), AGENTS.md files, README.

## Risks

- The bot sees meeting audio and the transcript in plaintext, like an agent. Its host is the
  server; nothing leaves it.
- rtc-node's `setKey` workaround uses an internal module; pinned version, acceptance-gated.
- Element Call's Compatibility mode is assumed; Matrix 2.0 identities break key mapping.
- Federated participants publish on their own SFU; the bot hears only this server's SFU.
