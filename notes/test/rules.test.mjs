import test from 'node:test';
import assert from 'node:assert/strict';
import { roomAdmin, parseCommand, Throttle, userOf, Segmenter, formatNotes, chunks, thumb, StillPicture, mediaKeyOK } from '../src/rules.mjs';

const create = { sender: '@alice:x', content: { additional_creators: ['@carol:x'] } };
const levels = { content: { users: { '@dana:x': 100, '@bob:x': 50 }, users_default: 0 } };

test('roomAdmin: creators and power level 100 only', () => {
  for (const u of ['@alice:x', '@carol:x', '@dana:x']) assert.equal(roomAdmin(create, levels, u), true, u);
  for (const u of ['@bob:x', '@erin:x', '', undefined]) assert.equal(roomAdmin(create, levels, u), false, String(u));
  assert.equal(roomAdmin(create, { content: { users_default: 100 } }, '@erin:x'), true);
  assert.equal(roomAdmin(null, levels, '@dana:x'), false);
});

test('parseCommand', () => {
  assert.equal(parseCommand('!notes pause'), 'pause');
  assert.equal(parseCommand('  !NOTES Resume '), 'resume');
  assert.equal(parseCommand('!notes stop'), 'leave');
  assert.equal(parseCommand('!notes leave now'), null);
  assert.equal(parseCommand('please !notes pause'), null);
  assert.equal(parseCommand(undefined), null);
});

test('Throttle: at most one notice a minute', () => {
  const t = new Throttle(60_000);
  assert.equal(t.wait(0), 0);
  t.spoke(1000);
  assert.equal(t.wait(31_000), 30_000);
  assert.equal(t.wait(61_000), 0);
});

test('userOf strips the device, keeps a server port', () => {
  assert.equal(userOf('@alice:example.com:DEVICE'), '@alice:example.com');
  assert.equal(userOf('@alice:example.com:8448:DEV'), '@alice:example.com:8448');
  assert.equal(userOf('opaque'), 'opaque');
});

const frame = (amp, n = 160) => Float32Array.from({ length: n }, (_, i) => (i % 2 ? amp : -amp));

test('Segmenter: an utterance between silences, with pre-roll', () => {
  const s = new Segmenter({ silenceMs: 100, preRollMs: 20, minSpeechMs: 50 });
  let t = 0, out = null;
  const feed = (amp, frames) => { for (let i = 0; i < frames; i++, t += 10) out = s.push(frame(amp), t) ?? out; };
  feed(0, 50);
  assert.equal(out, null);
  feed(0.2, 20); // 200 ms of speech
  feed(0, 10); // 100 ms of silence ends it
  assert.ok(out);
  assert.equal(out.at, 480); // 20 ms of pre-roll before the first loud frame at 500
  assert.equal(out.audio.length, (2 + 20 + 10) * 160);
});

test('Segmenter: a click is dropped, a monologue is cut at max', () => {
  const s = new Segmenter({ silenceMs: 100, maxMs: 1000, minSpeechMs: 100 });
  let t = 0, out = [];
  const feed = (amp, frames) => { for (let i = 0; i < frames; i++, t += 10) { const r = s.push(frame(amp), t); if (r) out.push(r); } };
  feed(0.2, 2); feed(0, 20); // 20 ms click
  assert.equal(out.length, 0);
  feed(0.2, 250); // 2.5 s
  assert.equal(out.length, 2);
  assert.ok(s.flush());
});

test('formatNotes orders lines and pauses, names speakers', () => {
  const start = Date.UTC(2026, 9, 20, 14, 0);
  const text = formatNotes({ room: 'Team', start, end: start + 1800_000, entries: [
    { at: start + 120_000, who: 'Bob', text: 'Agreed.' },
    { at: start + 60_000, who: 'Alice', text: 'Budget review moves to Tuesday.' },
    { at: start + 600_000, until: start + 720_000, by: 'Bob' },
  ] });
  assert.equal(text, [
    'Meeting notes: Team, 20 Oct 2026, 14:00-14:30 UTC',
    'Speakers: Bob, Alice',
    '',
    '[14:01] Alice: Budget review moves to Tuesday.',
    '[14:02] Bob: Agreed.',
    '[14:10] Paused by Bob until 14:12',
  ].join('\n'));
  assert.match(formatNotes({ room: 'T', start, end: start, entries: [] }), /No speech was transcribed\.$/);
});

test('chunks: split at lines, never over max', () => {
  const text = ['a'.repeat(6), 'b'.repeat(6), 'c'.repeat(25)].join('\n');
  const parts = chunks(text, 10);
  assert.deepEqual(parts.map((p) => p.length), [6, 6, 10, 10, 5]);
  assert.equal(parts.join(''), text.replace(/\n/g, ''));
  assert.deepEqual(chunks('short', 10), ['short']);
});

test('parseCommand: screens on and off', () => {
  assert.equal(parseCommand('!notes screens on'), 'screens-on');
  assert.equal(parseCommand('!notes  Screens   OFF'), 'screens-off');
  assert.equal(parseCommand('!notes screens'), null);
});

test('thumb averages blocks into grey', () => {
  const w = 4, h = 2, rgba = new Uint8Array(w * h * 4);
  for (let x = 0; x < 2; x++) for (let y = 0; y < 2; y++) rgba.set([255, 255, 255, 255], (y * w + x) * 4); // left half white
  assert.deepEqual([...thumb(rgba, w, h, 2, 1)].map((v) => Math.round(v * 100) / 100), [1, 0]);
});

test('StillPicture: one snapshot per settled change, spaced out', () => {
  const s = new StillPicture({ stillMs: 2000, gapMs: 10_000 });
  const a = new Float32Array(4).fill(0.2), b = new Float32Array(4).fill(0.8);
  const shots = [];
  for (let t = 0; t <= 30_000; t += 1000) {
    const pic = t < 14_000 ? a : b; // a slide change at 14 s
    if (s.feed(t < 1000 || (t >= 14_000 && t < 15_000) ? pic.map((v, i) => v + (i ? 0.5 : 0)) : pic, t)) shots.push(t);
  }
  // a settles at 1 s, so its first still frame 2 s on is 3 s; b settles at 15 s, so 17 s.
  assert.deepEqual(shots, [3000, 17_000]);
  // The same picture again is never shot twice.
  assert.equal(s.feed(b, 40_000), false);
});

test('formatNotes lists snapshots but not as speakers', () => {
  const start = Date.UTC(2026, 9, 20, 14, 0);
  const text = formatNotes({ room: 'T', start, end: start, entries: [{ at: start, who: 'Alice', shot: 1 }] });
  assert.match(text, /Speakers: none/);
  assert.match(text, /\[14:00\] Snapshot 1 of Alice's shared screen \(posted below\)/);
});

test('mediaKeyOK: call media keys below 128 bits are refused', () => {
  assert.equal(mediaKeyOK(new Uint8Array(16)), true);
  assert.equal(mediaKeyOK(new Uint8Array(32)), true);
  for (const k of [new Uint8Array(15), new Uint8Array(0), undefined, null]) assert.equal(mediaKeyOK(k), false, String(k?.length));
});
