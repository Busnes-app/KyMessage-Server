import type { ClientState, Proposal } from 'ts-mls';
import { createGroup, encodeGroupState, joinGroupWithExtensions } from 'ts-mls/clientState.js';
import { createCommit } from 'ts-mls/createCommit.js';
import { createApplicationMessage } from 'ts-mls/createMessage.js';
import { decodeMlsMessage, encodeMlsMessage } from 'ts-mls/message.js';
import { processPrivateMessage } from 'ts-mls/processMessages.js';
import { unprotectPrivateMessage } from 'ts-mls/messageProtection.js';
import { emptyPskIndex } from 'ts-mls/pskIndex.js';
import { verifyKeyPackage, makeKeyPackageRef, generateKeyPackageWithKey } from 'ts-mls/keyPackage.js';
import { defaultCapabilities } from 'ts-mls/defaultCapabilities.js';
import { decryptGroupSecrets, decryptGroupInfo } from 'ts-mls/welcome.js';
import { zeroOutUint8Array } from 'ts-mls/util/byteArray.js';
import { run, runAt, ensureEntry, useEntry, selectEntry, inspectEntries, unlockedIdentity, state, config, decode, keyPackage, pinFor, suite, encoder, decoder } from './device';
import { base64, unbase64, type DeviceRecord } from './vault';
import { object, text, accountID, integer, identityGeneration, array, roster, metadata, event, connection, type Connection, type Metadata, type Event, type Roster } from './delivery-wire';
import { signedInAccount, secureFetch, SessionError } from './session';

// Experiment-specific GroupInfo extension, authenticated by MLS for Welcome joins.
const bindingExtension = 0xff01;
let connectionAbort = new AbortController();
let session: {kind:'bearer'; token:string} | {kind:'cookie'; identity:string} | null = null;
const hex = (bytes: Uint8Array) => Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
const hash = async (bytes: Uint8Array<ArrayBuffer>) => hex(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes)));

async function request(path: string, token: string, method = 'GET', body?: unknown) {
  if (!session) throw new Error('Connect a disposable SSO session');
  const auth = session;
  const signal = AbortSignal.any([connectionAbort.signal,AbortSignal.timeout(10_000)]);
  if (auth.kind === 'cookie' && (await signedInAccount(signal))?.id !== auth.identity) throw new SessionError('The signed-in account changed or expired. Sign in again, then unlock its device.');
  const headers = new Headers({'X-KyMessages-Device':token,'Content-Type':'application/json'});
  if (auth.kind === 'bearer') headers.set('Authorization','Bearer ' + auth.token);
  const response = await (auth.kind === 'cookie' ? secureFetch : fetch)('/api/messaging' + path, {
    signal, method, credentials: auth.kind === 'cookie' ? 'same-origin' : 'omit', cache:'no-store', redirect: 'error', headers,
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  if (auth.kind === 'cookie' && response.status === 401) throw new SessionError('Your session expired. Sign in again, then unlock this device.');
  const raw = await response.text();
  if (raw.length > 2 * 1024 * 1024) throw new Error('Oversized delivery response');
  const value: unknown = JSON.parse(raw);
  return { status: response.status, value };
}
async function api(path: string, token: string, method = 'GET', body?: unknown) {
  const result = await request(path, token, method, body);
  if (result.status < 200 || result.status >= 300) throw new Error(`Delivery HTTP ${result.status}: ${text(object(result.value).error)}`);
  return object(result.value);
}
function connected(record: DeviceRecord): Connection {
  if (record.delivery === null) throw new Error('Enroll delivery device first');
  const value: unknown = JSON.parse(record.delivery);
  return connection(value);
}
async function transaction<T>(action: (r: DeviceRecord, d: Connection) => Promise<T>, entry?: string) {
  const operate = (work: (r: DeviceRecord) => Promise<T>) => entry === undefined ? run(work) : runAt(entry,work);
  return operate(async r => {
    const d = connected(r);
    const result = await action(r, d);
    r.delivery = JSON.stringify(connection(d)); // Validate capacity at the persisted boundary too.
    return result;
  });
}
function roomPath(d: Connection) {
  if (!d.room) throw new Error('Select a delivery room');
  return '/rooms/' + encodeURIComponent(d.room);
}
function privateKeys(r: DeviceRecord) {
  if (!r.keys) throw new Error('KeyPackage already consumed');
  return { initPrivateKey: unbase64(r.keys.initPrivateKey), hpkePrivateKey: unbase64(r.keys.hpkePrivateKey), signaturePrivateKey: unbase64(r.keys.signaturePrivateKey) };
}
function pinned(r: DeviceRecord, devices: Roster) {
  const peer = connected(r).directPeer;
  if (peer && devices.some(device => device.user_id !== r.identity && device.user_id !== peer)) throw new Error('Direct conversation contains another account');
  for (const d of devices) {
    if (!r.pins.some(p => p.identity === d.user_id && p.key === d.public_key && p.identityGeneration === d.identity_generation)) throw new Error('Unpinned roster identity/key');
  }
}
async function rosterHash(room: string, devices: Roster, legacy = false) {
  // Match Go encoding/json's fixed struct field order and HTML-safe escaping.
  // Device IDs are server UUIDs; account IDs remain exact UTF-8, never normalized.
  const json = JSON.stringify({ Domain: legacy ? 'KyMessages roster v1' : 'KyMessages roster v2', Room: room, Devices: [...devices].sort((a,b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0).map(d => ({ ID: d.id, UserID: d.user_id, PublicKey: d.public_key, Generation: d.generation, ...(legacy ? {} : {IdentityGeneration:d.identity_generation}) })) });
  return hash(encoder.encode(json.replace(/[<>&\u2028\u2029]/g,char => '\\u' + char.charCodeAt(0).toString(16).padStart(4,'0'))));
}
function groupMatches(current: ClientState, room: string, epoch: number, devices: Roster) {
  if (decoder.decode(current.groupContext.groupId) !== room || current.groupContext.epoch !== BigInt(epoch) || current.groupActiveState.kind !== 'active') throw new Error('MLS group/epoch mismatch');
  const leaves = current.ratchetTree.flatMap(node => node?.nodeType === 'leaf' ? [node.leaf] : []);
  if (leaves.length !== devices.length || leaves.some(leaf => leaf.credential.credentialType !== 'basic' || !devices.some(d => d.public_key === base64(leaf.signaturePublicKey) && leaf.credential.credentialType === 'basic' && d.user_id === decoder.decode(leaf.credential.identity)))) throw new Error('MLS roster mismatch');
}
function senderMatches(current: ClientState, index: number, sender: string, devices: Roster) {
  const leaf = current.ratchetTree[index * 2];
  const d = devices.find(d => d.id === sender);
  if (!d || leaf?.nodeType !== 'leaf' || base64(leaf.leaf.signaturePublicKey) !== d.public_key || leaf.leaf.credential.credentialType !== 'basic' || decoder.decode(leaf.leaf.credential.identity) !== d.user_id) throw new Error('MLS sender mismatch');
}
async function checkMetadata(r: DeviceRecord, d: Connection, e: Event, m: Metadata) {
  if (m.room !== d.room || m.id !== e.id || m.device_id !== e.device_id || m.kind !== e.kind || m.epoch + (m.kind === 'commit' ? 1 : 0) !== e.epoch || m.roster_hash !== e.roster_hash || await rosterHash(m.room,m.devices,m.domain === 'KyMessages MLS proof delivery v1') !== m.roster_hash) throw new Error('Authenticated delivery metadata mismatch');
  pinned(r,m.devices);
}
async function currentMetadata(r: DeviceRecord, d: Connection, kind: 'application' | 'commit'): Promise<Metadata> {
  if (!d.room || !d.device || d.pending || d.rejoinGeneration !== null || r.awaitingCommit || r.pending || r.outbox.length) throw new Error('Resolve pending delivery first');
  const v = await api(roomPath(d) + '/delivery', d.token);
  const devices = roster(v.devices);
  const epoch = integer(v.epoch);
  if (integer(v.sequence) !== r.cursor) throw new Error('Catch up before sending');
  if (state(r).groupContext.epoch !== BigInt(epoch)) throw new Error('Local epoch differs from server');
  if (kind === 'application' && v.paused !== false) throw new Error('Roster changed; commit required');
  pinned(r,devices);
  const roster_hash = text(v.roster_hash);
  if (await rosterHash(d.room,devices) !== roster_hash) throw new Error('Directory roster hash mismatch');
  return { domain: 'KyMessages MLS proof delivery v2', room: d.room, id: crypto.randomUUID(), device_id: d.device, kind, epoch, roster_hash, devices };
}

async function freshRoomRecord(base: DeviceRecord, room: string): Promise<DeviceRecord> {
  const old = keyPackage(base.keyPackage);
  const signKey = base.keys ? unbase64(base.keys.signaturePrivateKey) : state(base).signaturePrivateKey;
  const now = Math.floor(Date.now()/1000);
  const fresh = await generateKeyPackageWithKey(old.leafNode.credential,defaultCapabilities(),
    {notBefore:BigInt(now-300),notAfter:BigInt(now+7*24*3600)},[],
    {signKey,publicKey:old.leafNode.signaturePublicKey},await suite);
  const d = connected(base);
  const record: DeviceRecord = {
    ...base, keyPackage:base64(encodeMlsMessage({version:'mls10',wireformat:'mls_key_package',keyPackage:fresh.publicPackage})),
    keys:{initPrivateKey:base64(fresh.privatePackage.initPrivateKey),hpkePrivateKey:base64(fresh.privatePackage.hpkePrivateKey),signaturePrivateKey:base64(fresh.privatePackage.signaturePrivateKey)},
    state:null,pending:null,outbox:[],inbox:[],received:[],cursor:0,awaitingCommit:false,
    delivery:JSON.stringify(connection({device:d.device,token:d.token,room,pending:null,roster:null})),
  };
  Object.values(fresh.privatePackage).forEach(zeroOutUint8Array);
  return record;
}
async function openRoom(room: string, expectedPeer?: string) {
  if (!/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(room)) throw new Error('Invalid room ID');
  const signal = connectionAbort.signal;
  const base = await runAt('device',async r => r);
  const root = connected(base);
  const entry = root.room === null || root.room === room ? 'device' : 'room:' + room;
  if (entry !== 'device') await ensureEntry(entry,() => freshRoomRecord(base,room));
  await transaction(async (r,d) => {
    if (d.room !== null && d.room !== room) throw new Error('Another tab selected the first room; try again');
    d.room = room;
    const rooms = array((await api('/rooms',d.token)).rooms,object);
    const selected = rooms.find(x => x.id === room);
    if (selected?.membership !== 'active' && selected?.membership !== 'invited') throw new Error('Room not available to this account');
    const boundPeer = selected.peer_user_id === undefined || selected.peer_user_id === '' ? null : accountID(selected.peer_user_id);
    const directPeer = boundPeer === null ? null : selected.owner_id === r.identity ? boundPeer : accountID(selected.owner_id);
    if ((expectedPeer && directPeer !== expectedPeer) || (d.directPeer && d.directPeer !== directPeer)) throw new Error('Direct conversation account binding changed');
    d.directPeer = directPeer;
    if (selected.membership === 'invited' && !r.state) await api(roomPath(d) + '/join',d.token,'POST');
    d.name = text(selected.name);
    // Recover an empty owned room after a lost creation acknowledgement. Never
    // initialize a group with join material that has already been published.
    if (!r.state && selected.owner_id === r.identity && !d.publication) {
      const remote = await api(roomPath(d) + '/delivery',d.token);
      if (integer(remote.epoch) === 0) {
        const group = await createGroup(encoder.encode(room),keyPackage(r.keyPackage),privateKeys(r),[],await suite,config(r));
        r.state = base64(encodeGroupState(group));
        r.keys = null;
      }
    }
  },entry);
  signal.throwIfAborted();
  useEntry(entry);
}

export const delivery = {
  connect(value: string) { connectionAbort.abort(); connectionAbort = new AbortController(); session = {kind:'bearer',token:value}; },
  connectCookie(identity: string) { connectionAbort.abort(); connectionAbort = new AbortController(); session = {kind:'cookie',identity}; },
  disconnect() { session = null; connectionAbort.abort(); },
  async watch(room: string, onWake: (notice: {sequence:number;epoch:number;rosterHash:string}) => void, onDisconnect: () => void) {
    if (session?.kind !== 'cookie') return () => {};
    const signal = connectionAbort.signal;
    const identity = session.identity;
    if ((await signedInAccount(signal))?.id !== identity) throw new SessionError('The signed-in account changed or expired. Sign in again, then unlock its device.');
    const access = await transaction(async (_r,d) => ({room:d.room,token:d.token}));
    signal.throwIfAborted();
    if (access.room !== room) throw new Error('Selected room changed before live connection');
    const url = new URL('/api/messaging/rooms/' + encodeURIComponent(access.room) + '/live',location.href);
    url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const socket = new WebSocket(url);
    let stopped = false;
    const timeout = setTimeout(() => { stop(); onDisconnect(); },10_000);
    const stop = () => { stopped = true; clearTimeout(timeout); access.token = ''; signal.removeEventListener('abort',stop); socket.close(); };
    signal.addEventListener('abort',stop,{once:true});
    socket.onopen = () => { if (stopped) return; socket.send(access.token); access.token = ''; };
    socket.onmessage = event => {
      if (stopped) return;
      try {
        if (typeof event.data !== 'string' || event.data.length > 1024) throw new Error('Oversized live notice');
        const raw: unknown = JSON.parse(event.data);
        const value = object(raw);
        const rosterHash = text(value.roster_hash);
        if (value.kind !== 'wake' || !/^[a-f0-9]{64}$/.test(rosterHash)) throw new Error('Invalid live notice');
        clearTimeout(timeout);
        onWake({sequence:integer(value.sequence),epoch:integer(value.epoch),rosterHash});
      } catch { stop(); onDisconnect(); }
    };
    socket.onclose = () => { clearTimeout(timeout); signal.removeEventListener('abort',stop); access.token = ''; if (!stopped) onDisconnect(); };
    return stop;
  },
  async rooms() {
    return transaction(async (_r,d) => array((await api('/rooms',d.token)).rooms, item => {
      const room = object(item);
      if (room.membership !== 'active' && room.membership !== 'invited') throw new Error('Invalid room membership');
      return {id:text(room.id),name:text(room.name),owner:accountID(room.owner_id),peer:room.peer_user_id === undefined || room.peer_user_id === '' ? null : accountID(room.peer_user_id),membership:room.membership};
    }));
  },
  async savedRooms() {
    return inspectEntries(r => {
      if (!r.delivery) return null;
      const d = connected(r);
      return d.room ? {id:d.room,name:d.name ?? d.room} : null;
    });
  },
  async selectSavedRoom(entry: string) {
    const identity = unlockedIdentity();
    await selectEntry(entry,async r => {
      const d = connected(r);
      if (r.identity !== identity || !d.room || (entry !== 'device' && entry !== 'room:' + d.room)) throw new Error('Saved conversation binding mismatch');
    });
  },
  async accountDevices() {
    return transaction(async (_r,d) => array((await api('/devices',d.token)).devices, item => {
      const device = object(item);
      if (device.status !== 'unverified' && device.status !== 'pending' && device.status !== 'approved' && device.status !== 'revoked') throw new Error('Invalid device status');
      return {id:text(device.id),status:device.status,fingerprint:text(device.fingerprint),identity_generation:identityGeneration(device.identity_generation)};
    }));
  },
  async startIdentityReset() {
    if (session?.kind !== 'cookie') throw new Error('Identity reset requires suite sign-in');
    return transaction(async (r,d) => {
      if (!d.device || d.room || r.state || d.pending) throw new Error('Use a pending replacement in a separate browser profile');
      const result = await api('/devices/' + encodeURIComponent(d.device) + '/recovery-auth',d.token,'POST',{confirm_identity_reset:true});
      return text(result.authorization_url);
    });
  },
  async revokeAccountDevice(id: string) {
    return transaction(async (_r,d) => { await api('/devices/' + encodeURIComponent(id),d.token,'DELETE'); });
  },
  async approveAccountDevice(id: string, fingerprint: string) {
    return transaction(async (_r,d) => {
      const devices = array((await api('/devices',d.token)).devices,object);
      const target = devices.find(x => x.id === id && x.status === 'pending');
      if (!target || await hash(unbase64(target.public_key)) !== fingerprint) throw new Error('Pending device fingerprint mismatch');
      await api('/devices/' + encodeURIComponent(id) + '/approve',d.token,'POST');
    });
  },
  async enroll() {
    // Save the token and then the challenge in separate durable steps. A lost
    // verification response is reconciled against the account's device registry.
    await run(async r => {
      if (!r.delivery) r.delivery = JSON.stringify({ device: null, challenge: null, token: hex(crypto.getRandomValues(new Uint8Array(32))), room: null, pending: null, roster: null });
    });
    await transaction(async (r,d) => {
      const own = pinFor(r.keyPackage);
      const listing = await api('/devices',d.token);
      const existing = array(listing.devices,object).find(v => v.public_key === own.key && v.user_id === r.identity && (v.status === 'approved' || v.status === 'pending' || v.status === 'revoked'));
      if (existing) { d.device = text(existing.id); d.challenge = null; const pin = r.pins.find(p => p.identity === r.identity && p.key === own.key); if (pin) pin.identityGeneration = identityGeneration(existing.identity_generation); return; }
      if (d.challenge) {
        const saved: unknown = JSON.parse(decoder.decode(unbase64(d.challenge)));
        if (integer(object(saved).ExpiresAt) * 1000 > Date.now()) return;
        d.challenge = null;
        d.device = null;
      }
      const tokenHash = await hash(encoder.encode(d.token));
      const v = await api('/devices', d.token, 'POST', { name: 'MLS proof', public_key: own.key, token_hash: tokenHash });
      const device = object(v.device);
      d.device = text(device.id);
      d.challenge = text(v.signing_input);
    });
    return transaction(async (r,d) => {
      if (!d.device) throw new Error('Missing enrolled device ID');
      if (!d.challenge) return d.device;
      const own = pinFor(r.keyPackage);
      const bytes = unbase64(d.challenge);
      const parsed: unknown = JSON.parse(decoder.decode(bytes));
      const c = object(parsed);
      if (c.Domain !== 'KyMessages enrollment v1' || c.Origin !== location.origin || c.UserID !== r.identity || c.DeviceID !== d.device || c.PublicKey !== own.key || c.TokenHash !== await hash(encoder.encode(d.token)) || integer(c.ExpiresAt) * 1000 <= Date.now()) throw new Error('Enrollment binding mismatch');
      const signature = base64(await (await suite).signature.sign(privateKeys(r).signaturePrivateKey,bytes));
      const verified = object((await api('/devices/' + encodeURIComponent(d.device) + '/verify',d.token,'POST',{signature})).device);
      const pin = r.pins.find(p => p.identity === r.identity && p.key === own.key);
      if (pin) pin.identityGeneration = identityGeneration(verified.identity_generation);
      d.challenge = null;
      return d.device;
    });
  },
  async createRoom(name: string) {
    const signal = connectionAbort.signal;
    const token = await transaction(async (_r,d) => d.token);
    const v = await api('/rooms',token,'POST',{name});
    signal.throwIfAborted();
    const room = text(v.id);
    await openRoom(room);
    return room;
  },
  async directRoom(peerValue: string) {
    const peer = accountID(peerValue);
    const identity = unlockedIdentity();
    if (peer === identity) throw new Error('Choose another account for a direct conversation');
    const rooms = await delivery.rooms();
    // Concurrent starts may create separate conversations; each stays limited to
    // the same two accounts. Reopen the oldest visible match on ordinary retries.
    const existing = rooms.find(room => (room.owner === identity && room.peer === peer) || (room.owner === peer && room.peer === identity));
    if (existing) { await openRoom(existing.id,peer); return existing.id; }
    const signal = connectionAbort.signal;
    const token = await transaction(async (_r,d) => d.token);
    const created = await api('/rooms',token,'POST',{name:'Direct: ' + peer,peer_user_id:peer});
    signal.throwIfAborted();
    const room = text(created.id);
    await openRoom(room,peer);
    return room;
  },
  async selectRoom(room: string) { await openRoom(room); },
  async rejoin() {
    return transaction(async (r,d) => {
      if (!r.state || !d.room || !d.device) throw new Error('Rejoin requires an existing room');
      if (d.pending || r.pending || r.outbox.length || r.awaitingCommit) throw new Error('Resolve pending delivery before rejoining');
      if (d.rejoinGeneration !== null) return;
      const previous = d.roster?.find(x => x.id === d.device);
      if (!previous) throw new Error('Missing verified membership generation');
      const rooms = array((await api('/rooms',d.token)).rooms,object);
      const selected = rooms.find(x => x.id === d.room);
      if (selected?.membership === 'invited') await api(roomPath(d) + '/join',d.token,'POST');
      else if (selected?.membership !== 'active') throw new Error('A new room invitation is required');
      const directory = await api(roomPath(d) + '/delivery',d.token);
      const own = roster(directory.devices).find(x => x.id === d.device);
      const old = keyPackage(r.keyPackage);
      if (!own || own.user_id !== r.identity || own.public_key !== base64(old.leafNode.signaturePublicKey) || own.generation <= previous.generation) throw new Error('Rejoin requires a newer membership generation');
      const now = Math.floor(Date.now()/1000);
      const fresh = await generateKeyPackageWithKey(old.leafNode.credential,defaultCapabilities(),
        {notBefore:BigInt(now-300),notAfter:BigInt(now+7*24*3600)},[],
        {signKey:state(r).signaturePrivateKey,publicKey:old.leafNode.signaturePublicKey},await suite);
      r.keyPackage = base64(encodeMlsMessage({version:'mls10',wireformat:'mls_key_package',keyPackage:fresh.publicPackage}));
      r.keys = {initPrivateKey:base64(fresh.privatePackage.initPrivateKey),hpkePrivateKey:base64(fresh.privatePackage.hpkePrivateKey),signaturePrivateKey:base64(fresh.privatePackage.signaturePrivateKey)};
      Object.values(fresh.privatePackage).forEach(zeroOutUint8Array);
      d.publication = null;
      d.rejoinGeneration = own.generation;
      // Keep the old ratchet, cursor and history until a verified Welcome replaces it.
    });
  },
  async invite(user_id: string) { return transaction(async (_r,d) => { await api(roomPath(d) + '/members',d.token,'POST',{user_id}); }); },
  async members() {
    return transaction(async (_r,d) => array((await api(roomPath(d) + '/members',d.token)).members,item => {
      const member = object(item);
      if (member.status !== 'active' && member.status !== 'invited' && member.status !== 'removed') throw new Error('Invalid member status');
      return {id:accountID(member.user_id),status:member.status,identityGeneration:identityGeneration(member.identity_generation),currentIdentityGeneration:identityGeneration(member.current_identity_generation)};
    }));
  },
  async removeMember(user: string) {
    return transaction(async (r,d) => {
      if (d.pending || r.pending || r.outbox.length || r.awaitingCommit || d.rejoinGeneration !== null) throw new Error('Resolve pending delivery before removing a member');
      await api(roomPath(d) + '/members/' + encodeURIComponent(user),d.token,'DELETE');
    });
  },
  async ownFingerprint() { return run(r => hash(unbase64(pinFor(r.keyPackage).key))); },
  async directory() {
    return transaction(async (_r,d) => {
      const current = await api(roomPath(d) + '/delivery',d.token);
      if (typeof current.paused !== 'boolean') throw new Error('Invalid room pause state');
      const peers = await Promise.all(roster(current.devices).map(async device => ({id:device.id,user_id:device.user_id,identity_generation:device.identity_generation,fingerprint:await hash(unbase64(device.public_key)),approved:_r.pins.some(p => p.identity === device.user_id && p.key === device.public_key && p.identityGeneration === device.identity_generation)})));
      return {peers,paused:current.paused,room:d.room,rosterHash:text(current.roster_hash)};
    });
  },
  async approveDevice(device: string, expectedFingerprint: string) {
    return transaction(async (r,d) => {
      const current = await api(roomPath(d) + '/delivery',d.token);
      const target = roster(current.devices).find(x => x.id === device);
      if (!target || await hash(unbase64(target.public_key)) !== expectedFingerprint) throw new Error('Device fingerprint mismatch');
      if (!r.pins.some(p => p.identity === target.user_id && p.key === target.public_key && p.identityGeneration === target.identity_generation)) r.pins.push({identity:target.user_id,key:target.public_key,identityGeneration:target.identity_generation});
    });
  },
  async publishKeyPackage() {
    await transaction(async (r,d) => {
      if (!d.room) throw new Error('Select a room before publishing join material');
      if ((r.state && d.rejoinGeneration === null) || !r.keys) throw new Error('KeyPackage already consumed');
      if (d.publication && d.publication.expires_at*1000 <= Date.now()) {
        // Expiry prevents new allocation, but an earlier claim may already have
        // produced a Welcome. Keep its private join material until a verified join.
        if (d.joinPackages.length >= 16) throw new Error('Pending join package limit reached; check messages before renewing');
        const old = keyPackage(r.keyPackage);
        const now = Math.floor(Date.now()/1000);
        const fresh = await generateKeyPackageWithKey(old.leafNode.credential,defaultCapabilities(),
          {notBefore:BigInt(now-300),notAfter:BigInt(now+7*24*3600)},[],
          {signKey:unbase64(r.keys.signaturePrivateKey),publicKey:old.leafNode.signaturePublicKey},await suite);
        d.joinPackages.push({payload:r.keyPackage,...r.keys});
        r.keyPackage = base64(encodeMlsMessage({version:'mls10',wireformat:'mls_key_package',keyPackage:fresh.publicPackage}));
        r.keys = {initPrivateKey:base64(fresh.privatePackage.initPrivateKey),hpkePrivateKey:base64(fresh.privatePackage.hpkePrivateKey),signaturePrivateKey:base64(fresh.privatePackage.signaturePrivateKey)};
        Object.values(fresh.privatePackage).forEach(zeroOutUint8Array);
        d.publication = null;
      }
      if (!d.publication) d.publication = {payload:r.keyPackage,expires_at:Math.floor(Date.now()/1000)+3600,room_id:d.room};
    });
    return transaction(async (_r,d) => {
      if (!d.publication) throw new Error('Missing publication');
      const result = await api('/devices/key-packages',d.token,'POST',d.publication);
      if (text(result.package_id) !== await hash(unbase64(d.publication.payload))) throw new Error('Publication digest mismatch');
      return text(result.package_id);
    });
  },
  async stageCommit() {
    // Persist claim IDs before taking a one-time package. A failed response or
    // vault write retries the same claim rather than draining another package.
    const targets = await transaction(async (r,d) => {
      const m = await currentMetadata(r,d,'commit');
      const current = state(r);
      const targets = m.devices.filter(target => {
        const exists = current.ratchetTree.some(node => node?.nodeType === 'leaf' && base64(node.leaf.signaturePublicKey) === target.public_key);
        const old = d.roster?.find(x => x.id === target.id);
        return !exists || (old !== undefined && old.generation !== target.generation);
      });
      for (const target of targets) {
        const previous = d.claims.find(c => c.device === target.id && c.generation === target.generation);
        if (previous?.payload && previous.expires_at*1000 <= Date.now()) {
          previous.request_id = crypto.randomUUID();
          previous.payload = null;
          previous.expires_at = 0;
        }
        if (!previous) d.claims.push({device:target.id,generation:target.generation,request_id:crypto.randomUUID(),payload:null,expires_at:0});
      }
      return targets;
    });
    for (const target of targets) {
      const retryClaim = await transaction(async (r,d) => {
        const claim = d.claims.find(c => c.device === target.id && c.generation === target.generation);
        if (!claim) throw new Error('Missing durable claim');
        if (claim.payload) return;
        pinned(r,[target]);
        const response = await request(roomPath(d) + '/key-packages/claim',d.token,'POST',{device_id:target.id,request_id:claim.request_id});
        if (response.status === 409) {
          // A lost allocation response can leave expiry unknown. Only an explicit
          // server conflict retires that request; network failures keep its ID.
          claim.request_id = crypto.randomUUID();
          return true;
        }
        if (response.status !== 200) throw new Error(`Delivery HTTP ${response.status}: ${text(object(response.value).error)}`);
        const value = object(response.value);
        const wire = text(value.payload);
        const pin = pinFor(wire);
        if (text(value.device_id) !== target.id || text(value.package_id) !== await hash(unbase64(wire)) || integer(value.expires_at)*1000 <= Date.now() || pin.identity !== target.user_id || pin.key !== target.public_key || !await verifyKeyPackage(keyPackage(wire),(await suite).signature)) throw new Error('Invalid bound KeyPackage response');
        claim.payload = wire;
        claim.expires_at = integer(value.expires_at);
        return false;
      });
      if (retryClaim) throw new Error('Previous claim is no longer usable; retry membership with a fresh join package');
    }
    return transaction(async (r,d) => {
      const m = await currentMetadata(r,d,'commit');
      const current = state(r);
      const proposals: Proposal[] = [];
      const existing = new Set<string>();
      current.ratchetTree.forEach((node,index) => {
        if (node?.nodeType !== 'leaf') return;
        const key = base64(node.leaf.signaturePublicKey);
        const target = m.devices.find(x => x.public_key === key);
        const old = d.roster?.find(x => x.public_key === key);
        if (!target || (old && target.generation !== old.generation)) proposals.push({ proposalType: 'remove', remove: { removed: index/2 } });
        else existing.add(key);
      });
      const added: string[] = [];
      for (const target of m.devices) {
        if (existing.has(target.public_key)) continue;
        const claim = d.claims.find(c => c.device === target.id && c.generation === target.generation);
        if (!claim?.payload || claim.expires_at*1000 <= Date.now()) throw new Error('Missing or expired claimed KeyPackage');
        const wire = claim.payload;
        const pin = pinFor(wire);
        if (pin.identity !== target.user_id || pin.key !== target.public_key) throw new Error('Claimed KeyPackage identity changed');
        proposals.push({ proposalType: 'add', add: { keyPackage: keyPackage(wire) } });
        added.push(target.id);
      }
      const aad = encoder.encode(JSON.stringify(m));
      const result = await createCommit({ state: current, cipherSuite: await suite }, { extraProposals: proposals, ratchetTreeExtension: true, authenticatedData: aad, groupInfoExtensions: [{extensionType: bindingExtension,extensionData: aad}] });
      groupMatches(result.newState,m.room,m.epoch+1,m.devices);
      const welcomes: Record<string,string> = {};
      if (added.length) {
        if (!result.welcome) throw new Error('Missing Welcome');
        const wire = base64(encodeMlsMessage({version:'mls10',wireformat:'mls_welcome',welcome:result.welcome}));
        for (const id of added) welcomes[id] = wire;
      }
      d.pending = {request: JSON.stringify({id:m.id,kind:m.kind,epoch:m.epoch,roster_hash:m.roster_hash,payload:base64(encodeMlsMessage(result.commit)),welcomes}), state:base64(encodeGroupState(result.newState)),plaintext:null};
      result.consumed.forEach(zeroOutUint8Array);
    });
  },
  async stageSend(message: string) {
    const bytes = encoder.encode(message);
    if (!bytes.length || bytes.length > 4096) throw new Error('Use 1–4096 bytes of test text');
    return transaction(async (r,d) => {
      const m = await currentMetadata(r,d,'application');
      const current = state(r);
      groupMatches(current,m.room,m.epoch,m.devices);
      const result = await createApplicationMessage(current,bytes,await suite,encoder.encode(JSON.stringify(m)));
      r.state = base64(encodeGroupState(result.newState));
      d.pending = {request:JSON.stringify({id:m.id,kind:m.kind,epoch:m.epoch,roster_hash:m.roster_hash,payload:base64(encodeMlsMessage({version:'mls10',wireformat:'mls_private_message',privateMessage:result.privateMessage})),welcomes:{}}),state:null,plaintext:message};
      result.consumed.forEach(zeroOutUint8Array);
    });
  },
  async submit() {
    const result = await transaction(async (r,d) => {
      if (!d.pending) throw new Error('No pending delivery');
      const body: unknown = JSON.parse(d.pending.request);
      const result = await request(roomPath(d) + '/events',d.token,'POST',body);
      if (result.status === 409) { d.pending = null; r.awaitingCommit = true; }
      else if (result.status !== 200) throw new Error(`Delivery HTTP ${result.status}: ${text(object(result.value).error)}`);
      return result;
    });
    if (result.status === 409) throw new Error('Commit conflict; apply winner before retry');
    const receipt = object(result.value);
    return {sequence:integer(receipt.sequence),epoch:integer(receipt.epoch)};
  },
  async sync() {
    return transaction(async (r,d) => {
      const page = await api(roomPath(d) + '/events?after=' + r.cursor,d.token);
      const events = array(page.events,event);
      const floor = integer(page.start_sequence);
      for (const e of events) {
        const joining = r.state === null || d.rejoinGeneration !== null;
        if (e.sequence !== (joining ? floor : r.cursor+1)) throw new Error('Delivery sequence gap');
        const msg = decode(e.payload,decodeMlsMessage);
        if (msg.wireformat !== 'mls_private_message') throw new Error('Expected private MLS wire');
        const parsed: unknown = JSON.parse(decoder.decode(msg.privateMessage.authenticatedData));
        const m = metadata(parsed);
        await checkMetadata(r,d,e,m);
        if (decoder.decode(msg.privateMessage.groupId) !== d.room || msg.privateMessage.epoch !== BigInt(m.epoch) || msg.privateMessage.contentType !== e.kind) throw new Error('MLS wire metadata mismatch');
        if (joining) {
          if (!d.room || e.kind !== 'commit' || !e.welcome) throw new Error('Expected initial Welcome');
          if (d.rejoinGeneration !== null) {
            const own = m.devices.find(x => x.id === d.device);
            if (!own || own.generation !== d.rejoinGeneration || e.sequence <= r.cursor || BigInt(e.epoch) <= state(r).groupContext.epoch) throw new Error('Rejoin history or generation mismatch');
          }
          const welcome = decode(e.welcome,decodeMlsMessage);
          if (welcome.wireformat !== 'mls_welcome') throw new Error('Expected MLS Welcome');
          const cs = await suite;
          const candidates = [{payload:r.keyPackage,...privateKeys(r)}, ...d.joinPackages.map(saved => ({
            payload:saved.payload,initPrivateKey:unbase64(saved.initPrivateKey),hpkePrivateKey:unbase64(saved.hpkePrivateKey),signaturePrivateKey:unbase64(saved.signaturePrivateKey),
          }))];
          const matches = [];
          for (const candidate of candidates) {
            const ref = base64(await makeKeyPackageRef(keyPackage(candidate.payload),cs.hash));
            if (welcome.welcome.secrets.some(secret => base64(secret.newMember) === ref)) matches.push(candidate);
          }
          if (matches.length !== 1 || !matches[0]) throw new Error('Welcome must match exactly one retained join package');
          const keys = matches[0];
          const kp = keyPackage(keys.payload);
          // Use library decoders to expose the GroupInfo signer; joinGroup performs
          // its complete signature, tree and confirmation validation independently.
          const secrets = await decryptGroupSecrets(await cs.hpke.importPrivateKey(keys.initPrivateKey),await makeKeyPackageRef(kp,cs.hash),welcome.welcome,cs.hpke);
          if (!secrets || secrets.psks.length) throw new Error('Proof Welcome must not use PSKs');
          const info = await decryptGroupInfo(welcome.welcome,secrets.joinerSecret,new Uint8Array(cs.kdf.size),cs);
          secrets.joinerSecret.fill(0);
          if (!info) throw new Error('Missing GroupInfo');
          const [current,extensions] = await joinGroupWithExtensions(welcome.welcome,kp,keys,emptyPskIndex,cs,undefined,undefined,config(r));
          const bindings = extensions.filter(x => x.extensionType === bindingExtension);
          if (bindings.length !== 1 || base64(bindings[0]?.extensionData ?? new Uint8Array()) !== base64(msg.privateMessage.authenticatedData)) throw new Error('Welcome metadata mismatch');
          senderMatches(current,info.signer,e.device_id,m.devices);
          groupMatches(current,d.room,e.epoch,m.devices);
          r.state = base64(encodeGroupState(current)); r.keys = null;
          // A newer unclaimed package can still be offered on a later rejoin.
          // Retain unused material only; never retain the package just consumed.
          d.joinPackages = candidates.filter(candidate => candidate !== keys).map(candidate => ({
            payload:candidate.payload,initPrivateKey:base64(candidate.initPrivateKey),hpkePrivateKey:base64(candidate.hpkePrivateKey),signaturePrivateKey:base64(candidate.signaturePrivateKey),
          }));
          d.publication = null;
          d.rejoinGeneration = null;
        } else if (e.device_id === d.device) {
          if (!d.pending) throw new Error('Own event has no durable outbox');
          const body = object(JSON.parse(d.pending.request));
          if (body.id !== e.id || body.payload !== e.payload || body.roster_hash !== e.roster_hash) throw new Error('Own event differs from durable outbox');
          if (d.pending.state) r.state = d.pending.state;
          if (!d.room) throw new Error('Missing room');
          groupMatches(state(r),d.room,e.epoch,m.devices);
          if (e.kind === 'application' && d.pending.plaintext !== null) d.messages.push({id:e.id,sender:r.identity,text:d.pending.plaintext,sequence:e.sequence});
          d.pending = null;
        } else {
          if (d.pending && e.kind === 'commit') throw new Error('Resolve pending submission before winning commit');
          const current = state(r);
          if (e.kind === 'application') {
            const result = await unprotectPrivateMessage(current.keySchedule.senderDataSecret,msg.privateMessage,current.secretTree,current.ratchetTree,current.groupContext,current.clientConfig.keyRetentionConfig,await suite);
            const content = result.content.content;
            if (content.contentType !== 'application' || content.sender.senderType !== 'member') throw new Error('Expected member application');
            senderMatches(current,content.sender.leafIndex,e.device_id,m.devices);
            groupMatches(current,m.room,e.epoch,m.devices);
            const plaintext = decoder.decode(content.applicationData);
            r.inbox.push(plaintext);
            const sender = m.devices.find(x => x.id === e.device_id);
            if (!sender) throw new Error('Missing authenticated sender');
            d.messages.push({id:e.id,sender:sender.user_id,text:plaintext,sequence:e.sequence});
            r.state = base64(encodeGroupState({...current,secretTree:result.tree}));
            result.consumed.forEach(zeroOutUint8Array);
          } else {
            const result = await processPrivateMessage(current,msg.privateMessage,emptyPskIndex,await suite,incoming => {
              if (incoming.kind !== 'commit' || incoming.senderLeafIndex === undefined) throw new Error('Expected member commit');
              senderMatches(current,incoming.senderLeafIndex,e.device_id,m.devices);
              return 'accept';
            });
            if (result.kind !== 'newState') throw new Error('Expected MLS transition');
            groupMatches(result.newState,m.room,e.epoch,m.devices);
            r.state = base64(encodeGroupState(result.newState));
            result.consumed.forEach(zeroOutUint8Array);
          }
        }
        if (e.kind === 'commit') r.awaitingCommit = false;
        r.cursor = e.sequence;
        d.roster = m.devices;
      }
      if (integer(page.next) !== r.cursor) throw new Error('Invalid delivery cursor');
      return {cursor:r.cursor,inbox:r.inbox};
    });
  },
  async status() { return transaction(async (r,d) => ({identity:r.identity,device:d.device,room:d.room,name:d.name,rejoining:d.rejoinGeneration !== null,pending:d.pending !== null,pendingText:d.pending?.plaintext ?? null,messages:d.messages,cursor:r.cursor,inbox:r.inbox,epoch:r.state ? state(r).groupContext.epoch.toString() : null})); },
};
declare global { interface Window { delivery: typeof delivery } }
