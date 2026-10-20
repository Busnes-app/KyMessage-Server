// One meeting: join the room's Element Call with its media keys, announce the recording, take
// per-speaker notes, honour commands, post the notes and leave. Audio stays in memory.
import { createRequire } from 'node:module';
import path from 'node:path';
import { MatrixRTCSessionEvent } from 'matrix-js-sdk/lib/matrixrtc/index.js';
import { Segmenter, Throttle, formatNotes, chunks, userOf } from './rules.mjs';
import { speak, transcribe, RATE } from './speech.mjs';
import { resample, toInt16, toFloat } from './audio.mjs';

// rtc-node 1.1.0's KeyProvider.setKey drops the key bytes (dist/e2ee.cjs), so the bot sends
// the FFI request itself. The ESM build is one bundle; the CJS build exposes FfiClient.
const require = createRequire(import.meta.url);
const { AudioFrame, AudioSource, AudioStream, EncryptionType, LocalAudioTrack, Room, RoomEvent, TrackKind, TrackPublishOptions, TrackSource } = require('@livekit/rtc-node');
const { E2eeRequest, KeyDerivationFunction, SetKeyRequest } = require('@livekit/rtc-ffi-bindings');
const { FfiClient } = require(path.join(path.dirname(require.resolve('@livekit/rtc-node')), 'ffi_client.cjs'));

export const NOTICE = 'This meeting is being recorded for note taking.';
const HELP = 'Anyone in the call can type !notes pause, !notes resume or !notes leave.';
const ALONE_MS = 30_000; // the call ended for the bot once nobody else is in it this long

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
    this.lk.on(RoomEvent.TrackSubscribed, (track, _pub, p) => { if (track.kind === TrackKind.KIND_AUDIO) this.listen(track, p.identity); });
    this.lk.on(RoomEvent.ParticipantConnected, () => this.lateJoin());
    this.lk.on(RoomEvent.ParticipantDisconnected, (p) => { this.stopReader(p.identity); this.checkAlone(); });
    this.lk.on(RoomEvent.Disconnected, () => this.finish('disconnected from the call'));
    await this.lk.connect(this.cfg.livekitUrl, jwt, { autoSubscribe: true, dynacast: false, encryption: {
      encryptionType: EncryptionType.GCM,
      // Element Call's MatrixKeyProvider: HKDF from raw keys, livekit-client's salt, window and ring.
      keyProviderOptions: { keyDerivationFunction: KeyDerivationFunction.HKDF, ratchetSalt: new TextEncoder().encode('LKFrameEncryptionKey'),
        ratchetWindowSize: 10, keyRingSize: 256, failureTolerance: 10 },
    } });
    this.keys.forEach((k) => this.setKey(k));
    this.source = new AudioSource(24000, 1);
    await this.lk.localParticipant.publishTrack(LocalAudioTrack.createAudioTrack('notes', this.source), new TrackPublishOptions({ source: TrackSource.SOURCE_MICROPHONE }));
    this.log(`joined the call in ${this.room.roomId}`);
    // Element Call gets the bot's key on join; give it a moment before the first words.
    await new Promise((r) => setTimeout(r, 1500));
    this.throttle.spoke(Date.now());
    await this.say(NOTICE);
    await this.notice(`Meeting notes joined the call. ${NOTICE} ${HELP}`);
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
    this.pending = setTimeout(() => { this.pending = null; this.throttle.spoke(Date.now()); this.say(NOTICE); }, this.throttle.wait(Date.now()));
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
      await this.notice(HELP);
    }
  }

  // finish posts the notes (unless the bot lost the room) and leaves the call and the room.
  async finish(reason, { post = true } = {}) {
    if (this.finished) return;
    this.finished = true;
    clearTimeout(this.aloneTimer);
    clearTimeout(this.pending);
    this.log(`leaving the call in ${this.room.roomId}: ${reason}`);
    for (const r of this.readers.values()) r.stopped = true;
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
      }
    } catch (e) {
      this.log(`posting the notes failed: ${e.message}`);
    }
    this.entries = [];
    this.onDone(this);
  }
}
