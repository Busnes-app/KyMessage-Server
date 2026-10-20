// usage (inside the notes image): node say.mjs TEXT OUT.wav [VOICE]
// Kokoro speaks TEXT into a 48 kHz 16-bit mono WAV: the acceptance participant's microphone.
import fs from 'node:fs';
import { speak } from '/app/src/speech.mjs';
import { resample, toInt16 } from '/app/src/audio.mjs';

const [text, file, voice] = process.argv.slice(2);
const { audio, rate } = await speak(text, voice);
const pcm = Buffer.from(toInt16(resample(audio, rate, 48000)).buffer);
const h = Buffer.alloc(44);
h.write('RIFF', 0); h.writeUInt32LE(36 + pcm.length, 4); h.write('WAVEfmt ', 8); h.writeUInt32LE(16, 16); h.writeUInt16LE(1, 20); h.writeUInt16LE(1, 22);
h.writeUInt32LE(48000, 24); h.writeUInt32LE(96000, 28); h.writeUInt16LE(2, 32); h.writeUInt16LE(16, 34); h.write('data', 36); h.writeUInt32LE(pcm.length, 40);
fs.writeFileSync(file, Buffer.concat([h, pcm]));
console.log(`  ok: Kokoro spoke ${(audio.length / rate).toFixed(1)}s into ${file}`);
