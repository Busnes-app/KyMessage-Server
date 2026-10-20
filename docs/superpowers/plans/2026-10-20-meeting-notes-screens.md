# Meeting notes: shared-screen snapshots

Follows `2026-10-20-meeting-notes.md`. Owner decision (Yoshi, 2026-10-20): voice first, then
shared-screen snapshots as a separate opt-in; never full video or webcams.

## Decisions (made here; reversible)

- **S1 — Two opt-ins.** The console switch "Shared-screen snapshots" (off; needs meeting notes
  on; turning it on needs a recent sign-in and shows what it does). Then per meeting, the room's
  creator or an admin types `!notes screens on`; each meeting starts with it off. Anyone in the
  call can type `!notes screens off`.
- **S2 — Screens only.** The bot subscribes to screen-share tracks only while both opt-ins are
  on, and to no camera track ever. Turning either off unsubscribes at once.
- **S3 — Said out loud.** Turning it on is announced by voice and in chat; the join notice then
  says "including shared screens". When someone starts sharing, the bot says whose screen it is
  capturing, by voice and in chat.
- **S4 — Snapshots, not video.** About one frame a second is looked at, shrunk to a 32×18
  thumbnail. A snapshot is taken when the picture differs from the last snapshot and has held
  still for 2 seconds; at most one per 10 seconds, 60 per meeting. JPEG, longest side 1600 px.
  Nothing while paused.
- **S5 — Same lifetime as the transcript.** Snapshots stay in the bot's memory and are posted
  after the notes as encrypted `m.image` events (Matrix attachment encryption, AES-256-CTR), each
  named with its time and whose screen it was. Then they live in the room's encrypted media like
  any shared image. Never written to the bot's disk.
- **S6 — The server tells the bot the switch.** `GET /api/notes/config` (notes secret) answers
  `{screens}`; the bot reads it when the command is given and when a share starts.

## Tasks

1. Server: `notes_screens` setting, enable (fresh) / disable (one click) routes, audit,
   `/api/notes/config`. Go tests.
2. Bot: thumbnail and still-picture rules (pure, tested), screen subscription by source, the
   commands and announcements, JPEG via sharp from raw RGBA, attachment encryption (tested
   against a decryption), posting.
3. Console: the second switch in Settings > Meeting notes.
4. Acceptance: the owner shares a screen (Chromium's fake capture); the bot announces it and
   posts at least one encrypted snapshot that Element decrypts and shows; with the command off,
   none.
5. Docs: PRODUCT.md, MEETING-NOTES.md, AGENTS.md.
