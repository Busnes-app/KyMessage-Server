// One meeting: join the room's Element Call with its media keys, announce the recording, take
// per-speaker notes and, when allowed and turned on, snapshots of shared screens; honour
// commands, post the notes and leave. Audio and snapshots stay in memory until then.
import { createRequire } from 'node:module';
import path from 'node:path';
import { MatrixRTCSessionEvent } from 'matrix-js-sdk/lib/matrixrtc/index.js';
import sharp from 'sharp';
import { Segmenter, StillPicture, Throttle, formatNotes, chunks, roomAdmin, thumb, userOf } from './rules.mjs';
import { encrypt } from './attach.mjs';
import { speak, transcribe, RATE } from './speech.mjs';
import { resample, toInt16, toFloat } from './audio.mjs';

// rtc-node 1.1.0's KeyProvider.setKey drops the key bytes (dist/e2ee.cjs), so the bot sends
// the FFI request itself. The ESM build is one bundle; the CJS build exposes FfiClient.
const require = createRequire(import.meta.url);
const { AudioFrame, AudioSource, AudioStream, EncryptionType, LocalAudioTrack, Room, RoomEvent, TrackKind, TrackPublishOptions, TrackSource, VideoBufferType, VideoStream } = require('@livekit/rtc-node');
const { E2eeRequest, KeyDerivationFunction, SetKeyRequest } = require('@livekit/rtc-ffi-bindings');
const { FfiClient } = require(path.join(path.dirname(require.resolve('@livekit/rtc-node')), 'ffi_client.cjs'));

export const NOTICE = 'This meeting is being recorded for note taking.';
const HELP = 'Anyone in the call can type !notes pause, !notes resume or !notes leave.';
const SCREENS_HELP = "The room's creator or an admin can type !notes screens on to add shared screens; anyone in the call can type !notes screens off.";
const MAX_SHOTS = 60;
const SCREENS_RECHECK_MS = 10_000;
const ALONE_MS = 30_000; // the call ended for the bot once nobody else is in it this long
const LATE_LEAD_MS = 3000; // a newcomer's client subscribes and takes keys before the notice

// Transcription is CPU-bound: one at a time, across meetings.
let transcribing = Promise.resolve();
const queue = (fn) => (transcribing = transcribing.then(fn, fn));

export class Meeting {
  constructor({ client, room, cfg, log, onDone }) {
    Object.assign(this, { client, room, cfg, log, onDone });
    this.me = client.getUserId();
    this.device = client.getDeviceId();
    this.identity = `${this.me}:${this.device}`;
    this.session = client.matrixRTC.getRoomSession(room);
    this.keys = [];
    this.entries = [];
    this.readers = new Map(); // LiveKit identity → { seg, stop }
    this.throttle = new Throttle(cfg.noticeIntervalMs);
    this.paused = null; // the open pause entry
    this.speaking = Promise.resolve();
    this.finished = false;
    this.screens = false; // this meeting's !notes screens on
    this.shares = new Map(); // LiveKit identity → { stopped }
    this.shots = []; // { at, who, jpeg, w, h }
  }

  name(user) { return this.room.getMember(user)?.name ?? user; }
  notice(body) { return this.client.sendMessage(this.room.roomId, { msgtype: 'm.notice', body }); }

  setKey({ key, index, identity }) {
    FfiClient.instance.request({ message: { case: 'e2ee', value: new E2eeRequest({ roomHandle: this.lk.e2eeManager.roomHandle,
      message: { case: 'setKey', value: new SetKeyRequest({ participantIdentity: identity, key, keyIndex: index }) } }) } });
  }

  async start() {
    this.startedAt = Date.now();
    this.session.on(MatrixRTCSessionEvent.EncryptionKeyChanged, this.onKey = (key, index, _m, identity) => {
      const k = { key, index, identity };
      this.keys.push(k);
      if (this.lk) this.setKey(k);
    });
    this.session.join({ userId: this.me, deviceId: this.device, memberId: this.identity },
      [{ type: 'livekit', livekit_service_url: this.cfg.serviceUrl, livekit_alias: this.room.roomId }], { manageMediaKeys: true });
    const jwt = await this.grant();
    this.lk = new Room();
    // Audio, and screens only while turned on: the bot never subscribes to a camera, so its
    // frames never reach it.
    this.lk.on(RoomEvent.TrackPublished, (pub) => this.want(pub));
    this.lk.on(RoomEvent.TrackUnpublished, (_pub, p) => this.stopShare(p.identity));
    this.lk.on(RoomEvent.TrackSubscribed, (track, pub, p) => {
      if (track.kind === TrackKind.KIND_AUDIO) {
        this.log('subscribed to an audio track');
        this.listen(track, p.identity);
      } else if (this.screens && pub.source === TrackSource.SOURCE_SCREENSHARE) {
        this.log('subscribed to a screen track');
        this.watch(track, p.identity);
      } else pub.setSubscribed(false);
    });
    this.lk.on(RoomEvent.ParticipantConnected, () => this.lateJoin());
    this.lk.on(RoomEvent.ParticipantDisconnected, (p) => { this.stopReader(p.identity); this.checkAlone(); });
    this.lk.on(RoomEvent.Disconnected, () => this.finish('disconnected from the call'));
    await this.lk.connect(this.cfg.livekitUrl, jwt, { autoSubscribe: false, dynacast: false, encryption: {
      encryptionType: EncryptionType.GCM,
      // Element Call's MatrixKeyProvider: HKDF from raw keys, livekit-client's salt, window and ring.
      keyProviderOptions: { keyDerivationFunction: KeyDerivationFunction.HKDF, ratchetSalt: new TextEncoder().encode('LKFrameEncryptionKey'),
        ratchetWindowSize: 10, keyRingSize: 256, failureTolerance: 10 },
    } });
    this.keys.forEach((k) => this.setKey(k));
    for (const p of this.lk.remoteParticipants.values()) for (const pub of p.trackPublications.values()) this.want(pub);
    this.source = new AudioSource(24000, 1);
    // Speak only once someone is listening: their client has subscribed to the bot's track and
    // had a moment to take its key. Words spoken earlier arrive clipped or not at all.
    const heard = new Promise((r) => { this.lk.once(RoomEvent.LocalTrackSubscribed, r); setTimeout(r, 15_000); });
    await this.lk.localParticipant.publishTrack(LocalAudioTrack.createAudioTrack('notes', this.source), new TrackPublishOptions({ source: TrackSource.SOURCE_MICROPHONE }));
    this.log(`joined the call in ${this.room.roomId}`);
    await heard;
    await new Promise((r) => setTimeout(r, 1000));
    this.throttle.spoke(Date.now());
    await this.say(NOTICE);
    await this.notice(`Meeting notes joined the call. ${NOTICE} ${HELP} ${SCREENS_HELP}`);
    this.checkAlone();
  }

  // grant asks KyMessages for the LiveKit grant Element Call would get (/sfu/get).
  async grant() {
    const openid = await this.client.getOpenIdToken();
    const r = await fetch(`${this.cfg.appUrl}/api/matrix/rtc/sfu/get`, { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ room: this.room.roomId, device_id: this.device, openid_token: openid }) });
    const body = await r.json().catch(() => ({}));
    if (!r.ok || !body.jwt) throw Object.assign(new Error(body.error ?? `grant refused (${r.status})`), { refused: true });
    return body.jwt;
  }

  want(pub) {
    const screen = pub.source === TrackSource.SOURCE_SCREENSHARE;
    if (pub.kind === TrackKind.KIND_AUDIO) {
      if (!pub.subscribed) pub.setSubscribed(true);
    } else if (screen && this.screens) {
      // Settings may have changed since the meeting turned snapshots on: ask before each share.
      this.screensAllowed().then((ok) => {
        if (ok && this.screens && !this.finished && !pub.subscribed) pub.setSubscribed(true);
        else if (!ok) this.revoked();
      }, () => this.log('could not read the snapshot setting; the share is skipped'));
    } else this.log(`skipped a ${screen ? 'screen' : 'camera'} track`);
  }

  // revoked ends snapshots in this meeting once Settings no longer allows them (or cannot say).
  revoked() {
    if (!this.screens) return;
    this.setScreens(false);
    this.say('Shared screens are no longer captured.');
    this.notice('Shared-screen snapshots were turned off in Settings, so shared screens are no longer captured.').catch(() => {});
  }

  // screensAllowed asks KyMessages whether Settings allows shared-screen snapshots.
  async screensAllowed() {
    const r = await fetch(`${this.cfg.appUrl}/api/notes/config`, { headers: { Authorization: `Bearer ${this.cfg.secret}` } });
    return r.ok && (await r.json()).screens === true;
  }

  isAdmin(user) {
    const ev = (t) => { const e = this.room.currentState.getStateEvents(t, ''); return e && { sender: e.getSender(), content: e.getContent() }; };
    return roomAdmin(ev('m.room.create'), ev('m.room.power_levels'), user);
  }

  // setScreens subscribes to every shared screen, or drops them all. While on, it re-reads the
  // Settings permission every SCREENS_RECHECK_MS and drops them all once it is gone.
  setScreens(on) {
    this.screens = on;
    clearInterval(this.recheck);
    // Fails closed: an unreadable setting ends them too.
    if (on) this.recheck = setInterval(() => this.screensAllowed().then((ok) => ok || this.revoked(), () => this.revoked()), SCREENS_RECHECK_MS);
    for (const p of this.lk.remoteParticipants.values()) {
      for (const pub of p.trackPublications.values()) {
        if (pub.source !== TrackSource.SOURCE_SCREENSHARE) continue;
        if (on) this.want(pub); else { this.stopShare(p.identity); if (pub.subscribed) pub.setSubscribed(false); }
      }
    }
  }

  // watch looks at a shared screen about once a second and keeps a snapshot whenever the
  // picture changed and held still (StillPicture). Nothing while paused.
  watch(track, identity) {
    if (this.shares.has(identity)) return;
    const who = this.name(userOf(identity));
    const share = { stopped: false };
    this.shares.set(identity, share);
    this.say(`${who}'s shared screen is being captured for the notes.`);
    this.notice(`Capturing ${who}'s shared screen for the notes. !notes screens off stops it.`).catch(() => {});
    const still = new StillPicture();
    (async () => {
      let next = 0;
      for await (const { frame } of new VideoStream(track)) {
        if (share.stopped || this.finished || !this.screens) break;
        const now = Date.now();
        if (now < next || this.paused || this.shots.length >= MAX_SHOTS) continue;
        next = now + 1000;
        const f = frame.convert(VideoBufferType.RGBA);
        if (!still.feed(thumb(f.data, f.width, f.height), now)) continue;
        const { data, info } = await sharp(Buffer.from(f.data.buffer, f.data.byteOffset, f.data.byteLength), { raw: { width: f.width, height: f.height, channels: 4 } })
          .resize(1600, 1600, { fit: 'inside', withoutEnlargement: true }).jpeg({ quality: 80 }).toBuffer({ resolveWithObject: true });
        this.shots.push({ at: now, who, jpeg: data, w: info.width, h: info.height });
        this.entries.push({ at: now, who, shot: this.shots.length });
        this.log('took a screen snapshot');
      }
    })().catch((e) => this.log(`a shared screen failed: ${e.message}`));
  }

  stopShare(identity) {
    const s = this.shares.get(identity);
    if (s) { s.stopped = true; this.shares.delete(identity); }
  }

  // say speaks text into the call, one utterance at a time.
  say(text) {
    return (this.speaking = this.speaking.then(async () => {
      if (this.finished) return;
      const { audio, rate } = await speak(text);
      const pcm = toInt16(resample(audio, rate, 24000));
      const n = 480; // 20 ms
      for (let i = 0; i < pcm.length; i += n) {
        const f = new Int16Array(n);
        f.set(pcm.subarray(i, i + n));
        await this.source.captureFrame(new AudioFrame(f, 24000, 1, n));
      }
      await this.source.waitForPlayout();
    }).catch((e) => this.log(`speaking failed: ${e.message}`)));
  }

  // lateJoin repeats the spoken notice for someone joining, at most once a minute.
  lateJoin() {
    this.checkAlone();
    if (this.pending || this.finished) return;
    this.pending = setTimeout(() => { this.pending = null; this.throttle.spoke(Date.now()); this.say(NOTICE); }, Math.max(LATE_LEAD_MS, this.throttle.wait(Date.now())));
  }

  listen(track, identity) {
    if (identity === this.identity || this.readers.has(identity)) return;
    const reader = { seg: new Segmenter({ rate: RATE }), who: this.name(userOf(identity)), stopped: false };
    this.readers.set(identity, reader);
    const { seg } = reader;
    const take = (u) => this.take(u, reader.who);
    (async () => {
      let t = Date.now();
      for await (const f of new AudioStream(track, RATE, 1)) {
        if (reader.stopped || this.finished) break;
        const samples = toFloat(f.data);
        if (this.paused) { seg.reset(); t = Date.now(); continue; }
        take(seg.push(samples, t));
        t += (samples.length * 1000) / RATE;
      }
      take(seg.flush());
    })().catch((e) => this.log(`audio from a participant failed: ${e.message}`));
  }

  // take queues one utterance for Whisper; its text joins the notes under who.
  take(u, who) {
    if (u) queue(async () => { const text = await transcribe(u.audio); if (text) this.entries.push({ at: u.at, who, text }); });
  }

  stopReader(identity) {
    const r = this.readers.get(identity);
    if (r) { r.stopped = true; this.readers.delete(identity); this.take(r.seg.flush(), r.who); }
  }

  checkAlone() {
    const others = [...(this.lk?.remoteParticipants?.values() ?? [])].length;
    clearTimeout(this.aloneTimer);
    if (!others && !this.finished) this.aloneTimer = setTimeout(() => this.finish('the call ended'), ALONE_MS);
  }

  // command runs a chat command from a member who is in the call.
  async command(cmd, sender) {
    if (!this.session.memberships.some((m) => m.sender === sender)) return;
    const by = this.name(sender);
    if (cmd === 'pause' && !this.paused) {
      this.paused = { at: Date.now(), by };
      this.entries.push(this.paused);
      await this.say('Note taking paused.');
      await this.notice(`Note taking paused by ${by}. Type !notes resume to continue.`);
    } else if (cmd === 'resume' && this.paused) {
      this.paused.until = Date.now();
      this.paused = null;
      await this.say('Note taking resumed.');
      await this.notice(`Note taking resumed by ${by}.`);
    } else if (cmd === 'leave') {
      await this.say('Meeting notes are leaving. The notes will be posted in the chat.');
      await this.finish(`${by} asked it to leave`);
    } else if (cmd === 'help') {
      await this.notice(`${HELP} ${SCREENS_HELP}`);
    } else if (cmd === 'screens-on' && !this.screens) {
      if (!this.isAdmin(sender)) return this.notice("Only the room's creator or an admin can turn on shared-screen snapshots.");
      if (!(await this.screensAllowed())) return this.notice('Shared-screen snapshots are turned off in Settings.');
      this.setScreens(true);
      await this.say('Shared screens will be captured for the notes.');
      await this.notice(`Shared-screen snapshots turned on by ${by}: still pictures of shared screens will be posted with the notes. !notes screens off stops it.`);
    } else if (cmd === 'screens-off' && this.screens) {
      this.setScreens(false);
      await this.say('Shared screens are no longer captured.');
      await this.notice(`Shared-screen snapshots turned off by ${by}.`);
    }
  }

  // postShot uploads one snapshot encrypted (only the room can read it) as m.image.
  async postShot(n, { at, who, jpeg, w, h }) {
    const { data, file } = encrypt(jpeg);
    const { content_uri: url } = await this.client.uploadContent(data, { type: 'application/octet-stream', includeFilename: false });
    const time = new Date(at).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', timeZone: this.cfg.timeZone });
    await this.client.sendMessage(this.room.roomId, { msgtype: 'm.image', body: `Snapshot ${n} of ${who}'s shared screen, ${time}.jpg`,
      file: { ...file, url }, info: { mimetype: 'image/jpeg', w, h, size: jpeg.length } });
  }

  // finish posts the notes (unless the bot lost the room) and leaves the call and the room.
  async finish(reason, { post = true } = {}) {
    if (this.finished) return;
    this.finished = true;
    clearTimeout(this.aloneTimer);
    clearTimeout(this.pending);
    clearInterval(this.recheck);
    this.log(`leaving the call in ${this.room.roomId}: ${reason}`);
    for (const r of this.readers.values()) r.stopped = true;
    for (const id of [...this.shares.keys()]) this.stopShare(id);
    for (const r of this.readers.values()) this.take(r.seg.flush(), r.who);
    if (this.paused) this.paused.until = Date.now();
    await this.speaking;
    await this.lk?.disconnect().catch(() => {});
    await this.session.leave(5000).catch(() => {});
    this.session.off(MatrixRTCSessionEvent.EncryptionKeyChanged, this.onKey);
    await queue(() => {});
    try {
      if (post) {
        const text = formatNotes({ room: this.room.name || 'this room', start: this.startedAt ?? Date.now(), end: Date.now(), entries: this.entries, timeZone: this.cfg.timeZone });
        const parts = chunks(text);
        for (const [i, p] of parts.entries()) await this.notice(parts.length > 1 ? `(${i + 1}/${parts.length}) ${p}` : p);
        for (const [i, s] of this.shots.entries()) await this.postShot(i + 1, s);
      }
    } catch (e) {
      this.log(`posting the notes failed: ${e.message}`);
    }
    this.entries = [];
    this.shots = [];
    this.onDone(this);
  }
}
