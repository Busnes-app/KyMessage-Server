# Meeting notes

A bot that takes notes in calls. Members invite it to a room before a meeting; it joins that
room's next call, says out loud and in the chat that the meeting is recorded for note taking,
transcribes each speaker on this server, posts the notes in the room and leaves.

Off by default. Design and decisions:
[the plan](superpowers/plans/2026-10-20-meeting-notes.md).

## What members see

1. Someone who is the room's creator or an admin (power level 100) invites
   `@kymessages-notes:<server name>`. Anyone else's invite is refused in the room, and the bot
   leaves. It works only in encrypted rooms.
2. The bot says it will join the next call. If no call starts within 12 hours it leaves.
   `!notes leave` cancels it.
3. When the call starts, the bot joins it and says "This meeting is being recorded for note
   taking." It also posts that notice, with the commands, in the chat. When someone joins later,
   it says the sentence again, at most once a minute.
4. Anyone in the call can type in the room's chat:
   - `!notes pause`: stop transcribing (audio is dropped, the notes mark the gap)
   - `!notes resume`: continue
   - `!notes leave` (or `stop`): end now; the notes are posted
   - `!notes help`: list the commands
5. When the call ends (nobody else in it for 30 seconds), the bot posts the notes, one line per
   utterance with the time and the speaker's display name, and leaves the room. The next meeting
   needs a new invite.

English only (Whisper base.en). Speakers are told apart by their call connection, not by voice.

## Shared-screen snapshots

Off twice over: an admin must allow them in Settings > Meeting notes, and each meeting starts
without them.

- In the call, the room's creator or an admin types `!notes screens on`. The bot says, out loud
  and in the chat, that shared screens will be captured. Anyone in the call can type
  `!notes screens off`.
- Turning snapshots off in Settings also stops them in meetings already running, within 10
  seconds; snapshots taken before stay.
- When someone shares a screen, the bot says whose screen it is capturing. It looks at shared
  screens only, never at cameras.
- It keeps a still picture when the shared screen changes and then holds still for 2 seconds
  (a new slide, say): at most one every 10 seconds, 60 per meeting, nothing while paused. A
  playing video never holds still, so it is not captured.
- After the notes it posts the snapshots, encrypted, as images in the room; each line of the
  notes says when it was taken and whose screen it was. Like the transcript, they stay in the
  bot's memory until then and are never written to its disk.

## What stays on the server

- Audio is transcribed in the bot's memory and dropped. Nothing is written to disk but the bot's
  encryption keys (`notes-data` volume).
- The transcript stays in the bot's memory until it posts the notes, encrypted, to the room.
  Like an agent, the bot sees the meeting in plaintext; unlike a hosted service, nothing leaves
  this server. The speech models are in the image; the bot never downloads anything.
- The bot restarting, being removed from the room, or the feature being turned off ends the
  meeting without notes.
- Every session the bot gets and every call it joins is audited (Audit, kind Matrix:
  `notes.session`, `notes.call`, with the inviter).

## Set up

On a stack set up before meeting notes, run `matrix-init` once more: it adds
`matrix/secrets/notes_api_secret` and keeps every other file.

1. Append the overlay to `COMPOSE_FILE` in `.env`, after `docker-compose.matrix.yml`:
   `:docker-compose.notes.yml` (a source install also appends `:docker-compose.notes-build.yml`).
2. `docker compose up -d`. The app reads the new secret on start; the bot waits while the
   feature is off.
3. In the console, Settings > Meeting notes: Turn on (needs a recent sign-in). The panel shows
   when the bot last checked in.

`KY_NOTES_TZ` (default `UTC`) sets the time zone of the times in the notes. The bot loads
both speech models into memory for its first meeting and runs them on the CPU, one utterance
at a time; it takes up to two meetings at once.

Turning it off revokes the bot's session at once: it leaves any call within 5 seconds and posts
nothing more.

## Limits

- Element Call's compatibility mode only (the pinned Element's default).
- Participants from other servers are not heard: their media goes through their own server.
- Voice and still snapshots of shared screens only; never video or webcams.
