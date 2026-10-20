// KyMessages meeting-notes bot. Gets its session from KyMessages with the notes secret, joins
// rooms only on an invite from the room's creator or an admin, records the room's next call for
// note taking, posts the notes and leaves. Plan: docs/superpowers/plans/2026-10-20-meeting-notes.md.
import 'fake-indexeddb/auto';
import fs from 'node:fs';
import { ClientEvent, createClient, HttpApiEvent, MatrixEventEvent, RoomEvent } from 'matrix-js-sdk';
import { MatrixRTCSessionEvent } from 'matrix-js-sdk/lib/matrixrtc/index.js';
import { roomAdmin, parseCommand } from './rules.mjs';
import { load, save, wipe } from './store.mjs';
import { Meeting } from './meeting.mjs';

const env = process.env;
const cfg = {
  appUrl: env.NOTES_APP_URL ?? 'http://app:8080',
  matrixUrl: env.NOTES_MATRIX_URL ?? 'http://synapse:8008',
  livekitUrl: env.NOTES_LIVEKIT_URL ?? 'ws://livekit:7880',
  // The transport Element Call members publish: KyMessages' public call authorizer.
  serviceUrl: env.NOTES_RTC_SERVICE_URL,
  secret: fs.readFileSync(env.NOTES_SECRET_FILE ?? '/run/secrets/notes_api_secret', 'utf8').trim(),
  dataDir: env.NOTES_DATA_DIR ?? '/data',
  timeZone: env.TZ || 'UTC',
  waitMs: Number(env.NOTES_WAIT_HOURS ?? 12) * 3600_000,
  noticeIntervalMs: 60_000,
  maxMeetings: Number(env.NOTES_MAX_MEETINGS ?? 2),
};
if (!cfg.serviceUrl?.startsWith('https://')) throw new Error('NOTES_RTC_SERVICE_URL must be the https call authorizer URL');
const storeFile = `${cfg.dataDir}/crypto.v8`;
const deviceFile = `${cfg.dataDir}/device`;
const log = (m) => console.error(`[notes] ${m}`);
// matrix-js-sdk is chatty on stdout; keep only our lines.
console.log = console.info = console.debug = () => {};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let flush = async () => {}; // saves the running session's crypto store
const until = async (fn, ms = 30_000) => { for (let t = 0; t < ms && !fn(); t += 250) await sleep(250); return fn(); };

// session asks KyMessages for a token on our device ('' for a new one).
async function session(device) {
  const r = await fetch(`${cfg.appUrl}/api/notes/session`, { method: 'POST',
    headers: { Authorization: `Bearer ${cfg.secret}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ device_id: device }) });
  const body = await r.json().catch(() => ({}));
  if (!r.ok) throw Object.assign(new Error(body.error ?? `HTTP ${r.status}`), { status: r.status });
  return body;
}

const ev = (e) => e && { sender: e.getSender(), content: e.getContent() };

// run serves one session until its token stops working.
async function run(s) {
  const client = createClient({ baseUrl: cfg.matrixUrl, accessToken: s.access_token, userId: s.user_id, deviceId: s.device_id });
  await client.initRustCrypto({ cryptoDatabasePrefix: 'notes' });
  const meetings = new Map(); // room ID → Meeting, or 'armed' while waiting for its call
  const persist = () => save(storeFile).catch((e) => log(`saving the crypto store failed: ${e.message}`));
  const saver = setInterval(persist, 30_000);
  flush = persist;
  const leave = async (room, why) => {
    meetings.delete(room.roomId);
    if (why) await client.sendMessage(room.roomId, { msgtype: 'm.notice', body: why }).catch(() => {});
    await client.leave(room.roomId).catch((e) => log(`leaving ${room.roomId} failed: ${e.message}`));
  };

  // arm waits for the room's next call, then records it (D2: one invite, one meeting).
  const arm = (room) => {
    meetings.set(room.roomId, 'armed');
    const rtc = client.matrixRTC.getRoomSession(room);
    const others = () => rtc.memberships.some((m) => m.sender !== s.user_id);
    const timer = setTimeout(() => meetings.get(room.roomId) === 'armed' && leave(room, 'No call started in this room, so meeting notes are leaving. Invite them again for the next meeting.'), cfg.waitMs);
    const begin = async () => {
      if (meetings.get(room.roomId) !== 'armed' || !others()) return;
      rtc.off(MatrixRTCSessionEvent.MembershipsChanged, begin);
      clearTimeout(timer);
      const m = new Meeting({ client, room, cfg, log, onDone: () => leave(room) });
      meetings.set(room.roomId, m);
      try {
        await m.start();
      } catch (e) {
        log(`joining the call in ${room.roomId} failed: ${e.message}`);
        await m.finish('could not join', { post: false });
        await client.sendMessage(room.roomId, { msgtype: 'm.notice', body: `Meeting notes could not join the call: ${e.message}` }).catch(() => {});
      }
    };
    rtc.on(MatrixRTCSessionEvent.MembershipsChanged, begin);
    begin();
  };

  // invited checks the rule and the room, then joins and arms.
  const invited = async (room) => {
    const inviter = room.getMember(s.user_id)?.events.member?.getSender();
    await client.joinRoom(room.roomId);
    // The joined room's full state arrives with the next sync.
    if (!(await until(() => room.getMyMembership() === 'join' && room.currentState.getStateEvents('m.room.create', '')))) {
      return leave(room, 'Meeting notes could not read this room, so they are leaving.');
    }
    const state = room.currentState;
    if (!roomAdmin(ev(state.getStateEvents('m.room.create', '')), ev(state.getStateEvents('m.room.power_levels', '')), inviter)) {
      return leave(room, "Only the room's creator or an admin can invite meeting notes, so they are leaving.");
    }
    if (!state.getStateEvents('m.room.encryption', '')) return leave(room, 'Meeting notes work only in encrypted rooms, so they are leaving.');
    if ([...meetings.values()].filter((m) => m !== 'armed').length >= cfg.maxMeetings) return leave(room, 'Meeting notes are busy in other meetings. Try again later.');
    log(`invited to ${room.roomId}`);
    await client.sendMessage(room.roomId, { msgtype: 'm.notice', body:
      'Meeting notes will join the next call in this room and record it for note taking, then post the notes here and leave. Type !notes leave to cancel.' });
    arm(room);
  };

  const onMembership = (room, membership) => {
    if (membership === 'invite' && !meetings.has(room.roomId)) {
      meetings.set(room.roomId, 'joining');
      invited(room).catch((e) => { meetings.delete(room.roomId); log(`invite to ${room.roomId} failed: ${e.message}`); });
    } else if (membership === 'leave' || membership === 'ban') {
      const m = meetings.get(room.roomId);
      meetings.delete(room.roomId);
      if (m instanceof Meeting) m.finish('removed from the room', { post: false });
    }
  };

  const since = Date.now();
  client.on(MatrixEventEvent.Decrypted, (e) => {
    if (e.isDecryptionFailure() || e.getType() !== 'm.room.message' || e.getTs() < since || e.getSender() === s.user_id) return;
    const cmd = parseCommand(e.getContent().body);
    const m = meetings.get(e.getRoomId());
    const room = client.getRoom(e.getRoomId());
    if (!cmd || !room) return;
    if (m instanceof Meeting) m.command(cmd, e.getSender()).catch((err) => log(`command failed: ${err.message}`));
    else if (m === 'armed' && cmd === 'leave') leave(room, `Meeting notes were cancelled by ${room.getMember(e.getSender())?.name ?? e.getSender()}.`);
  });

  const ended = new Promise((resolve) => client.once(HttpApiEvent.SessionLoggedOut, resolve));
  await client.startClient({ initialSyncLimit: 1 });
  await new Promise((r) => client.once(ClientEvent.Sync, r));
  if (!(await client.getCrypto().isCrossSigningReady())) {
    await client.getCrypto().bootstrapCrossSigning({ authUploadDeviceSigningKeys: async (req) => { await req(null); } })
      .catch((e) => log(`cross-signing not set up, the device shows unverified: ${e.message}`));
  }
  const profile = await client.getProfileInfo(s.user_id).catch(() => ({}));
  if (profile.displayname !== 'Meeting notes') await client.setDisplayName('Meeting notes').catch(() => {});
  // A restart drops whatever it was doing: rooms it is in need a new invite (D2).
  for (const room of client.getRooms()) {
    if (room.getMyMembership() === 'join') await leave(room, 'Meeting notes restarted and left. Invite them again for the next meeting.');
    else if (room.getMyMembership() === 'invite') onMembership(room, 'invite');
  }
  client.on(RoomEvent.MyMembership, onMembership);
  log(`running as ${s.user_id} on ${s.device_id}`);
  await ended;
  log('the session ended (turned off or revoked)');
  for (const m of meetings.values()) if (m instanceof Meeting) await m.finish('the session ended', { post: false });
  clearInterval(saver);
  client.stopClient();
  await persist();
}

async function main() {
  fs.mkdirSync(cfg.dataDir, { recursive: true, mode: 0o700 });
  for (;;) {
    const stored = fs.existsSync(deviceFile) ? fs.readFileSync(deviceFile, 'utf8').trim() : '';
    let s;
    try {
      s = await session(stored && fs.existsSync(storeFile) ? stored : '');
    } catch (e) {
      log(e.status === 403 ? 'meeting notes are turned off in Settings; checking again in a minute' : `no session yet: ${e.message}`);
      await sleep(60_000);
      continue;
    }
    if (s.device_id !== stored || !fs.existsSync(storeFile)) {
      await wipe();
      fs.rmSync(storeFile, { force: true });
      fs.writeFileSync(deviceFile, s.device_id, { mode: 0o600 });
    } else {
      await load(storeFile);
    }
    await run(s).catch((e) => log(`stopped: ${e.message}`));
    await wipe();
    await sleep(5000);
  }
}

for (const sig of ['SIGTERM', 'SIGINT']) process.on(sig, () => flush().finally(() => process.exit(0)));
// The phonemizer behind Kokoro rethrows uncaught errors with its whole source; report here.
process.on('uncaughtException', (e) => { log(`fatal: ${e.stack ?? e}`); process.exit(1); });
main();
