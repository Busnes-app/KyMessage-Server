// PCM helpers.

// resample: linear, which is enough for speech going to Whisper or out to the call.
export function resample(audio, from, to) {
  if (from === to) return audio;
  const out = new Float32Array(Math.floor((audio.length * to) / from));
  for (let i = 0; i < out.length; i++) {
    const x = (i * from) / to, j = Math.floor(x), f = x - j;
    out[i] = (audio[j] ?? 0) * (1 - f) + (audio[j + 1] ?? 0) * f;
  }
  return out;
}

export const toInt16 = (f) => Int16Array.from(f, (s) => Math.max(-1, Math.min(1, s)) * 32767);
export const toFloat = (i) => Float32Array.from(i, (s) => s / 32768);
