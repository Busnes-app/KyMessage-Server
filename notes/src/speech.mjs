// Local speech, offline: Kokoro TTS and Whisper STT on the CPU through transformers.js (ONNX),
// from the models the image bakes into NOTES_MODELS_DIR (models.txt). Nothing is downloaded.
import { env, pipeline } from '@huggingface/transformers';
import { KokoroTTS } from 'kokoro-js';

env.allowRemoteModels = false;
env.localModelPath = `${process.env.NOTES_MODELS_DIR ?? '/models'}/`;

export const RATE = 16000; // Whisper's input rate
let tts, stt;

// speak(text) → { audio: Float32Array, rate }
export async function speak(text, voice = 'af_heart') {
  tts ??= await KokoroTTS.from_pretrained('onnx-community/Kokoro-82M-v1.0-ONNX', { dtype: 'q8', device: 'cpu' });
  const out = await tts.generate(text, { voice });
  return { audio: out.audio, rate: out.sampling_rate };
}

// transcribe(Float32Array at RATE) → text, "" for silence. Whisper's well-known output on
// noise ("you", "Thank you.", bracketed sounds) is dropped.
export async function transcribe(audio) {
  stt ??= await pipeline('automatic-speech-recognition', 'onnx-community/whisper-base.en', { dtype: 'q8', device: 'cpu' });
  const { text } = await stt(audio, { chunk_length_s: 30 });
  const t = text.trim();
  return /^(\[.*\]|\(.*\)|you\.?|thank you\.?|thanks for watching!?)$/i.test(t) ? '' : t;
}
