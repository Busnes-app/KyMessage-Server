// usage: node agent.mjs AGENT OWNER DEVICE MESSAGE REPLY
// The agent side of scripts/matrix-acceptance.sh's agents step: matrix-js-sdk with Rust crypto,
// signed in with the console's token alone ($ACCEPT_DIR/state/agent.token). whoami must be
// AGENT on DEVICE. A first run sets up cross-signing (no interactive auth), secret storage and
// key backup; every run joins OWNER's invites, waits for OWNER's MESSAGE decrypted from an
// encrypted event, replies REPLY and exits 0 once the reply is sent. The crypto store
// is saved to state/agent.crypto (0600) on exit and loaded by the next run, as a real agent
// keeps its store across restarts. *.kymatrix.test resolves to the harness proxy; the harness
// passes its CA in NODE_EXTRA_CA_CERTS.
import 'fake-indexeddb/auto';
import dns from 'node:dns';
import fs from 'node:fs';
import v8 from 'node:v8';
import { ClientEvent, createClient, MatrixEventEvent, RoomEvent } from 'matrix-js-sdk';

const { ACCEPT_DIR: dir, ACCEPT_PORT: port } = process.env;
const [agent, owner, device, want, reply] = process.argv.slice(2);
const baseUrl = `https://matrix.kymatrix.test:${port}`;
const token = fs.readFileSync(`${dir}/state/agent.token`, 'utf8').trim();
const storeFile = `${dir}/state/agent.crypto`;
const keyFile = `${dir}/state/agent.identity`;
// Only this script's lines on stdout; the SDK's warnings and errors still reach stderr.
const say = console.log;
console.log = console.info = console.debug = () => {};
const lookup = dns.lookup;
dns.lookup = (host, opts, cb) => lookup(host.endsWith('.kymatrix.test') ? '127.0.0.1' : host, opts, cb);
setTimeout(() => {
  console.error('agent: no reply sent within 240s');
  process.exit(1);
}, 240000);

function check(ok, what) {
  if (!ok) throw new Error(`check failed: ${what}`);
  say(`  ok: ${what}`);
}
const req = (r) => new Promise((ok, no) => { r.onsuccess = () => ok(r.result); r.onerror = () => no(r.error); });

// The whole fake IndexedDB: every database, store, index and record.
async function save() {
  const dbs = [];
  for (const { name, version } of await indexedDB.databases()) {
    const db = await req(indexedDB.open(name));
    const stores = [];
    for (const s of db.objectStoreNames) {
      const os = db.transaction(s).objectStore(s);
      const indexes = [...os.indexNames].map((n) => { const i = os.index(n); return { name: n, keyPath: i.keyPath, unique: i.unique, multiEntry: i.multiEntry }; });
      const [keys, values] = await Promise.all([req(os.getAllKeys()), req(os.getAll())]);
      stores.push({ name: s, keyPath: os.keyPath, autoIncrement: os.autoIncrement, indexes, keys, values });
    }
    db.close();
    dbs.push({ name, version, stores });
  }
  fs.writeFileSync(storeFile, v8.serialize(dbs), { mode: 0o600 });
}

async function load() {
  for (const d of v8.deserialize(fs.readFileSync(storeFile))) {
    const open = indexedDB.open(d.name, d.version);
    open.onupgradeneeded = () => {
      for (const s of d.stores) {
        const os = open.result.createObjectStore(s.name, { keyPath: s.keyPath ?? undefined, autoIncrement: s.autoIncrement });
        for (const i of s.indexes) os.createIndex(i.name, i.keyPath, { unique: i.unique, multiEntry: i.multiEntry });
      }
    };
    const db = await req(open);
    if (d.stores.length) {
      const tx = db.transaction(d.stores.map((s) => s.name), 'readwrite');
      for (const s of d.stores) {
        const os = tx.objectStore(s.name);
        s.values.forEach((v, i) => (s.keyPath === null ? os.put(v, s.keys[i]) : os.put(v)));
      }
      await new Promise((ok, no) => { tx.oncomplete = ok; tx.onerror = () => no(tx.error); });
    }
    db.close();
  }
}

const api = async (path, body) => {
  const r = await fetch(`${baseUrl}/_matrix/client/v3/${path}`, {
    method: body ? 'POST' : 'GET',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: body && JSON.stringify(body),
  });
  return r.json();
};

const who = await api('account/whoami');
check(who.user_id === agent && who.device_id === device, `whoami with the token alone: ${who.user_id} on device ${who.device_id}`);

const restored = fs.existsSync(storeFile);
if (restored) await load();
let ssKey;
const client = createClient({
  baseUrl, accessToken: token, userId: agent, deviceId: device,
  // Asked only while storing secrets in this run, for the key it just created.
  cryptoCallbacks: { getSecretStorageKey: async ({ keys }) => (ssKey ? [Object.keys(keys)[0], ssKey.privateKey] : null) },
});
await client.initRustCrypto({ cryptoDatabasePrefix: 'kymatrix-agent' });
const crypto = client.getCrypto();
const { ed25519 } = await crypto.getOwnDeviceKeys();
if (restored) check(ed25519 === fs.readFileSync(keyFile, 'utf8'), 'crypto store loaded: the same device identity key as the first run');
else fs.writeFileSync(keyFile, ed25519);

if (!(await crypto.isCrossSigningReady())) {
  await crypto.bootstrapCrossSigning({ authUploadDeviceSigningKeys: async (makeRequest) => { await makeRequest(null); } });
  await crypto.bootstrapSecretStorage({
    createSecretStorageKey: async () => (ssKey = await crypto.createRecoveryKeyFromPassphrase()),
    setupNewSecretStorage: true,
    setupNewKeyBackup: true,
  });
  check(await crypto.isCrossSigningReady(), 'cross-signing set up with no interactive auth');
}
check(await crypto.isSecretStorageReady(), 'secret storage holds the cross-signing keys and the backup key');
check((await crypto.getActiveSessionBackupVersion()) !== null, 'key backup active');

// Owners see the agent's device keys before it joins, so their first message is encrypted to it.
async function keysUploaded() {
  for (let i = 0; i < 30; i++) {
    const r = await api('keys/query', { device_keys: { [agent]: [device] } });
    if (r.device_keys?.[agent]?.[device]) return;
    await new Promise((ok) => setTimeout(ok, 1000));
  }
  throw new Error('device keys never reached the server');
}

let replied = false;
const done = new Promise((resolve, reject) => {
  // The first sync replays old invites; the server says which rooms are already joined.
  const join = (room) => {
    if (room.getMyMembership() !== 'invite' || room.getMember(agent)?.events.member?.getSender() !== owner) return;
    api('joined_rooms')
      .then(({ joined_rooms: joined }) => joined.includes(room.roomId) || keysUploaded()
        .then(() => client.joinRoom(room.roomId))
        .then(() => say(`  ok: joined ${room.roomId} on ${owner}'s invite`)))
      .catch(reject);
  };
  client.once(ClientEvent.Sync, () => {
    client.getRooms().forEach(join);
    client.on(RoomEvent.MyMembership, join);
  });
  client.on(MatrixEventEvent.Decrypted, (ev) => {
    if (replied || ev.isDecryptionFailure() || !ev.isEncrypted() || ev.getSender() !== owner || ev.getType() !== 'm.room.message' || ev.getContent().body !== want) return;
    replied = true;
    say(`  ok: decrypted ${owner}'s ${want}`);
    client.sendTextMessage(ev.getRoomId(), reply).then(resolve, reject);
  });
});
await client.startClient({ initialSyncLimit: 20 });
const { event_id: sent } = await done;
check(sent?.startsWith('$'), `replied ${reply} as ${sent}`);
client.stopClient();
await save();
process.exit(0);
