import { expect, it } from 'vitest';
import { browserSupport, type SupportEnv } from './browserSupport';

const ed25519: SubtleCrypto = {
  generateKey: async () => ({privateKey: {} as CryptoKey, publicKey: {} as CryptoKey}),
  sign: async () => new ArrayBuffer(64),
} as unknown as SubtleCrypto;
const base = (over: Partial<SupportEnv> = {}): SupportEnv => ({
  subtle: ed25519, indexedDB: {} as IDBFactory, locks: {} as LockManager,
  secureContext: true, brands: ['Google Chrome'], userAgent: '', ...over,
});

it('supports a declared browser with every feature', async () => {
  expect(await browserSupport(base())).toEqual({state: 'supported', missing: []});
});
it('marks an undeclared browser with every feature unverified', async () => {
  expect((await browserSupport(base({brands: [], userAgent: 'Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Version/18.0 Safari/605.1.15'}))).state).toBe('unverified');
});
it('recognises Firefox and Edge from the user agent', async () => {
  expect((await browserSupport(base({brands: [], userAgent: 'Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0'}))).state).toBe('supported');
  expect((await browserSupport(base({brands: ['Microsoft Edge']}))).state).toBe('supported');
});
it('names each missing feature', async () => {
  const failing = {generateKey: async () => { throw new Error('NotSupportedError'); }} as unknown as SubtleCrypto;
  const result = await browserSupport(base({subtle: failing, indexedDB: undefined, locks: undefined, secureContext: false}));
  expect(result.state).toBe('unsupported');
  expect(result.missing).toEqual(['Ed25519 signing', 'IndexedDB', 'Web Locks', 'secure context (HTTPS)']);
});
