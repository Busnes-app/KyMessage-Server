import test from 'node:test';
import assert from 'node:assert/strict';
import { roomAdmin, parseCommand, Throttle, userOf, Segmenter, formatNotes, chunks } from '../src/rules.mjs';

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
