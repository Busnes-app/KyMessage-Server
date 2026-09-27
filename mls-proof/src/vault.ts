// Experiment only: one device per browser profile, one encrypted IndexedDB record.
// A user-supplied passphrase stays in memory; the wrapping key is never persisted.
const encoder = new TextEncoder();
const decoder = new TextDecoder('utf-8', { fatal: true });
const aad = encoder.encode('kymessages-mls-proof/v1');
export const databaseName = 'kymessages-mls-proof-v1';
const maxBytes = 2 * 1024 * 1024;

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
    inbox: list(r.inbox, string),
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

async function read(db: IDBDatabase): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const transaction = db.transaction('vault', 'readonly');
    const request = transaction.objectStore('vault').get('device');
    transaction.oncomplete = () => resolve(request.result);
    transaction.onabort = () => reject(transaction.error ?? new Error('Read aborted'));
  });
}

async function write(db: IDBDatabase, value: unknown): Promise<void> {
  return new Promise((resolve, reject) => {
    const transaction = db.transaction('vault', 'readwrite', { durability: 'strict' });
    transaction.objectStore('vault').put(value, 'device');
    transaction.oncomplete = () => resolve();
    transaction.onabort = () => reject(transaction.error ?? new Error('Write aborted'));
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

// The MLS ratchet is one shared invariant. Separate profiles own separate records;
// tabs of the SAME device lock, reload, transform, and atomically replace its record.
export async function withVault<T>(passphrase: string, action: (record: DeviceRecord) => Promise<T>): Promise<T> {
  return navigator.locks.request(databaseName, async () => {
    const db = await openDatabase();
    try {
      const envelope = object(await read(db));
      if (envelope.version !== 1) throw new Error('Unknown vault version');
      const salt = unbase64(envelope.salt);
      const iv = unbase64(envelope.iv);
      if (salt.length !== 16 || iv.length !== 12) throw new Error('Invalid vault salt/IV');
      const key = await derive(passphrase, salt);
      const plaintext = await crypto.subtle.decrypt(
        { name: 'AES-GCM', iv, additionalData: aad }, key, unbase64(envelope.ciphertext),
      );
      const parsed: unknown = JSON.parse(decoder.decode(plaintext));
      new Uint8Array(plaintext).fill(0);
      const record = parseRecord(parsed);
      const result = await action(record);
      await seal(db, record, salt, key);
      return result; // Never release wire bytes before the ratchet/outbox commit.
    } finally {
      db.close();
    }
  });
}

async function seal(db: IDBDatabase, record: DeviceRecord, salt: Uint8Array<ArrayBuffer>, key: CryptoKey) {
  parseRecord(record); // Enforce proof capacity before committing, including outgoing lists.
  const plaintext = encoder.encode(JSON.stringify(record));
  if (plaintext.length > maxBytes) throw new Error('Proof vault capacity reached');
  const iv = crypto.getRandomValues(new Uint8Array(12));
  try {
    const ciphertext = await crypto.subtle.encrypt({ name: 'AES-GCM', iv, additionalData: aad }, key, plaintext);
    await write(db, { version: 1, salt: base64(salt), iv: base64(iv), ciphertext: base64(new Uint8Array(ciphertext)) });
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
      await seal(db, record, salt, key);
      return record.keyPackage;
    } finally {
      db.close();
    }
  });
}
