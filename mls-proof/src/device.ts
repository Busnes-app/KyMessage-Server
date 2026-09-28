// Use the package's exported subpaths: its root also exports the optional noble
// provider, whose unresolved dependencies otherwise break the browser bundle.
import type { ClientConfig, ClientState, Decoder, Proposal } from 'ts-mls';
import { createApplicationMessage } from 'ts-mls/createMessage.js';
import { createCommit } from 'ts-mls/createCommit.js';
import { createGroup, decodeGroupState, encodeGroupState, joinGroup } from 'ts-mls/clientState.js';
import { decodeMlsMessage, encodeMlsMessage } from 'ts-mls/message.js';
import { defaultCapabilities } from 'ts-mls/defaultCapabilities.js';
import { defaultLifetime } from 'ts-mls/lifetime.js';
import { emptyPskIndex } from 'ts-mls/pskIndex.js';
import { generateKeyPackage } from 'ts-mls/keyPackage.js';
import { getCiphersuiteFromName } from 'ts-mls/crypto/ciphersuite.js';
import { getCiphersuiteImpl } from 'ts-mls/crypto/getCiphersuiteImpl.js';
import { processPrivateMessage } from 'ts-mls/processMessages.js';
import { zeroOutUint8Array } from 'ts-mls/util/byteArray.js';
import { mlsExporter } from 'ts-mls/keySchedule.js';
import { defaultClientConfig } from 'ts-mls/clientConfig.js';
import { base64, unbase64, initializeVault, unlockVault, withVault, type UnlockedVault, type DeviceRecord } from './vault';
import { accountID } from './delivery-wire';

export const encoder = new TextEncoder();
export const decoder = new TextDecoder('utf-8', { fatal: true });
export const suite = getCiphersuiteImpl(getCiphersuiteFromName('MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519'));
let unlocked: UnlockedVault | null = null;
let unlockGeneration = 0;

export function decode<T>(wire: string, codec: Decoder<T>): T {
  const bytes = unbase64(wire);
  const result = codec(bytes, 0);
  if (!result || result[1] !== bytes.length) throw new Error('Malformed or trailing wire bytes');
  return result[0];
}

export function keyPackage(wire: string) {
  const message = decode(wire, decodeMlsMessage);
  if (message.wireformat !== 'mls_key_package') throw new Error('Expected MLS KeyPackage');
  if (message.keyPackage.cipherSuite !== 'MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519') {
    throw new Error('Unsupported ciphersuite');
  }
  return message.keyPackage;
}

export function pinFor(wire: string) {
  const leaf = keyPackage(wire).leafNode;
  if (leaf.credential.credentialType !== 'basic') throw new Error('Expected basic credential');
  return { identity: decoder.decode(leaf.credential.identity), key: base64(leaf.signaturePublicKey), identityGeneration: 1 };
}

export function config(record: DeviceRecord): ClientConfig {
  return {
    ...defaultClientConfig,
    // This proof requires ordered delivery and keeps no prior epoch/generation keys.
    keyRetentionConfig: { retainKeysForEpochs: 0, retainKeysForGenerations: 0, maximumForwardRatchetSteps: 100 },
    authService: {
      async validateCredential(credential, signaturePublicKey) {
        return credential.credentialType === 'basic' && record.pins.some(pin =>
          pin.identity === decoder.decode(credential.identity) && pin.key === base64(signaturePublicKey));
      },
    },
  };
}

export function state(record: DeviceRecord): ClientState {
  if (record.state === null) throw new Error('No room joined');
  return { ...decode(record.state, decodeGroupState), clientConfig: config(record) };
}

function idle(record: DeviceRecord) {
  if (record.pending !== null) throw new Error('Resolve the staged commit before continuing');
  if (record.awaitingCommit) throw new Error('Apply the winning commit before continuing');
}

export function run<T>(action: (record: DeviceRecord) => Promise<T>): Promise<T> {
  if (unlocked === null) throw new Error('Device locked');
  return withVault(unlocked, action);
}

async function stage(record: DeviceRecord, proposals: Proposal[]) {
  idle(record);
  if (record.outbox.length) throw new Error('Deliver the outbox before changing epoch');
  const current = state(record);
  const result = await createCommit({ state: current, cipherSuite: await suite }, {
    extraProposals: proposals, ratchetTreeExtension: true,
  });
  record.pending = {
    state: base64(encodeGroupState(result.newState)),
    wire: base64(encodeMlsMessage(result.commit)),
    welcome: result.welcome ? base64(encodeMlsMessage({ version: 'mls10', wireformat: 'mls_welcome', welcome: result.welcome })) : null,
    epoch: current.groupContext.epoch.toString(),
  };
  result.consumed.forEach(zeroOutUint8Array);
  const { wire, welcome, epoch } = record.pending;
  return { wire, welcome, epoch };
}

export const proof = {
  // Independent published vectors use only synthetic inputs, never device state.
  async exporterVector(input: { secret: string; label: string; context: string; length: number }) {
    if (!Number.isSafeInteger(input.length) || input.length < 1 || input.length > 256) throw new Error('Invalid vector length');
    return base64(await mlsExporter(unbase64(input.secret), input.label, unbase64(input.context), input.length, await suite));
  },

  async initialize(identity: string, password: string) {
    accountID(identity);
    const generation = ++unlockGeneration;
    unlocked = null;
    const created = await initializeVault(password, async () => {
      const keys = await generateKeyPackage(
        { credentialType: 'basic', identity: encoder.encode(identity) },
        defaultCapabilities(), defaultLifetime, [], await suite,
      );
      const wire = base64(encodeMlsMessage({ version: 'mls10', wireformat: 'mls_key_package', keyPackage: keys.publicPackage }));
      return {
        version: 1, identity, keyPackage: wire, delivery: null,
        keys: {
          initPrivateKey: base64(keys.privatePackage.initPrivateKey),
          hpkePrivateKey: base64(keys.privatePackage.hpkePrivateKey),
          signaturePrivateKey: base64(keys.privatePackage.signaturePrivateKey),
        },
        pins: [pinFor(wire)], state: null, pending: null, inbox: [], outbox: [], cursor: 0, received: [], awaitingCommit: false,
      };
    });
    if (generation !== unlockGeneration) throw new Error('Device locked during setup');
    unlocked = created.unlocked;
    return created.wire;
  },

  async unlock(password: string) {
    const generation = ++unlockGeneration;
    unlocked = null;
    const next = await unlockVault(password);
    if (generation !== unlockGeneration) throw new Error('Device locked during unlock');
    unlocked = next;
  },

  lock() { unlockGeneration++; unlocked = null; },

  async inspectKeyPackage(wire: string) {
    const pin = pinFor(wire);
    const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', unbase64(pin.key)));
    return { identity: pin.identity, fingerprint: Array.from(digest, b => b.toString(16).padStart(2, '0')).join('') };
  },

  // Explicit out-of-band pinning in the experiment, not a directory-authentication claim.
  async approve(wire: string) {
    const pin = pinFor(wire);
    return run(async record => {
      const existing = record.pins.find(p => p.identity === pin.identity);
      if (existing && existing.key !== pin.key) throw new Error('Device identity key changed');
      if (!existing) record.pins.push(pin);
    });
  },

  async create() {
    return run(async record => {
      if (record.state || !record.keys) throw new Error('Device already used its KeyPackage');
      const current = await createGroup(crypto.getRandomValues(new Uint8Array(16)), keyPackage(record.keyPackage), {
        initPrivateKey: unbase64(record.keys.initPrivateKey),
        hpkePrivateKey: unbase64(record.keys.hpkePrivateKey),
        signaturePrivateKey: unbase64(record.keys.signaturePrivateKey),
      }, [], await suite, config(record));
      record.state = base64(encodeGroupState(current));
      record.keys = null;
    });
  },

  async add(wire: string) {
    const packageToAdd = keyPackage(wire);
    return run(record => stage(record, [{ proposalType: 'add', add: { keyPackage: packageToAdd } }]));
  },

  async update() { return run(record => stage(record, [])); },

  async remove(identity: string) {
    return run(record => {
      const current = state(record);
      const index = current.ratchetTree.findIndex(node => node?.nodeType === 'leaf'
        && node.leaf.credential.credentialType === 'basic'
        && decoder.decode(node.leaf.credential.identity) === identity);
      if (index < 0) throw new Error('Device is not in the room');
      return stage(record, [{ proposalType: 'remove', remove: { removed: index / 2 } }]);
    });
  },

  // The trusted test relay chooses one winner for an epoch. Commit state is applied
  // only after acceptance; a loser discards staged state and processes the winner.
  async settle(accepted: boolean) {
    return run(async record => {
      if (!record.pending) throw new Error('No staged commit');
      if (accepted) record.state = record.pending.state;
      // createCommit does not expose an advanced old-epoch handshake ratchet.
      // A discarded commit must never be regenerated in that same epoch.
      record.awaitingCommit = !accepted;
      record.pending = null;
    });
  },

  async join(wire: string) {
    const message = decode(wire, decodeMlsMessage);
    if (message.wireformat !== 'mls_welcome') throw new Error('Expected MLS Welcome');
    return run(async record => {
      if (record.state || !record.keys) throw new Error('Welcome already consumed');
      const current = await joinGroup(message.welcome, keyPackage(record.keyPackage), {
        initPrivateKey: unbase64(record.keys.initPrivateKey),
        hpkePrivateKey: unbase64(record.keys.hpkePrivateKey),
        signaturePrivateKey: unbase64(record.keys.signaturePrivateKey),
      }, emptyPskIndex, await suite, undefined, undefined, config(record));
      record.state = base64(encodeGroupState(current));
      record.keys = null;
    });
  },

  async send(text: string) {
    const bytes = encoder.encode(text);
    if (!bytes.length || bytes.length > 4096) throw new Error('Use 1–4096 bytes of test text');
    return run(async record => {
      idle(record);
      const result = await createApplicationMessage(state(record), bytes, await suite);
      const wire = base64(encodeMlsMessage({ version: 'mls10', wireformat: 'mls_private_message', privateMessage: result.privateMessage }));
      record.state = base64(encodeGroupState(result.newState));
      record.outbox.push(wire);
      result.consumed.forEach(zeroOutUint8Array);
      return wire;
    });
  },

  async acknowledge(wire: string) {
    return run(async record => { record.outbox = record.outbox.filter(item => item !== wire); });
  },

  async receive(sequence: number, wire: string) {
    if (!Number.isSafeInteger(sequence) || sequence < 1) throw new Error('Invalid delivery sequence');
    const message = decode(wire, decodeMlsMessage);
    if (message.wireformat !== 'mls_private_message') throw new Error('Expected private MLS message');
    const hash = base64(new Uint8Array(await crypto.subtle.digest('SHA-256', unbase64(wire))));
    return run(async record => {
      if (record.pending !== null) throw new Error('Resolve the staged commit before continuing');
      if (record.awaitingCommit && message.privateMessage.contentType !== 'commit') {
        throw new Error('Apply the winning commit before continuing');
      }
      if (sequence <= record.cursor) {
        if (record.received[sequence - 1] !== hash) throw new Error('Delivery sequence conflict');
        return 'duplicate';
      }
      if (sequence !== record.cursor + 1) throw new Error('Delivery gap; fetch missing events');
      const result = await processPrivateMessage(state(record), message.privateMessage, emptyPskIndex, await suite);
      if (record.awaitingCommit && result.kind !== 'newState') throw new Error('Expected winning commit');
      record.state = base64(encodeGroupState(result.newState));
      record.awaitingCommit = false;
      record.cursor = sequence;
      record.received.push(hash);
      if (result.kind === 'applicationMessage') record.inbox.push(decoder.decode(result.message));
      result.consumed.forEach(zeroOutUint8Array);
      return result.kind;
    });
  },

  async status() {
    return run(async record => {
      const current = record.state ? state(record) : null;
      return {
        identity: record.identity, keyPackage: record.keyPackage,
        epoch: current?.groupContext.epoch.toString() ?? null,
        members: current?.ratchetTree.flatMap(node => node?.nodeType === 'leaf' && node.leaf.credential.credentialType === 'basic'
          ? [decoder.decode(node.leaf.credential.identity)] : []) ?? [],
        inbox: record.inbox, outbox: record.outbox, cursor: record.cursor,
        awaitingCommit: record.awaitingCommit,
        pending: record.pending ? { wire: record.pending.wire, welcome: record.pending.welcome, epoch: record.pending.epoch } : null,
      };
    });
  },
};

declare global { interface Window { proof: typeof proof } }
