// Needs the models: NOTES_MODELS_DIR (fetch-models.sh). Skipped without them.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';

const dir = process.env.NOTES_MODELS_DIR;
const have = dir && fs.existsSync(`${dir}/onnx-community/whisper-base.en/config.json`);

test('Kokoro speaks the notice and Whisper hears it back, offline', { skip: !have && 'no models' }, async () => {
  const { speak, transcribe } = await import('../src/speech.mjs');
  const { resample } = await import('../src/audio.mjs');
  const notice = 'This meeting is being recorded for note taking.';
  const { audio, rate } = await speak(notice);
  const heard = await transcribe(resample(audio, rate, 16000));
  const norm = (s) => s.toLowerCase().replace(/-/g, ' ').replace(/[^a-z ]/g, '').trim();
  assert.equal(norm(heard), norm(notice));
  assert.equal(await transcribe(new Float32Array(16000)), '');
});
