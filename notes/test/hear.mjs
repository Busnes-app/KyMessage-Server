// usage (inside the notes image): node hear.mjs IN.wav TEXT
// Exits 0 when Whisper hears TEXT in the 16-bit mono WAV (ignoring case and punctuation), 3 when
// it does not. scripts/matrix-acceptance.sh mounts this file into the image to check audio
// Element Call decoded, with the image's own offline models.
import fs from 'node:fs';
import { transcribe, RATE } from '/app/src/speech.mjs';
import { resample, toFloat } from '/app/src/audio.mjs';

const [file, want] = process.argv.slice(2);
const buf = fs.readFileSync(file);
const pcm = new Int16Array(buf.buffer, buf.byteOffset + 44, (buf.length - 44) >> 1);
const heard = pcm.length ? await transcribe(resample(toFloat(pcm), buf.readUInt32LE(24), RATE)) : '';
const norm = (s) => s.toLowerCase().replace(/-/g, ' ').replace(/[^a-z ]/g, '').replace(/\s+/g, ' ').trim();
console.log(`  heard: "${heard}"`);
if (!norm(heard).includes(norm(want))) { console.error(`  FAILED: "${want}" not heard`); process.exit(3); }
console.log(`  ok: Whisper heard "${want}"`);
