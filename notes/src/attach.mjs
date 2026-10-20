// Matrix encrypted attachments (client-server spec, "Sending encrypted attachments"): AES-256-CTR
// with a fresh key and a 128-bit IV whose low 64 bits are zero, SHA-256 of the ciphertext.
import { createCipheriv, createDecipheriv, createHash, randomBytes } from 'node:crypto';

const b64 = (b) => b.toString('base64').replace(/=+$/, '');

// encrypt → { data: ciphertext, file: EncryptedFile without url }
export function encrypt(plain) {
  const key = randomBytes(32);
  const iv = Buffer.concat([randomBytes(8), Buffer.alloc(8)]);
  const c = createCipheriv('aes-256-ctr', key, iv);
  const data = Buffer.concat([c.update(plain), c.final()]);
  return {
    data,
    file: {
      v: 'v2', iv: b64(iv), hashes: { sha256: b64(createHash('sha256').update(data).digest()) },
      key: { kty: 'oct', key_ops: ['encrypt', 'decrypt'], alg: 'A256CTR', k: key.toString('base64url'), ext: true },
    },
  };
}

// decrypt reverses encrypt after checking the hash (tests; a client does the same).
export function decrypt(data, file) {
  if (b64(createHash('sha256').update(data).digest()) !== file.hashes.sha256) throw new Error('hash mismatch');
  const d = createDecipheriv('aes-256-ctr', Buffer.from(file.key.k, 'base64url'), Buffer.from(file.iv, 'base64'));
  return Buffer.concat([d.update(data), d.final()]);
}
