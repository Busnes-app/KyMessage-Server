import { test, expect, firefox, type Browser, type Page } from '@playwright/test';
import type {} from '../src/delivery';
import type {} from '../src/device';
import { object, text } from '../src/delivery-wire';

const password = 'disposable MLS HTTP integration passphrase';
async function device(browser: Browser, name: string, loseEnrollmentReply = false) {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto('/');
  await page.waitForFunction(() => Boolean(window.delivery));
  const user = name + '-' + crypto.randomUUID().slice(0,8);
  const response = await page.request.post('/proof-fixture/session/' + user);
  expect(response.ok()).toBe(true);
  const raw: unknown = await response.json();
  const session = text(object(raw).session);
  await page.evaluate(({user,password}) => window.proof.initialize(user,password),{user,password});
  await page.evaluate(session => window.delivery.connect(session),session);
  if (loseEnrollmentReply) {
    await page.route('**/api/messaging/devices/*/verify',async route => {
      const response = await route.fetch();
      expect(response.status()).toBe(200);
      await route.abort('failed');
    },{times:1});
    await expect(page.evaluate(() => window.delivery.enroll())).rejects.toThrow();
    await reload(page,session);
  }
  const id = await page.evaluate(() => window.delivery.enroll());
  const fingerprint = await page.evaluate(() => window.delivery.ownFingerprint());
  return {context,page,user,session,fingerprint,id};
}
async function reload(page: Page, session: string) {
  await page.reload();
  await page.waitForFunction(() => Boolean(window.delivery));
  await page.evaluate(({password,session}) => { window.delivery.connect(session); return window.proof.unlock(password); },{password,session});
}
async function accept(page: Page) {
  const receipt = await page.evaluate(() => window.delivery.submit());
  await page.evaluate(() => window.delivery.sync());
  return receipt;
}
async function pair(browser: Browser, failure: 'lost' | 'tamper' | null = null, names = {alice:'alice',bob:'bob'}) {
  const alice = await device(browser,names.alice);
  const bob = await device(browser,names.bob);
  const room = await alice.page.evaluate(() => window.delivery.createRoom('Synthetic team'));
  await alice.page.evaluate(() => window.delivery.stageCommit());
  expect(await accept(alice.page)).toEqual({epoch:1,sequence:1});
  await alice.page.evaluate(user => window.delivery.invite(user),bob.user);
  await bob.page.evaluate(room => window.delivery.selectRoom(room),room);
  // The server directory alone cannot approve a previously unknown MLS key.
  await expect(alice.page.evaluate(() => window.delivery.stageCommit())).rejects.toThrow('Unpinned roster');
  await expect(alice.page.evaluate(id => window.delivery.approveDevice(id,'0'.repeat(64)),bob.id)).rejects.toThrow('fingerprint mismatch');
  await alice.page.evaluate(({id,fingerprint}) => window.delivery.approveDevice(id,fingerprint),{id:bob.id,fingerprint:bob.fingerprint});
  await bob.page.evaluate(({id,fingerprint}) => window.delivery.approveDevice(id,fingerprint),{id:alice.id,fingerprint:alice.fingerprint});
  if (failure === 'lost') {
    await bob.page.route('**/api/messaging/devices/key-packages',async route => {
      expect((await route.fetch()).status()).toBe(200);
      await route.abort('failed');
    },{times:1});
    await expect(bob.page.evaluate(() => window.delivery.publishKeyPackage())).rejects.toThrow();
    await reload(bob.page,bob.session);
  }
  await bob.page.evaluate(() => window.delivery.publishKeyPackage());
  const claims: string[] = [];
  alice.page.on('request',request => { if (request.url().endsWith('/key-packages/claim')) claims.push(request.postData() ?? ''); });
  if (failure) {
    await alice.page.route('**/key-packages/claim',async route => {
      const response = await route.fetch();
      expect(response.status()).toBe(200);
      if (failure === 'lost') await route.abort('failed');
      else {
        const raw: unknown = await response.json();
        const body = object(raw);
        body.payload = 'b3BhcXVl';
        await route.fulfill({response,json:body});
      }
    },{times:1});
    await expect(alice.page.evaluate(() => window.delivery.stageCommit())).rejects.toThrow();
    expect((await alice.page.evaluate(() => window.delivery.status())).epoch).toBe('1');
    await reload(alice.page,alice.session);
  }
  await alice.page.evaluate(() => window.delivery.stageCommit());
  if (failure) { expect(claims.length).toBe(2); expect(claims[0]).toBe(claims[1]); }
  expect(await accept(alice.page)).toEqual({epoch:2,sequence:2});
  await bob.page.evaluate(() => window.delivery.sync());
  expect((await bob.page.evaluate(() => window.delivery.status())).cursor).toBe(2);
  return {alice,bob,room};
}

test('UTF-8 account IDs retain exact MLS credentials and match Go roster escaping',async ({browser}) => {
  const {alice,bob} = await pair(browser,null,{alice:'Élise<&>\u2028x',bob:'李\u2029x'});
  try {
    await alice.page.evaluate(() => window.delivery.stageSend('UTF-8 bound identities'));
    await accept(alice.page);
    expect((await bob.page.evaluate(() => window.delivery.sync())).inbox).toEqual(['UTF-8 bound identities']);
    expect((await alice.page.evaluate(() => window.proof.status())).identity).toBe(alice.user);
    expect((await bob.page.evaluate(() => window.proof.status())).identity).toBe(bob.user);
  } finally { await alice.context.close(); await bob.context.close(); }
});

test('real HTTP relay: MLS join, lost acknowledgement, reload, commit race and removal',async ({browser}) => {
  const {alice,bob} = await pair(browser,'lost');
  try {
    const sent: string[] = [];
    alice.page.on('request',request => { if (request.method() === 'POST' && request.url().endsWith('/events')) sent.push(request.postData() ?? ''); });
    const message = 'Synthetic secret delivered through real Go API 620e';
    await alice.page.evaluate(message => window.delivery.stageSend(message),message);
    // Backend commits, but the browser never receives the acknowledgement.
    await alice.page.route('**/api/messaging/rooms/*/events',async route => {
      const result = await route.fetch();
      expect(result.status()).toBe(200);
      await route.abort('failed');
    },{times:1});
    await expect(alice.page.evaluate(() => window.delivery.submit())).rejects.toThrow();
    await reload(alice.page,alice.session);
    expect((await alice.page.evaluate(() => window.delivery.status())).pending).toBe(true);
    expect(await accept(alice.page)).toEqual({epoch:2,sequence:3});
    await reload(bob.page,bob.session);
    expect((await bob.page.evaluate(() => window.delivery.sync())).inbox).toEqual([message]);
    expect(sent.length).toBe(2);
    expect(sent[0]).toBe(sent[1]);
    for (const body of sent) { expect(body).not.toContain(message); expect(body).not.toContain('PrivateKey'); }
    await bob.page.evaluate(() => window.delivery.stageSend('Reply over HTTP'));
    await accept(bob.page);
    expect((await alice.page.evaluate(() => window.delivery.sync())).inbox).toEqual(['Reply over HTTP']);
    await Promise.all([alice.page.evaluate(() => window.delivery.stageCommit()),bob.page.evaluate(() => window.delivery.stageCommit())]);
    expect(await accept(alice.page)).toEqual({epoch:3,sequence:5});
    await expect(bob.page.evaluate(() => window.delivery.submit())).rejects.toThrow('Commit conflict');
    await reload(bob.page,bob.session);
    await expect(bob.page.evaluate(() => window.delivery.stageCommit())).rejects.toThrow('Resolve pending');
    await bob.page.evaluate(() => window.delivery.sync());
    await bob.page.evaluate(() => window.delivery.stageCommit());
    expect(await accept(bob.page)).toEqual({epoch:4,sequence:6});
    await alice.page.evaluate(() => window.delivery.sync());
    await alice.page.evaluate(user => window.delivery.removeMember(user),bob.user);
    await expect(alice.page.evaluate(() => window.delivery.stageSend('Must rekey'))).rejects.toThrow('Roster changed');
    await alice.page.evaluate(() => window.delivery.stageCommit());
    expect(await accept(alice.page)).toEqual({epoch:5,sequence:7});
    await expect(bob.page.evaluate(() => window.delivery.sync())).rejects.toThrow('HTTP 404');
    await alice.page.evaluate(() => window.delivery.stageSend('After cryptographic removal'));
    await accept(alice.page);
    const last = sent.at(-1);
    if (!last) throw new Error('Missing application request');
    const wire = text(object(JSON.parse(last)).payload);
    // Give the removed client the new ciphertext despite its server access denial.
    // Its old MLS state still cannot decrypt the newer epoch.
    await expect(bob.page.evaluate(wire => window.proof.receive(7,wire),wire)).rejects.toThrow();
    expect((await bob.page.evaluate(() => window.delivery.status())).inbox).toEqual([message]);
  } finally { await alice.context.close(); await bob.context.close(); }
});

test('authenticated envelope mismatch and aborted receiver writes release no plaintext or cursor',async ({browser}) => {
  const {alice,bob} = await pair(browser,'tamper');
  try {
    await alice.page.evaluate(() => window.delivery.stageSend('Only after validation and durable write'));
    await accept(alice.page);
    await bob.page.route('**/api/messaging/rooms/*/events?after=*',async route => {
      const response = await route.fetch();
      const value: unknown = await response.json();
      const page = object(value);
      if (!Array.isArray(page.events) || !page.events[0]) throw new Error('Missing event');
      object(page.events[0]).device_id = bob.id;
      await route.fulfill({response,json:page});
    },{times:1});
    await expect(bob.page.evaluate(() => window.delivery.sync())).rejects.toThrow('metadata mismatch');
    expect((await bob.page.evaluate(() => window.delivery.status())).cursor).toBe(2);
    await bob.page.evaluate(() => {
      const original = IDBObjectStore.prototype.put;
      IDBObjectStore.prototype.put = function(...args: Parameters<IDBObjectStore['put']>) {
        IDBObjectStore.prototype.put = original;
        const result = original.apply(this,args);
        this.transaction.abort();
        return result;
      };
    });
    await expect(bob.page.evaluate(() => window.delivery.sync())).rejects.toThrow();
    await reload(bob.page,bob.session);
    expect((await bob.page.evaluate(() => window.delivery.status())).inbox).toEqual([]);
    expect((await bob.page.evaluate(() => window.delivery.status())).messages).toEqual([]);
    expect((await bob.page.evaluate(() => window.delivery.sync())).inbox).toEqual(['Only after validation and durable write']);
    expect((await bob.page.evaluate(() => window.delivery.status())).messages).toHaveLength(1);
  } finally { await alice.context.close(); await bob.context.close(); }
});


test('Chromium and Firefox exchange concurrent sends after enrollment acknowledgement loss',async ({browser,browserName}) => {
  test.skip(browserName !== 'chromium','Cross-engine case runs once from Chromium');
  const other = await firefox.launch();
  const alice = await device(browser,'cross-alice',true);
  const bob = await device(other,'cross-bob');
  try {
    const room = await alice.page.evaluate(() => window.delivery.createRoom('Cross engine'));
    await alice.page.evaluate(user => window.delivery.invite(user),bob.user);
    await bob.page.evaluate(room => window.delivery.selectRoom(room),room);
    await alice.page.evaluate(({id,fingerprint}) => window.delivery.approveDevice(id,fingerprint),{id:bob.id,fingerprint:bob.fingerprint});
    await bob.page.evaluate(({id,fingerprint}) => window.delivery.approveDevice(id,fingerprint),{id:alice.id,fingerprint:alice.fingerprint});
    await bob.page.evaluate(() => window.delivery.publishKeyPackage());
    await alice.page.evaluate(() => window.delivery.stageCommit());
    await accept(alice.page);
    await bob.page.evaluate(() => window.delivery.sync());
    await Promise.all([
      alice.page.evaluate(() => window.delivery.stageSend('Chromium to Firefox')),
      bob.page.evaluate(() => window.delivery.stageSend('Firefox to Chromium')),
    ]);
    await Promise.all([alice.page.evaluate(() => window.delivery.submit()),bob.page.evaluate(() => window.delivery.submit())]);
    expect((await alice.page.evaluate(() => window.delivery.sync())).inbox).toEqual(['Firefox to Chromium']);
    expect((await bob.page.evaluate(() => window.delivery.sync())).inbox).toEqual(['Chromium to Firefox']);
    await bob.page.evaluate(() => window.delivery.stageCommit());
    await accept(bob.page);
    await alice.page.evaluate(() => window.delivery.sync());
    expect((await alice.page.evaluate(() => window.delivery.status())).epoch).toBe('2');
  } finally { await alice.context.close(); await bob.context.close(); await other.close(); }
});
