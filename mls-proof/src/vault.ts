// Experiment only: one device per browser profile, separate encrypted room records.
// Unlock derives a non-extractable wrapping key once; neither it nor the passphrase is persisted.
const encoder = new TextEncoder();
const decoder = new TextDecoder('utf-8', { fatal: true });
const aad = (entry: string) => encoder.encode(entry === 'device' ? 'kymessages-mls-proof/v1' : 'kymessages-mls-proof/v1/' + entry);
export const databaseName = 'kymessages-mls-proof-v1';
const maxBytes = 2 * 1024 * 1024;
export const maxSavedMessages = 256;
export const maxSavedMessageBytes = 256 * 1024;

// Retain a recent cache without allowing display history to stop MLS progress.
// Count serialized bytes so escaping/control characters cannot evade the budget.
export function trimSavedMessages<T>(messages: T[]): number {
  let start = messages.length, bytes = 2;
  while (start > 0 && messages.length - start < maxSavedMessages) {
    const size = encoder.encode(JSON.stringify(messages[start - 1])).length + 1;
    if (bytes + size > maxSavedMessageBytes) break;
    bytes += size;
    start--;
  }
  messages.splice(0, start);
  return start;
}

export function base64(bytes: Uint8Array): string {
  return btoa(Array.from(bytes, byte => String.fromCharCode(byte)).join(''));
}

export function unbase64(value: unknown): Uint8Array<ArrayBuffer> {
  if (typeof value !== 'string' || value.length > maxBytes * 2) throw new Error('Invalid encoded bytes');
  const bytes = Uint8Array.from(atob(value), char => char.charCodeAt(0));
  if (bytes.length > maxBytes || base64(bytes) !== value) throw new Error('Invalid encoded bytes');
  return bytes;
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid record');
  return value as Record<string, unknown>;
}

function string(value: unknown): string {
  if (typeof value !== 'string') throw new Error('Invalid text field');
  return value;
}

function nullableString(value: unknown): string | null {
  return value === null ? null : string(value);
}

function list<T>(value: unknown, parse: (item: unknown) => T): T[] {
  if (!Array.isArray(value) || value.length > 256) throw new Error('Invalid or full record list');
  return value.map(parse);
}

// Legacy transcripts have no trustworthy deadline; preserve them until explicit clearing.
export function expiry(value: unknown): number | null {
  if (value === undefined || value === null) return null;
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) throw new Error('Invalid transcript expiry');
  return value;
}

export function parseRecord(value: unknown) {
  const r = object(value);
  if (r.version !== 1 || !Number.isSafeInteger(r.cursor) || typeof r.cursor !== 'number' || r.cursor < 0
    || typeof r.awaitingCommit !== 'boolean') {
    throw new Error('Invalid record version/cursor');
  }
  const keys = r.keys === null ? null : object(r.keys);
  const pending = r.pending === null ? null : object(r.pending);
  return {
    version: 1,
    identity: string(r.identity),
    delivery: r.delivery === undefined ? null : nullableString(r.delivery),
    keyPackage: string(r.keyPackage),
    keys: keys === null ? null : {
      initPrivateKey: string(keys.initPrivateKey),
      hpkePrivateKey: string(keys.hpkePrivateKey),
      signaturePrivateKey: string(keys.signaturePrivateKey),
    },
    pins: list(r.pins, item => {
      const pin = object(item);
      const identityGeneration = pin.identityGeneration === undefined ? 1 : pin.identityGeneration;
      if (typeof identityGeneration !== 'number' || !Number.isSafeInteger(identityGeneration) || identityGeneration < 1) throw new Error('Invalid identity generation');
      return { identity: string(pin.identity), key: string(pin.key), identityGeneration };
    }),
    state: nullableString(r.state),
    pending: pending === null ? null : {
      state: string(pending.state),
      wire: string(pending.wire),
      welcome: nullableString(pending.welcome),
      epoch: string(pending.epoch),
    },
    outbox: list(r.outbox, string),
    inbox: list(r.inbox, item => {
      if (typeof item === 'string') return {text:item,expiresAt:null,sequence:null};
      const saved = object(item);
      return {text:string(saved.text),expiresAt:expiry(saved.expiresAt),sequence:expiry(saved.sequence)};
    }),
    cursor: r.cursor,
    awaitingCommit: r.awaitingCommit,
    received: list(r.received, string),
  };
}

export type DeviceRecord = ReturnType<typeof parseRecord>;

async function openDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(databaseName, 1);
    request.onupgradeneeded = () => request.result.createObjectStore('vault');
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
    request.onblocked = () => reject(new Error('Database upgrade blocked'));
  });
}

async function read(db: IDBDatabase, entry = 'device'): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const transaction = db.transaction('vault', 'readonly');
    const request = transaction.objectStore('vault').get(entry);
    transaction.oncomplete = () => resolve(request.result);
    transaction.onabort = () => reject(transaction.error ?? new Error('Read aborted'));
  });
}

async function write(db: IDBDatabase, value: {version:number;salt:string;iv:string;ciphertext:string}, entry: string, createOnly = false): Promise<void> {
  return new Promise((resolve, reject) => {
    const transaction = db.transaction('vault', 'readwrite', { durability: 'strict' });
    const store = transaction.objectStore('vault');
    let failure: Error | undefined;
    const root = store.get('device');
    root.onsuccess = () => {
      try {
        // A deletion/replacement can win while this room encrypts outside IndexedDB.
        // Check the original vault generation inside the write transaction.
        if (!(createOnly && entry === 'device') && (root.result === undefined || string(object(root.result).salt) !== value.salt)) throw new Error('Vault removed or replaced; lock and unlock again');
        if (createOnly) {
          const count = store.count();
          count.onsuccess = () => { if (count.result >= 100) { failure = new Error('Local room capacity reached; existing conversations remain available'); transaction.abort(); } else store.add(value,entry); };
        } else store.put(value,entry);
      } catch (error) { failure = error instanceof Error ? error : new Error('Invalid root vault'); transaction.abort(); }
    };
    transaction.oncomplete = () => resolve();
    transaction.onabort = () => reject(failure ?? transaction.error ?? new Error('Write aborted'));
  });
}

export async function forgetVault(unlocked: UnlockedVault) {
  return navigator.locks.request(databaseName,async () => {
    const db = await openDatabase();
    try {
      await new Promise<void>((resolve,reject) => {
        const tx = db.transaction('vault','readwrite',{durability:'strict'});
        const entries = tx.objectStore('vault');
        const root = entries.get('device');
        let failure: Error | undefined;
        root.onsuccess = () => {
          try {
            if (root.result === undefined || string(object(root.result).salt) !== unlocked.salt) throw new Error('Vault changed before removal; unlock it again');
            entries.clear();
          } catch (error) { failure = error instanceof Error ? error : new Error('Invalid root vault'); tx.abort(); }
        };
        tx.oncomplete = () => resolve();
        tx.onabort = () => reject(failure ?? tx.error ?? new Error('Vault removal aborted'));
      });
    } finally { db.close(); }
  });
}

async function derive(passphrase: string, salt: Uint8Array<ArrayBuffer>): Promise<CryptoKey> {
  if (passphrase.length < 16 || passphrase.length > 1024) throw new Error('Use a 16–1024 character test passphrase');
  const material = await crypto.subtle.importKey('raw', encoder.encode(passphrase), 'PBKDF2', false, ['deriveKey']);
  return crypto.subtle.deriveKey(
    { name: 'PBKDF2', hash: 'SHA-256', iterations: 600_000, salt },
    material, { name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt'],
  );
}

// The wrapping key lives only in the unlocked tab. Salt binds it to this envelope.
export type UnlockedVault = { key: CryptoKey; salt: string; identity: string };
function envelope(value: unknown) {
  const e = object(value);
  const salt = unbase64(e.salt), iv = unbase64(e.iv);
  if (e.version !== 1 || salt.length !== 16 || iv.length !== 12) throw new Error('Invalid vault envelope');
  return {salt,iv,ciphertext:unbase64(e.ciphertext)};
}
async function decrypt(e: ReturnType<typeof envelope>, key: CryptoKey, entry = 'device') {
  const plaintext = await crypto.subtle.decrypt({name:'AES-GCM',iv:e.iv,additionalData:aad(entry)},key,e.ciphertext);
  try {
    const parsed: unknown = JSON.parse(decoder.decode(plaintext));
    return parseRecord(parsed);
  } finally { new Uint8Array(plaintext).fill(0); }
}
export async function unlockVault(passphrase: string): Promise<UnlockedVault> {
  return navigator.locks.request(databaseName, async () => {
    const db = await openDatabase();
    try {
      const e = envelope(await read(db));
      const key = await derive(passphrase,e.salt);
      const record = await decrypt(e,key); // Authenticate storage before retaining the key.
      return {key,salt:base64(e.salt),identity:record.identity};
    } finally { db.close(); }
  });
}

// Each room owns its ratchet/outbox/history. Only tabs of the SAME room serialize.
// The legacy device record keeps its original lock name and authenticated format.
export async function withVault<T>(unlocked: UnlockedVault, action: (record: DeviceRecord) => Promise<T>, entry = 'device'): Promise<T> {
  return navigator.locks.request(entry === 'device' ? databaseName : databaseName + ':' + entry, async () => {
    const db = await openDatabase();
    try {
      const e = envelope(await read(db,entry));
      if (base64(e.salt) !== unlocked.salt) throw new Error('Vault replaced; lock and unlock again');
      const record = await decrypt(e,unlocked.key,entry);
      const result = await action(record);
      await seal(db, record, e.salt, unlocked.key,entry);
      return result; // Never release wire bytes before the ratchet/outbox commit.
    } finally { db.close(); }
  });
}

async function seal(db: IDBDatabase, record: DeviceRecord, salt: Uint8Array<ArrayBuffer>, key: CryptoKey, entry = 'device', createOnly = false) {
  parseRecord(record); // Enforce proof capacity before committing, including outgoing lists.
  const plaintext = encoder.encode(JSON.stringify(record));
  if (plaintext.length > maxBytes) throw new Error('Proof vault capacity reached');
  const iv = crypto.getRandomValues(new Uint8Array(12));
  try {
    const ciphertext = await crypto.subtle.encrypt({ name: 'AES-GCM', iv, additionalData: aad(entry) }, key, plaintext);
    await write(db, { version: 1, salt: base64(salt), iv: base64(iv), ciphertext: base64(new Uint8Array(ciphertext)) },entry,createOnly);
  } finally {
    plaintext.fill(0);
  }
}

export async function initializeVault(passphrase: string, create: () => Promise<DeviceRecord>) {
  return navigator.locks.request(databaseName, async () => {
    const db = await openDatabase();
    try {
      if (await read(db) !== undefined) throw new Error('Device already initialized; unlock it instead');
      const salt = crypto.getRandomValues(new Uint8Array(16));
      const key = await derive(passphrase, salt);
      const record = await create();
      await seal(db, record, salt, key,'device',true);
      return {wire:record.keyPackage, unlocked:{key,salt:base64(salt),identity:record.identity}};
    } finally {
      db.close();
    }
  });
}

// Existing entries are never recreated, including after a lost initialization reply.
export async function ensureRoomVault(unlocked: UnlockedVault, entry: string, create: () => Promise<DeviceRecord>) {
  return navigator.locks.request(databaseName + ':' + entry, async () => {
    const db = await openDatabase();
    try {
      if (await read(db,entry) !== undefined) return;
      await seal(db,await create(),unbase64(unlocked.salt),unlocked.key,entry,true);
    } finally { db.close(); }
  });
}

// Merge independent room summaries at the read boundary. A damaged entry must not
// hide the other rooms or cause a write; keep its ciphertext for recovery.
export async function inspectVaults<T>(unlocked: UnlockedVault, inspect: (record: DeviceRecord) => T) {
  const db = await openDatabase();
  const result: ({kind:'ready';entry:string;value:T} | {kind:'unreadable'})[] = [];
  try {
    const entries = await new Promise<IDBValidKey[]>((resolve,reject) => {
      const tx = db.transaction('vault','readonly');
      const request = tx.objectStore('vault').getAllKeys();
      tx.oncomplete = () => resolve(request.result);
      tx.onabort = () => reject(tx.error);
    });
    if (entries.length > 100) throw new Error('Local vault entry limit exceeded');
    for (const entry of entries) {
      if (typeof entry !== 'string') { result.push({kind:'unreadable'}); continue; }
      try {
        const e = envelope(await read(db,entry));
        if (base64(e.salt) !== unlocked.salt) throw new Error('Different vault key');
        result.push({kind:'ready',entry,value:inspect(await decrypt(e,unlocked.key,entry))});
      } catch { result.push({kind:'unreadable'}); }
    }
    return result;
  } finally {db.close();}
}
