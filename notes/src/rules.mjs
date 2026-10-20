// The bot's decisions, free of I/O so node:test covers them.

// roomAdmin: the room's creator (m.room.create sender or additional_creators) or power level
// 100. The server applies the same rule before every call grant (synapseadmin.AdminInvite).
export function roomAdmin(create, levels, user) {
  if (!create || !user) return false;
  if (create.sender === user || (create.content?.additional_creators ?? []).includes(user)) return true;
  const pl = levels?.content ?? {};
  const level = Object.hasOwn(pl.users ?? {}, user) ? pl.users[user] : (pl.users_default ?? 0);
  return Number(level) >= 100;
}

// parseCommand: "!notes pause|resume|leave|stop|help" → the command, else null.
export function parseCommand(body) {
  const m = /^\s*!notes\s+(pause|resume|leave|stop|help)\s*$/i.exec(body ?? '');
  if (!m) return null;
  const c = m[1].toLowerCase();
  return c === 'stop' ? 'leave' : c;
}

// Throttle spaces spoken notices at least intervalMs apart. wait(now) is how long until the
// next may play (0: now); spoke(now) records one.
export class Throttle {
  constructor(intervalMs = 60_000) { this.interval = intervalMs; this.last = -Infinity; }
  wait(now) { return Math.max(0, this.last + this.interval - now); }
  spoke(now) { this.last = now; }
}

// userOf: the Matrix user of an Element Call LiveKit identity "@user:server:DEVICE".
export function userOf(identity) {
  const i = identity.lastIndexOf(':');
  return identity.startsWith('@') && i > identity.indexOf(':') ? identity.slice(0, i) : identity;
}

// Segmenter cuts one speaker's 16 kHz mono audio into utterances by energy: an utterance starts
// at the first loud frame (with preRollMs before it), ends after silenceMs of quiet or at maxMs,
// and is dropped when it held less than minSpeechMs of speech.
export class Segmenter {
  constructor({ rate = 16000, threshold = 0.01, preRollMs = 300, silenceMs = 800, maxMs = 20_000, minSpeechMs = 300 } = {}) {
    Object.assign(this, { rate, threshold, minSpeech: (minSpeechMs * rate) / 1000 });
    this.preRoll = (preRollMs * rate) / 1000;
    this.silence = (silenceMs * rate) / 1000;
    this.max = (maxMs * rate) / 1000;
    this.reset();
  }

  reset() { this.buf = []; this.len = 0; this.speech = 0; this.quiet = 0; this.active = false; this.startAt = 0; }

  // push(samples Float32Array, at ms of the frame's start) → a finished {audio, at} or null.
  push(samples, at) {
    let e = 0;
    for (const s of samples) e += s * s;
    const loud = Math.sqrt(e / samples.length) >= this.threshold;
    if (!this.active) {
      while (this.buf.length && this.len - this.buf[0].length >= this.preRoll) this.len -= this.buf.shift().length;
      this.buf.push(samples);
      this.len += samples.length;
      if (!loud) return null;
      this.active = true;
      this.startAt = at - ((this.len - samples.length) * 1000) / this.rate;
      this.speech = samples.length;
      return null;
    }
    this.buf.push(samples);
    this.len += samples.length;
    if (loud) { this.speech += samples.length; this.quiet = 0; } else this.quiet += samples.length;
    return this.quiet >= this.silence || this.len >= this.max ? this.flush() : null;
  }

  // flush ends the current utterance: {audio, at} when it held enough speech, else null.
  flush() {
    const { buf, len, speech, active, startAt } = this;
    this.reset();
    if (!active || speech < this.minSpeech) return null;
    const audio = new Float32Array(len);
    let o = 0;
    for (const b of buf) { audio.set(b, o); o += b.length; }
    return { audio, at: startAt };
  }
}

const clock = (ms, timeZone) => new Date(ms).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', timeZone });

// formatNotes renders the transcript: entries are {at, who, text} lines and {at, until, by}
// pauses, any order; times in timeZone.
export function formatNotes({ room, start, end, entries, timeZone = 'UTC' }) {
  const day = new Date(start).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone });
  const people = [...new Set(entries.filter((e) => e.who).map((e) => e.who))];
  const head = [`Meeting notes: ${room}, ${day}, ${clock(start, timeZone)}-${clock(end, timeZone)} ${timeZone}`,
    `Speakers: ${people.length ? people.join(', ') : 'none'}`, ''];
  const body = [...entries].sort((a, b) => a.at - b.at).map((e) => (e.who
    ? `[${clock(e.at, timeZone)}] ${e.who}: ${e.text}`
    : `[${clock(e.at, timeZone)}] Paused by ${e.by} until ${e.until ? clock(e.until, timeZone) : 'the end'}`));
  return [...head, ...(body.length ? body : ['No speech was transcribed.'])].join('\n');
}

// chunks splits text at line breaks into parts of at most max characters (a line longer than
// max is cut), so each part fits one Matrix event once encrypted.
export function chunks(text, max = 30_000) {
  const out = [];
  let cur = '';
  for (let line of text.split('\n')) {
    while (line.length > max) { if (cur) { out.push(cur); cur = ''; } out.push(line.slice(0, max)); line = line.slice(max); }
    if (cur.length + line.length + 1 > max) { out.push(cur); cur = ''; }
    cur = cur ? `${cur}\n${line}` : line;
  }
  if (cur) out.push(cur);
  return out;
}
