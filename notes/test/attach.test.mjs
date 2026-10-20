import test from 'node:test';
import assert from 'node:assert/strict';
import { encrypt, decrypt } from '../src/attach.mjs';

test('attachments: fresh key and IV, low IV half zero, round trip, tamper refused', () => {
  const plain = Buffer.from('a snapshot');
  const a = encrypt(plain), b = encrypt(plain);
  assert.notEqual(a.file.key.k, b.file.key.k);
  assert.notDeepEqual(a.data, plain);
  assert.deepEqual(Buffer.from(a.file.iv, 'base64').subarray(8), Buffer.alloc(8));
  assert.equal(a.file.key.alg, 'A256CTR');
  assert.deepEqual(decrypt(a.data, a.file), plain);
  const bad = Buffer.from(a.data); bad[0] ^= 1;
  assert.throws(() => decrypt(bad, a.file), /hash mismatch/);
});
