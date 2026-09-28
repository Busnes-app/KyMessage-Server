/// <reference types="node" />
import { test, expect, type Browser, type Page } from '@playwright/test';
import { execFile, spawn, type ChildProcess } from 'node:child_process';
import { createHash } from 'node:crypto';
import { dirname, basename } from 'node:path';
import { once } from 'node:events';
import { createServer } from 'node:net';
import type {} from '../src/device';

// Optional, independent native fixture. Never included in the ordinary proof tests
// or a deployed bundle. Setup pins and commands are in docs/MLS-INTEROP-RESEARCH.md.
function required(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`Set ${name} to the pinned local interop fixture`);
  return value;
}
const executable = required('OPENMLS_INTEROP_BIN');
const proto = required('MLS_INTEROP_PROTO');
const grpcurl = required('GRPCURL_BIN');
let port: number;
const password = 'Synthetic interop vault passphrase';
const encode = (text: string) => Buffer.from(text).toString('base64');
const hash = (wire: string) => createHash('sha256').update(Buffer.from(wire, 'base64')).digest('base64');
let server: ChildProcess;

function rpc(method: string, request: object): Promise<Record<string, unknown>> {
  return new Promise((resolve, reject) => {
    const child = execFile(grpcurl, ['-plaintext', '-max-time', '10', '-emit-defaults',
      '-import-path', dirname(proto), '-proto', basename(proto), '-d', '@',
      `127.0.0.1:${port}`, `mls_client.MLSClient/${method}`],
    { timeout: 15_000, maxBuffer: 1024 * 1024 }, (error, stdout) => {
      // Never include a complete RPC response (it may contain synthetic private keys).
      if (error) { reject(new Error(`${method} RPC failed`)); return; }
      try {
        const result: unknown = JSON.parse(stdout);
        if (typeof result !== 'object' || result === null || Array.isArray(result)) throw new Error('Expected RPC object');
        resolve(Object.fromEntries(Object.entries(result)));
      } catch { reject(new Error(`${method} returned an invalid response`)); }
    });
    child.stdin?.end(JSON.stringify(request));
  });
}
function text(result: Record<string, unknown>, key: string): string {
  const value = result[key];
  if (typeof value !== 'string') throw new Error(`Missing RPC bytes: ${key}`);
  return value;
}
function id(result: Record<string, unknown>, key: string): number {
  const value = result[key];
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) throw new Error(`Invalid RPC ID: ${key}`);
  return value;
}

test.beforeAll(async () => {
  const reservation = createServer();
  reservation.listen(0, '127.0.0.1');
  await once(reservation, 'listening');
  const address = reservation.address();
  if (!address || typeof address === 'string') throw new Error('Expected loopback port');
  port = address.port;
  await new Promise<void>((resolve, reject) => reservation.close(error => error ? reject(error) : resolve()));
  server = spawn(executable, ['--host', '127.0.0.1', '--port', String(port)],
    { stdio: 'ignore', env: { ...process.env, RUST_LOG: 'error' } });
  await once(server, 'spawn');
  await expect.poll(async () => {
    if (server.exitCode !== null) throw new Error('Owned OpenMLS fixture exited; check port availability');
    try { return text(await rpc('Name', {}), 'name'); } catch { return ''; }
  }).toContain('OpenMLS');
  expect((await rpc('SupportedCiphersuites', {})).ciphersuites).toContain(1);
});
test.afterAll(async () => {
  if (server && server.exitCode === null && server.signalCode === null) {
    const exited = once(server, 'exit');
    server.kill('SIGTERM');
    const timer = setTimeout(() => server.kill('SIGKILL'), 5_000);
    try { await exited; } finally { clearTimeout(timer); }
  }
});

async function device(browser: Browser, identity: string) {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto('/');
  await page.waitForFunction(() => Boolean(window.proof));
  const keyPackage = await page.evaluate(({ identity, password }) => window.proof.initialize(identity, password), { identity, password });
  return { context, page, keyPackage };
}
async function agree(page: Page, stateId: number) {
  const auth = await rpc('StateAuth', { state_id: stateId });
  const exporter = await rpc('Export', { state_id: stateId, label: 'kymessages-interop', context: encode('synthetic-context'), key_length: 32 });
  expect(await page.evaluate(() => window.proof.interopState())).toEqual({
    authentication: hash(text(auth, 'stateAuthSecret')), exporter: hash(text(exporter, 'exportedSecret')),
  });
}

test('OpenMLS and browser MLS exchange Welcomes, updates and removal', async ({ browser }) => {
  const alice = await device(browser, 'alice');
  const bob = await rpc('CreateKeyPackage', { cipher_suite: 1, identity: encode('bob') });
  const bobPackage = text(bob, 'keyPackage');
  await alice.page.evaluate(wire => window.proof.approve(wire), bobPackage);
  await alice.page.evaluate(() => window.proof.create());
  const addition = await alice.page.evaluate(wire => window.proof.add(wire), bobPackage);
  if (!addition.welcome) throw new Error('Missing browser Welcome');
  await alice.page.evaluate(() => window.proof.settle(true));
  let bobID = id(await rpc('JoinGroup', { transaction_id: id(bob, 'transactionId'), welcome: addition.welcome,
    identity: encode('bob'), encrypt_handshake: true }), 'stateId');
  let sequence = 0;
  const receive = (page: Page, wire: string, seq: number) => page.evaluate(({ wire, seq }) => window.proof.receive(seq, wire), { wire, seq });
  async function exchange(label: string) {
    const outbound = await alice.page.evaluate(message => window.proof.send(message), `Alice ${label}`);
    expect(text(await rpc('Unprotect', { state_id: bobID, ciphertext: outbound }), 'plaintext')).toBe(encode(`Alice ${label}`));
    await alice.page.evaluate(wire => window.proof.acknowledge(wire), outbound);
    const incoming = text(await rpc('Protect', { state_id: bobID, plaintext: encode(`Bob ${label}`) }), 'ciphertext');
    expect(await receive(alice.page, incoming, ++sequence)).toBe('applicationMessage');
    expect((await alice.page.evaluate(() => window.proof.status())).inbox.at(-1)).toBe(`Bob ${label}`);
    await agree(alice.page, bobID);
  }
  await exchange('after browser Welcome');
  await alice.page.reload();
  await alice.page.waitForFunction(() => Boolean(window.proof));
  await alice.page.evaluate(password => window.proof.unlock(password), password);
  await exchange('after browser reload');

  const update = await alice.page.evaluate(() => window.proof.update());
  bobID = id(await rpc('HandleCommit', { state_id: bobID, commit: update.wire }), 'stateId');
  await alice.page.evaluate(() => window.proof.settle(true));
  await exchange('after browser update');
  const nativeUpdate = await rpc('Commit', { state_id: bobID, force_path: true });
  await receive(alice.page, text(nativeUpdate, 'commit'), ++sequence);
  bobID = id(await rpc('HandlePendingCommit', { state_id: bobID }), 'stateId');
  await exchange('after OpenMLS update');

  const carol = await device(browser, 'carol');
  for (const keyPackage of [alice.keyPackage, bobPackage]) await carol.page.evaluate(wire => window.proof.approve(wire), keyPackage);
  await alice.page.evaluate(wire => window.proof.approve(wire), carol.keyPackage);
  const addCarol = await rpc('Commit', { state_id: bobID, force_path: true,
    by_value: [{ proposal_type: encode('add'), key_package: carol.keyPackage }] });
  await receive(alice.page, text(addCarol, 'commit'), ++sequence);
  bobID = id(await rpc('HandlePendingCommit', { state_id: bobID }), 'stateId');
  await carol.page.evaluate(wire => window.proof.join(wire), text(addCarol, 'welcome'));
  await agree(alice.page, bobID);
  await agree(carol.page, bobID);
  const shared = text(await rpc('Protect', { state_id: bobID, plaintext: encode('All three members') }), 'ciphertext');
  await receive(alice.page, shared, ++sequence);
  await receive(carol.page, shared, 1);
  expect((await carol.page.evaluate(() => window.proof.status())).inbox).toEqual(['All three members']);

  const remove = await alice.page.evaluate(() => window.proof.remove('bob'));
  await alice.page.evaluate(() => window.proof.settle(true));
  await receive(carol.page, remove.wire, 2);
  const privateWire = await alice.page.evaluate(() => window.proof.send('After removing OpenMLS Bob'));
  await expect(rpc('Unprotect', { state_id: bobID, ciphertext: privateWire })).rejects.toThrow('RPC failed');
  const damaged = Buffer.from(privateWire, 'base64');
  damaged[damaged.length - 1] = (damaged.at(-1) ?? 0) ^ 1;
  await expect(receive(carol.page, damaged.toString('base64'), 3)).rejects.toThrow();
  expect((await carol.page.evaluate(() => window.proof.status())).cursor).toBe(2);
  await receive(carol.page, privateWire, 3);
  expect(await receive(carol.page, privateWire, 3)).toBe('duplicate');
  await expect(receive(carol.page, privateWire, 4)).rejects.toThrow();
  expect((await carol.page.evaluate(() => window.proof.status())).inbox.at(-1)).toBe('After removing OpenMLS Bob');
  await alice.context.close();
  await carol.context.close();
  await rpc('Free', { state_id: bobID });
});
