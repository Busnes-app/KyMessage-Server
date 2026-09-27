import { test, expect, type Browser, type Page } from '@playwright/test';
import type {} from '../src/device';
import { base64, unbase64 } from '../src/vault';

const password = 'synthetic test passphrase, never a real secret';

test('matches the MLS working group suite-1 exporter vector in the browser', async ({ page }) => {
  // Source: mlswg/mls-implementations, fd51ea702fe637e48452b8118123bff118767731,
  // test-vectors/key-schedule.json, first case, epoch 0. The label is literal text.
  const hexBase64 = (hex: string) => base64(Uint8Array.from(hex.match(/../g) ?? [], pair => parseInt(pair, 16)));
  await page.goto('/');
  await page.waitForFunction(() => Boolean(window.proof));
  const result = await page.evaluate(input => window.proof.exporterVector(input), {
    secret: hexBase64('5a097e149f2a375d0b9e1d1f4dc3a9c6c1788df888e5441f41a8791f4dc56cea'),
    label: '9ba13d54ecdec7cbefcb47b4268d7b1990fabc6d6e67681e167959389d84e4e4',
    context: hexBase64('884f1af892ab002f5be4c5d5081ade9e0e6418c6ea7a9a92e90534f19dcef785'),
    length: 32,
  });
  expect(result).toBe(hexBase64('dbce4e25e59ab4dfa6f6200f113ed08393cf6e7286d024811141c6a4dd11c0cb'));
});

async function device(browser: Browser, identity: string) {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto('/');
  await page.waitForFunction(() => Boolean(window.proof));
  const keyPackage = await page.evaluate(({ identity, password }) => window.proof.initialize(identity, password), { identity, password });
  return { context, page, keyPackage };
}

async function pair(browser: Browser) {
  const alice = await device(browser, 'alice');
  const bob = await device(browser, 'bob');
  await alice.page.evaluate(wire => window.proof.approve(wire), bob.keyPackage);
  await bob.page.evaluate(wire => window.proof.approve(wire), alice.keyPackage);
  await alice.page.evaluate(() => window.proof.create());
  const addition = await alice.page.evaluate(wire => window.proof.add(wire), bob.keyPackage);
  if (!addition.welcome) throw new Error('Expected Welcome');
  await alice.page.evaluate(() => window.proof.settle(true));
  await bob.page.evaluate(wire => window.proof.join(wire), addition.welcome);
  return { alice, bob };
}

async function reload(page: Page) {
  await page.reload();
  await page.waitForFunction(() => Boolean(window.proof));
  await expect(page.evaluate(() => window.proof.status())).rejects.toThrow('Device locked');
  await page.evaluate(password => window.proof.unlock(password), password);
}

async function receive(page: Page, sequence: number, wire: string) {
  return page.evaluate(({ sequence, wire }) => window.proof.receive(sequence, wire), { sequence, wire });
}

test('isolated browsers persist ratchets, encrypted history and a resendable outbox', async ({ browser }) => {
  const { alice, bob } = await pair(browser);
  const text = 'Synthetic private text: 24de175a';
  const wire = await alice.page.evaluate(text => window.proof.send(text), text);
  expect(atob(wire).includes(text)).toBe(false);

  // Browser closed while offline, then reopens with its independent device storage.
  await bob.page.close();
  await reload(alice.page);
  expect((await alice.page.evaluate(() => window.proof.status())).outbox).toEqual([wire]);
  bob.page = await bob.context.newPage();
  await bob.page.goto('/');
  await bob.page.waitForFunction(() => Boolean(window.proof));
  await bob.page.evaluate(password => window.proof.unlock(password), password);
  expect(await receive(bob.page, 1, wire)).toBe('applicationMessage');
  await alice.page.evaluate(wire => window.proof.acknowledge(wire), wire);
  await reload(bob.page);
  const status = await bob.page.evaluate(() => window.proof.status());
  expect(status.inbox).toEqual([text]);
  expect(status.cursor).toBe(1);

  const stored = await bob.page.evaluate(async () => {
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open('kymessages-mls-proof-v1');
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    try {
      return await new Promise<string>((resolve, reject) => {
        const transaction = db.transaction('vault');
        const request = transaction.objectStore('vault').get('device');
        transaction.oncomplete = () => resolve(JSON.stringify(request.result));
        transaction.onabort = () => reject(transaction.error);
      });
    } finally { db.close(); }
  });
  expect(stored).not.toContain(text);
  expect(stored).not.toContain('signaturePrivateKey');
  expect(stored).not.toContain(password);
  await expect(bob.page.evaluate(() => window.proof.unlock('incorrect synthetic passphrase'))).rejects.toThrow();
  await expect(bob.page.evaluate(() => window.proof.status())).rejects.toThrow('Device locked');
  await bob.page.evaluate(password => window.proof.unlock(password), password);
  const reply = await bob.page.evaluate(() => window.proof.send('Reply after reload'));
  await receive(alice.page, 1, reply);
  expect((await alice.page.evaluate(() => window.proof.status())).inbox).toEqual(['Reply after reload']);
  await alice.context.close();
  await bob.context.close();
});

test('tampering, replay, trailing bytes and gaps leave the receiver unchanged', async ({ browser }) => {
  const { alice, bob } = await pair(browser);
  const wire = await alice.page.evaluate(() => window.proof.send('Authenticated content'));
  const damaged = unbase64(wire);
  const last = damaged.at(-1);
  if (last === undefined) throw new Error('Empty wire');
  damaged[damaged.length - 1] = last ^ 1;
  await expect(receive(bob.page, 1, base64(damaged))).rejects.toThrow();
  await expect(receive(bob.page, 1, base64(new Uint8Array([...unbase64(wire), 0]))))
    .rejects.toThrow('Malformed or trailing wire bytes');
  await expect(receive(bob.page, 2, wire)).rejects.toThrow('Delivery gap');
  expect((await bob.page.evaluate(() => window.proof.status())).cursor).toBe(0);
  await receive(bob.page, 1, wire);
  expect(await receive(bob.page, 1, wire)).toBe('duplicate');
  await expect(receive(bob.page, 1, base64(damaged))).rejects.toThrow('Delivery sequence conflict');
  await expect(receive(bob.page, 2, wire)).rejects.toThrow();
  expect((await bob.page.evaluate(() => window.proof.status())).inbox).toEqual(['Authenticated content']);
  const next = await alice.page.evaluate(() => window.proof.send('Still synchronized'));
  await receive(bob.page, 2, next);
  await alice.context.close();
  await bob.context.close();
});

test('new devices get future messages; a removed device cannot decrypt the next epoch', async ({ browser }) => {
  const { alice, bob } = await pair(browser);
  const old = await alice.page.evaluate(() => window.proof.send('Before Charlie joined'));
  await receive(bob.page, 1, old);
  await alice.page.evaluate(wire => window.proof.acknowledge(wire), old);
  const charlie = await device(browser, 'charlie');
  for (const peer of [alice, bob]) {
    await peer.page.evaluate(wire => window.proof.approve(wire), charlie.keyPackage);
    await charlie.page.evaluate(wire => window.proof.approve(wire), peer.keyPackage);
  }
  const add = await alice.page.evaluate(wire => window.proof.add(wire), charlie.keyPackage);
  if (!add.welcome) throw new Error('Expected Welcome');
  await alice.page.evaluate(() => window.proof.settle(true));
  await receive(bob.page, 2, add.wire);
  await charlie.page.evaluate(wire => window.proof.join(wire), add.welcome);
  await expect(charlie.page.evaluate(wire => window.proof.join(wire), add.welcome)).rejects.toThrow('Welcome already consumed');
  await expect(receive(charlie.page, 1, old)).rejects.toThrow();
  const shared = await alice.page.evaluate(() => window.proof.send('Three approved devices'));
  await receive(bob.page, 3, shared);
  await receive(charlie.page, 1, shared);
  await alice.page.evaluate(wire => window.proof.acknowledge(wire), shared);

  const removal = await alice.page.evaluate(() => window.proof.remove('bob'));
  await alice.page.evaluate(() => window.proof.settle(true));
  await receive(charlie.page, 2, removal.wire);
  const privateWire = await alice.page.evaluate(() => window.proof.send('Only Alice and Charlie'));
  await expect(receive(bob.page, 4, privateWire)).rejects.toThrow();
  await receive(charlie.page, 3, privateWire);
  expect((await charlie.page.evaluate(() => window.proof.status())).inbox).toEqual(['Three approved devices', 'Only Alice and Charlie']);
  expect((await alice.page.evaluate(() => window.proof.status())).members).toEqual(['alice', 'charlie']);
  for (const peer of [alice, bob, charlie]) await peer.context.close();
});

test('competing commits stage, survive reload, and resolve by accept or discard then retry', async ({ browser }) => {
  const { alice, bob } = await pair(browser);
  const a = await alice.page.evaluate(() => window.proof.update());
  const b = await bob.page.evaluate(() => window.proof.update());
  expect(a.epoch).toBe(b.epoch);
  await expect(bob.page.evaluate(() => window.proof.send('Cannot send with an unresolved commit'))).rejects.toThrow('Resolve the staged commit');
  await reload(alice.page);
  expect((await alice.page.evaluate(() => window.proof.status())).pending?.wire).toBe(a.wire);
  // Test relay's epoch CAS chooses Alice; Bob keeps old state until applying her winner.
  await alice.page.evaluate(() => window.proof.settle(true));
  await bob.page.evaluate(() => window.proof.settle(false));
  await reload(bob.page);
  await expect(bob.page.evaluate(() => window.proof.update())).rejects.toThrow('Apply the winning commit');
  await expect(bob.page.evaluate(() => window.proof.send('Cannot reuse the abandoned epoch'))).rejects.toThrow('Apply the winning commit');
  await receive(bob.page, 1, a.wire);
  await expect(receive(alice.page, 1, b.wire)).rejects.toThrow();
  const retry = await bob.page.evaluate(() => window.proof.update());
  expect(BigInt(retry.epoch)).toBe(BigInt(b.epoch) + 1n);
  await bob.page.evaluate(() => window.proof.settle(true));
  await receive(alice.page, 1, retry.wire);
  const wire = await bob.page.evaluate(() => window.proof.send('Converged after losing a commit'));
  await receive(alice.page, 2, wire);
  expect((await alice.page.evaluate(() => window.proof.status())).inbox).toEqual(['Converged after losing a commit']);
  await alice.context.close();
  await bob.context.close();
});

test('unknown and substituted device credentials fail closed', async ({ browser }) => {
  const alice = await device(browser, 'alice');
  const bob = await device(browser, 'bob');
  const impostor = await device(browser, 'bob');
  await alice.page.evaluate(() => window.proof.create());
  await expect(alice.page.evaluate(wire => window.proof.add(wire), bob.keyPackage)).rejects.toThrow();
  await alice.page.evaluate(wire => window.proof.approve(wire), bob.keyPackage);
  await expect(alice.page.evaluate(wire => window.proof.approve(wire), impostor.keyPackage)).rejects.toThrow('Device identity key changed');
  await expect(alice.page.evaluate(wire => window.proof.add(wire), impostor.keyPackage)).rejects.toThrow();
  const add = await alice.page.evaluate(wire => window.proof.add(wire), bob.keyPackage);
  if (!add.welcome) throw new Error('Expected Welcome');
  await alice.page.evaluate(() => window.proof.settle(true));
  await expect(bob.page.evaluate(wire => window.proof.join(wire), add.welcome)).rejects.toThrow();
  await bob.page.evaluate(wire => window.proof.approve(wire), alice.keyPackage);
  await bob.page.evaluate(wire => window.proof.join(wire), add.welcome);
  for (const peer of [alice, bob, impostor]) await peer.context.close();
});

test('tabs serialize one device ratchet and storage failure never releases a message', async ({ browser }) => {
  const { alice, bob } = await pair(browser);
  const tab = await alice.context.newPage();
  await tab.goto('/');
  await tab.waitForFunction(() => Boolean(window.proof));
  await tab.evaluate(password => window.proof.unlock(password), password);
  await Promise.all([
    alice.page.evaluate(() => window.proof.send('First tab')),
    tab.evaluate(() => window.proof.send('Second tab')),
  ]);
  const outbox = (await alice.page.evaluate(() => window.proof.status())).outbox;
  expect(outbox).toHaveLength(2);
  for (const [index, wire] of outbox.entries()) await receive(bob.page, index + 1, wire);
  expect((await bob.page.evaluate(() => window.proof.status())).inbox.sort()).toEqual(['First tab', 'Second tab']);

  // Real crypto + storage, with only the failure boundary fault-injected.
  await alice.page.evaluate(() => {
    const put = IDBObjectStore.prototype.put;
    IDBObjectStore.prototype.put = function (...args: Parameters<IDBObjectStore['put']>) {
      const result = put.apply(this, args);
      this.transaction.abort();
      return result;
    };
  });
  await expect(alice.page.evaluate(() => window.proof.send('Must not escape failed persistence'))).rejects.toThrow('Write aborted');
  await reload(alice.page);
  expect((await alice.page.evaluate(() => window.proof.status())).outbox).toEqual(outbox);
  const next = await alice.page.evaluate(() => window.proof.send('Successful retry after quota failure'));
  await bob.page.evaluate(() => {
    const put = IDBObjectStore.prototype.put;
    IDBObjectStore.prototype.put = function (...args: Parameters<IDBObjectStore['put']>) {
      const result = put.apply(this, args);
      this.transaction.abort();
      return result;
    };
  });
  await expect(receive(bob.page, 3, next)).rejects.toThrow('Write aborted');
  await reload(bob.page);
  expect((await bob.page.evaluate(() => window.proof.status())).cursor).toBe(2);
  await receive(bob.page, 3, next);
  expect((await bob.page.evaluate(() => window.proof.status())).inbox).toHaveLength(3);
  await alice.context.close();
  await bob.context.close();
});

test('manual proof UI uses the production CSP and does not make relay requests', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
  const response = await page.goto('/');
  expect(response?.headers()['content-security-policy']).toContain("script-src 'self'");
  await page.getByLabel('Device name', { exact: true }).fill('ui-device');
  await page.getByLabel('Test passphrase (16 characters minimum)').fill(password);
  await page.getByRole('button', { name: 'Initialize', exact: true }).click();
  await expect(page.getByRole('status')).toHaveText('Initialize: complete');
  await page.getByRole('button', { name: 'Create room', exact: true }).click();
  await expect(page.getByRole('status')).toHaveText('Create room: complete');
  const requests: string[] = [];
  page.on('request', request => requests.push(request.url()));
  await page.getByLabel('Test message', { exact: true }).fill('<img src=x onerror=alert(1)>');
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await expect(page.getByRole('status')).toHaveText('Send: complete');
  expect(requests).toEqual([]);
  expect(errors).toEqual([]);
});
