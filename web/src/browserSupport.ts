// Feature checks decide whether chat can run; the declared list only decides whether
// we have verified it. The user agent picks wording, never a security decision.
export const supportedBrowsers = 'current desktop Chrome, Edge and Firefox';

export type BrowserSupport = {state: 'supported' | 'unverified' | 'unsupported'; missing: string[]};
export type SupportEnv = {
  subtle?: SubtleCrypto; indexedDB?: IDBFactory; locks?: LockManager;
  secureContext: boolean; brands: string[]; userAgent: string; mobile: boolean;
};

function realEnv(): SupportEnv {
  const nav = globalThis.navigator as Navigator & {userAgentData?: {brands: {brand: string}[]; mobile: boolean}};
  return {
    subtle: globalThis.crypto?.subtle, indexedDB: globalThis.indexedDB, locks: nav?.locks,
    secureContext: globalThis.isSecureContext === true,
    brands: nav?.userAgentData?.brands.map(b => b.brand) ?? [], userAgent: nav?.userAgent ?? '',
    mobile: nav?.userAgentData?.mobile ?? false,
  };
}

async function ed25519(subtle?: SubtleCrypto): Promise<boolean> {
  if (!subtle) return false;
  try {
    const pair = await subtle.generateKey({name: 'Ed25519'}, false, ['sign', 'verify']) as CryptoKeyPair;
    await subtle.sign({name: 'Ed25519'}, pair.privateKey, new Uint8Array(1));
    return true;
  } catch { return false; }
}

function declared(env: SupportEnv): boolean {
  if (env.mobile || /Mobile|Android|iPhone|iPad/.test(env.userAgent)) return false;
  if (env.brands.length > 0) {
    return env.brands.some(b => b === 'Google Chrome' || b === 'Microsoft Edge');
  }
  const ua = env.userAgent;
  return /Firefox\/\d+/.test(ua) || /Edg\/\d+/.test(ua) || (/Chrome\/\d+/.test(ua) && !/OPR\/|Brave/.test(ua));
}

export async function browserSupport(env: SupportEnv = realEnv()): Promise<BrowserSupport> {
  const missing: string[] = [];
  if (!(await ed25519(env.subtle))) missing.push('Ed25519 signing');
  if (!env.indexedDB) missing.push('IndexedDB');
  if (!env.locks) missing.push('Web Locks');
  if (!env.secureContext) missing.push('secure context (HTTPS)');
  if (missing.length) return {state: 'unsupported', missing};
  return {state: declared(env) ? 'supported' : 'unverified', missing};
}
