import { test, expect, firefox, type Browser, type Page } from '@playwright/test';
import type {} from '../src/delivery';
import type {} from '../src/device';
import { object, text, connection } from '../src/delivery-wire';
import { parseRecord, trimSavedMessages, maxSavedMessageBytes } from '../src/vault';

const password = 'disposable MLS HTTP integration passphrase';
async function device(browser: Browser, name: string, loseEnrollmentReply = false) {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.clock.install();
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

for (const mode of ['cached','lost','welcome']) test(`join package renewal preserves identity after ${mode} claim`,async ({browser}) => {
  const alice = await device(browser,'renew-alice');
  const bob = await device(browser,'renew-bob');
  try {
    const room = await alice.page.evaluate(() => window.delivery.createRoom('Renewal'));
    await alice.page.evaluate(user => window.delivery.invite(user),bob.user);
    await bob.page.evaluate(room => window.delivery.selectRoom(room),room);
    await alice.page.evaluate(({id,fingerprint}) => window.delivery.approveDevice(id,fingerprint),{id:bob.id,fingerprint:bob.fingerprint});
    await bob.page.evaluate(({id,fingerprint}) => window.delivery.approveDevice(id,fingerprint),{id:alice.id,fingerprint:alice.fingerprint});
    // Use the browser clock to request a real, short server allocation lifetime.
    const expiry = Math.floor(Date.now()/1000)+10;
    await bob.page.clock.setFixedTime((expiry-3600)*1000);
    const first = await bob.page.evaluate(() => window.delivery.publishKeyPackage());
    const claims: string[] = [];
    alice.page.on('request',request => { if (request.url().endsWith('/key-packages/claim')) claims.push(request.postData() ?? ''); });
    if (mode === 'lost') {
      await alice.page.route('**/key-packages/claim',async route => {
        expect((await route.fetch()).status()).toBe(200);
        await route.abort('failed');
      },{times:1});
      await expect(alice.page.evaluate(() => window.delivery.stageCommit())).rejects.toThrow();
    } else if (mode === 'cached') {
      let reads = 0;
      await alice.page.route('**/rooms/*/delivery',async route => {
        // Save the claim, then interrupt before an MLS commit is staged.
        if (++reads === 2) await route.abort('failed'); else await route.continue();
      });
      await expect(alice.page.evaluate(() => window.delivery.stageCommit())).rejects.toThrow();
      await alice.page.unroute('**/rooms/*/delivery');
    } else {
      await alice.page.evaluate(() => window.delivery.stageCommit());
      await accept(alice.page);
    }
    await expect.poll(() => Date.now(),{timeout:15000}).toBeGreaterThanOrEqual(expiry*1000);
    await bob.page.clock.setFixedTime(Date.now());
    const publications: string[] = [];
    bob.page.on('request',request => { if (request.url().endsWith('/devices/key-packages')) publications.push(request.postData() ?? ''); });
    await bob.page.route('**/devices/key-packages',async route => {
      expect((await route.fetch()).status()).toBe(200);
      await route.abort('failed');
    },{times:1});
    await expect(bob.page.evaluate(() => window.delivery.publishKeyPackage())).rejects.toThrow();
    await reload(bob.page,bob.session);
    const fresh = await bob.page.evaluate(() => window.delivery.publishKeyPackage());
    expect(fresh).not.toBe(first);
    expect(publications).toHaveLength(2);
    expect(publications[0]).toBe(publications[1]);
    expect(await bob.page.evaluate(() => window.delivery.ownFingerprint())).toBe(bob.fingerprint);
    if (mode !== 'welcome') {
      await reload(alice.page,alice.session);
      if (mode === 'lost') {
        await expect(alice.page.evaluate(() => window.delivery.stageCommit())).rejects.toThrow('Previous claim is no longer usable');
        expect(claims[0]).toBe(claims[1]);
        await reload(alice.page,alice.session);
      }
      await alice.page.evaluate(() => window.delivery.stageCommit());
      expect(claims).toHaveLength(mode === 'lost' ? 3 : 2);
      expect(claims[0]).not.toBe(claims.at(-1));
      await accept(alice.page);
    }
    await bob.page.evaluate(() => window.delivery.sync());
    await expect(bob.page.evaluate(() => window.delivery.publishKeyPackage())).rejects.toThrow('already consumed');
    await alice.page.evaluate(() => window.delivery.stageSend('Renewed join works'));
    await accept(alice.page);
    expect((await bob.page.evaluate(() => window.delivery.sync())).inbox).toEqual(['Renewed join works']);
    if (mode === 'welcome') {
      // The unused newer package is still in the server pool after the older
      // Welcome joins. Its retained keys must remain usable for a later rejoin.
      await alice.page.evaluate(user => window.delivery.removeMember(user),bob.user);
      await alice.page.evaluate(() => window.delivery.stageCommit());
      await accept(alice.page);
      await alice.page.evaluate(user => window.delivery.invite(user),bob.user);
      await bob.page.evaluate(() => window.delivery.rejoin());
      await bob.page.evaluate(() => window.delivery.publishKeyPackage());
      await alice.page.evaluate(() => window.delivery.stageCommit());
      await accept(alice.page);
      await reload(bob.page,bob.session);
      await bob.page.evaluate(() => window.delivery.sync());
      await bob.page.evaluate(() => window.delivery.stageSend('Unused package rejoin works'));
      await accept(bob.page);
      expect((await alice.page.evaluate(() => window.delivery.sync())).inbox).toEqual(['Unused package rejoin works']);
    }
  } finally { await alice.context.close(); await bob.context.close(); }
});

for (const removeCommit of [false,true]) test(`explicit rejoin preserves history with removal commit ${removeCommit}`,async ({browser}) => {
  const {alice,bob} = await pair(browser);
  try {
    await expect(bob.page.evaluate(() => window.delivery.rejoin())).rejects.toThrow('newer membership generation');
    await bob.page.evaluate(() => window.delivery.stageSend('Kept before removal'));
    await expect(bob.page.evaluate(() => window.delivery.rejoin())).rejects.toThrow('Resolve pending');
    await accept(bob.page);
    await alice.page.evaluate(() => window.delivery.sync());
    const before = await bob.page.evaluate(() => window.delivery.status());
    await alice.page.evaluate(user => window.delivery.removeMember(user),bob.user);
    await expect(bob.page.evaluate(() => window.delivery.rejoin())).rejects.toThrow('invitation is required');
    if (removeCommit) {
      await alice.page.evaluate(() => window.delivery.stageCommit());
      await accept(alice.page);
      await alice.page.evaluate(() => window.delivery.stageSend('Unavailable during removal'));
      await accept(alice.page);
    }
    await alice.page.evaluate(user => window.delivery.invite(user),bob.user);
    await bob.page.route('**/rooms/*/join',async route => {
      expect((await route.fetch()).status()).toBe(200);
      await route.abort('failed');
    },{times:1});
    await expect(bob.page.evaluate(() => window.delivery.rejoin())).rejects.toThrow();
    expect(await bob.page.evaluate(() => window.delivery.status())).toEqual(before);
    if (removeCommit) {
      await bob.page.goto('/chat.html');
      await bob.page.getByLabel('Test account',{exact:true}).fill(bob.user);
      await bob.page.getByLabel('Local passphrase').fill(password);
      await bob.page.getByRole('button',{name:'Unlock existing device',exact:true}).click();
      await expect(bob.page.getByRole('status')).toContainText('Test device connected');
      await bob.page.getByRole('button',{name:'Accept reinvitation',exact:true}).click();
      await expect(bob.page.getByRole('status')).toContainText('Reinvitation accepted');
      await expect(bob.page.locator('#room-state')).toContainText('Earlier local history stays');
      await expect(bob.page.locator('#send')).toBeDisabled();
      await bob.page.goto('/');
    }
    await reload(bob.page,bob.session);
    await bob.page.evaluate(() => window.delivery.rejoin());
    await bob.page.evaluate(() => window.delivery.publishKeyPackage());
    await reload(bob.page,bob.session);
    const waiting = await bob.page.evaluate(() => window.delivery.status());
    expect(waiting.rejoining).toBe(true);
    expect(waiting.epoch).toBe(before.epoch);
    expect(waiting.cursor).toBe(before.cursor);
    expect(waiting.messages).toEqual(before.messages);
    expect(await bob.page.evaluate(() => window.delivery.ownFingerprint())).toBe(bob.fingerprint);
    await expect(bob.page.evaluate(() => window.delivery.stageSend('Too early'))).rejects.toThrow('Resolve pending');
    await alice.page.evaluate(() => window.delivery.stageCommit());
    await accept(alice.page);
    await bob.page.route('**/events?after=*',async route => {
      const response = await route.fetch();
      const body = object(await response.json());
      if (!Array.isArray(body.events)) throw new Error('Expected events');
      body.start_sequence = before.cursor;
      object(body.events[0]).sequence = before.cursor;
      await route.fulfill({response,json:body});
    },{times:1});
    await expect(bob.page.evaluate(() => window.delivery.sync())).rejects.toThrow('history or generation mismatch');
    expect(await bob.page.evaluate(() => window.delivery.status())).toEqual(waiting);
    await bob.page.evaluate(() => window.delivery.sync());
    expect((await bob.page.evaluate(() => window.delivery.status())).rejoining).toBe(false);
    await alice.page.evaluate(() => window.delivery.stageSend('Available after rejoin'));
    await accept(alice.page);
    expect((await bob.page.evaluate(() => window.delivery.sync())).inbox).toEqual(['Available after rejoin']);
    expect((await bob.page.evaluate(() => window.delivery.status())).messages.map(m => m.text)).toEqual(['Kept before removal','Available after rejoin']);
    await bob.page.evaluate(() => window.delivery.stageSend('Rejoined reply'));
    await accept(bob.page);
    expect((await alice.page.evaluate(() => window.delivery.sync())).inbox).toEqual(['Kept before removal','Rejoined reply']);
  } finally { await alice.context.close(); await bob.context.close(); }
});

test('separate room ratchets do not share a lock or accept swapped encrypted records',async ({browser}) => {
  const alice = await device(browser,'separate-rooms');
  const other = await alice.context.newPage();
  try {
    const first = await alice.page.evaluate(() => window.delivery.createRoom('First isolated room'));
    await alice.page.evaluate(() => window.delivery.stageCommit());
    await accept(alice.page);
    const second = await alice.page.evaluate(() => window.delivery.createRoom('Second isolated room'));
    await alice.page.evaluate(() => window.delivery.stageCommit());
    await accept(alice.page);
    await other.goto('/');
    await other.waitForFunction(() => Boolean(window.delivery));
    await other.evaluate(async ({password,session,room}) => {
      await window.proof.unlock(password);
      window.delivery.connect(session);
      await window.delivery.selectRoom(room);
    },{password,session:alice.session,room:second});
    await alice.page.evaluate(room => window.delivery.selectRoom(room),first);
    await alice.page.evaluate(() => new Promise<void>(resolve => {
      void navigator.locks.request('kymessages-mls-proof-v1',async () => {
        const held = new Promise<void>(release => { Object.assign(window,{releaseRoomLock:release}); });
        resolve();
        await held;
      });
    }));
    try {
      await Promise.race([
        other.evaluate(async () => {
          await window.delivery.stageSend('Independent room state');
          await window.delivery.submit();
          await window.delivery.sync();
        }),
        new Promise<never>((_resolve,reject) => setTimeout(() => reject(new Error('Second room blocked on first room lock')),5000)),
      ]);
    } finally {
      await alice.page.evaluate(() => (window as typeof window & {releaseRoomLock:()=>void}).releaseRoomLock());
    }
    expect((await other.evaluate(() => window.delivery.status())).messages[0]?.text).toBe('Independent room state');
    await other.evaluate(async room => {
      const db = await new Promise<IDBDatabase>((resolve,reject) => {
        const open = indexedDB.open('kymessages-mls-proof-v1',1);
        open.onsuccess = () => resolve(open.result);
        open.onerror = () => reject(open.error);
      });
      try {
        await new Promise<void>((resolve,reject) => {
          const tx = db.transaction('vault','readwrite');
          const store = tx.objectStore('vault');
          const read = store.get('device');
          read.onsuccess = () => store.put(read.result,'room:' + room);
          tx.oncomplete = () => resolve();
          tx.onabort = () => reject(tx.error);
        });
      } finally {db.close();}
    },second);
    await expect(other.evaluate(() => window.delivery.status())).rejects.toThrow();
    expect((await alice.page.evaluate(() => window.delivery.status())).room).toBe(first);
  } finally { await alice.context.close(); }
});

test('server rollback response preserves the ratchet and pending bytes across reload',async ({browser}) => {
  const {alice,bob} = await pair(browser);
  try {
    await bob.page.evaluate(() => window.delivery.stageSend('Keep unresolved ciphertext'));
    const before = await bob.page.evaluate(() => window.delivery.status());
    await bob.page.route('**/events?after=*',route => route.fulfill({status:409,json:{error:'Cursor exceeds restored server sequence'}}),{times:1});
    await expect(bob.page.evaluate(() => window.delivery.sync())).rejects.toThrow('Server history is behind');
    await reload(bob.page,bob.session);
    expect(await bob.page.evaluate(() => window.delivery.status())).toEqual({...before,historyGap:'rollback'});
    await expect(bob.page.evaluate(() => window.delivery.stageSend('Do not regenerate'))).rejects.toThrow('Missing encrypted history');
    await expect(bob.page.evaluate(() => window.delivery.rejoin())).rejects.toThrow('Resolve pending');
    // A later successful read must not silently clear the persisted failure.
    await expect(bob.page.evaluate(() => window.delivery.sync())).rejects.toThrow('Server history is behind');
  } finally { await alice.context.close(); await bob.context.close(); }
});

test('local expiry removes both transcript copies without changing keys or pending delivery',async ({browser}) => {
  const {alice,bob} = await pair(browser);
  try {
    await alice.page.evaluate(() => window.delivery.stageSend('Same text in both directions'));
    await accept(alice.page);
    await bob.page.evaluate(() => window.delivery.sync());
    await bob.page.evaluate(() => window.delivery.stageSend('Same text in both directions'));
    await accept(bob.page);
    await bob.page.evaluate(() => window.delivery.stageSend('Pending text must survive cleanup'));
    const before = await bob.page.evaluate(() => window.delivery.status());
    expect(before.messages).toHaveLength(2);
    expect(before.inbox).toHaveLength(1);
    const now = Date.now();
    await bob.page.clock.setSystemTime(new Date(now+91*86400_000));
    const expired = await bob.page.evaluate(() => window.delivery.status());
    expect(expired).toEqual({...before,messages:[],inbox:[]});
    expect((await bob.page.evaluate(() => window.proof.status())).inbox).toEqual([]);
    await reload(bob.page,bob.session);
    expect(await bob.page.evaluate(() => window.delivery.status())).toEqual(expired);
    await bob.page.clock.setSystemTime(new Date(now));
    await accept(bob.page);
    await alice.page.evaluate(() => window.delivery.sync());
    expect((await alice.page.evaluate(() => window.delivery.status())).inbox).toContain('Pending text must survive cleanup');
    const sent = await bob.page.evaluate(() => window.delivery.status());
    await bob.page.evaluate(() => window.delivery.clearHistory());
    expect(await bob.page.evaluate(() => window.delivery.status())).toEqual({...sent,messages:[],inbox:[]});
  } finally { await alice.context.close(); await bob.context.close(); }
});


test('a pending send older than the retention window is dropped once, not on every retry',async ({browser}) => {
  const {alice,bob} = await pair(browser);
  try {
    await bob.page.evaluate(() => window.delivery.stageSend('Stale pending'));
    await bob.page.clock.setSystemTime(new Date(Date.now()+91*86400_000));
    await expect(bob.page.evaluate(() => window.delivery.submit())).rejects.toThrow('was not sent');
    expect((await bob.page.evaluate(() => window.delivery.status())).pending).toBe(false);
    await expect(bob.page.evaluate(() => window.delivery.submit())).rejects.toThrow('No pending delivery');
  } finally { await alice.context.close(); await bob.context.close(); }
});

test('legacy transcript parsing preserves unknown expiry and rejects invalid deadlines',() => {
  const old = {version:1,identity:'legacy',keyPackage:'',keys:null,pins:[],state:null,pending:null,inbox:['Keep legacy text'],outbox:[],cursor:0,awaitingCommit:false,received:[]};
  expect(parseRecord(old).inbox).toEqual([{text:'Keep legacy text',expiresAt:null,sequence:null}]);
  const saved = {device:null,token:'',room:null,roster:null,pending:null,messages:[{id:'event',sender:'legacy',text:'Keep legacy text',sequence:1}]};
  expect(connection(saved).messages[0]?.expiresAt).toBeNull();
  for (const expiresAt of [-1,1.5,'100',Infinity]) {
    expect(() => parseRecord({...old,inbox:[{text:'Bad deadline',expiresAt}]})).toThrow('Invalid transcript expiry');
    expect(() => connection({...saved,messages:[{...saved.messages[0],expiresAt}]})).toThrow('Invalid transcript expiry');
  }
});

test('removing a vault prevents an in-flight room write from reviving it after replacement',async ({browser}) => {
  const alice = await device(browser,'forget-race');
  const other = await alice.context.newPage();
  try {
    await alice.page.evaluate(() => window.delivery.createRoom('Root room'));
    await alice.page.evaluate(() => window.delivery.stageCommit());
    await accept(alice.page);
    const second = await alice.page.evaluate(() => window.delivery.createRoom('Separate room'));
    await alice.page.evaluate(() => window.delivery.stageCommit());
    await accept(alice.page);
    await other.goto('/');
    await other.waitForFunction(() => Boolean(window.delivery));
    await other.evaluate(async ({password,session,room}) => {
      await window.proof.unlock(password);
      window.delivery.connect(session);
      await window.delivery.selectRoom(room);
      const encrypt = crypto.subtle.encrypt.bind(crypto.subtle);
      crypto.subtle.encrypt = async (...args: Parameters<SubtleCrypto['encrypt']>) => {
        crypto.subtle.encrypt = encrypt;
        document.body.dataset.encryptionPaused = 'true';
        await new Promise<void>(resolve => window.addEventListener('release-encryption',() => resolve(),{once:true}));
        return encrypt(...args);
      };
    },{password,session:alice.session,room:second});
    const pending = other.evaluate(() => window.delivery.stageSend('Must never return as durable ciphertext')).then(() => 'unexpected success',error => String(error));
    await expect(other.locator('body')).toHaveAttribute('data-encryption-paused','true');
    await alice.page.evaluate(() => window.proof.forget());
    await alice.page.evaluate(password => window.proof.initialize('replacement-identity',password),password);
    await other.evaluate(() => window.dispatchEvent(new Event('release-encryption')));
    expect(await pending).toContain('Vault removed or replaced');
    expect((await alice.page.evaluate(() => window.proof.status())).identity).toBe('replacement-identity');
    const entries = await alice.page.evaluate(async () => {
      const db = await new Promise<IDBDatabase>((resolve,reject) => {
        const open = indexedDB.open('kymessages-mls-proof-v1',1);
        open.onsuccess = () => resolve(open.result); open.onerror = () => reject(open.error);
      });
      try { return await new Promise<IDBValidKey[]>((resolve,reject) => {
        const tx = db.transaction('vault','readonly'); const keys = tx.objectStore('vault').getAllKeys();
        tx.oncomplete = () => resolve(keys.result); tx.onabort = () => reject(tx.error);
      }); } finally { db.close(); }
    });
    expect(entries).toEqual(['device']);
  } finally { await alice.context.close(); }
});

test('history byte limits count serialized escaping rather than only visible text', () => {
  const messages = Array.from({length:256}, (_, index) => ({text:'\0'.repeat(4096),index}));
  const removed = trimSavedMessages(messages);
  expect(removed).toBeGreaterThan(200);
  expect(messages.at(-1)?.index).toBe(255);
  expect(new TextEncoder().encode(JSON.stringify(messages)).length).toBeLessThanOrEqual(maxSavedMessageBytes);
});

test('a full saved cache accepts new encrypted traffic without losing a pending send', async ({browser}) => {
  const {alice,bob} = await pair(browser);
  try {
    // Populate only synthetic display history, preserving the actual live MLS state
    // and server cursor. This checks the cache boundary, not sustained server load.
    const root = await bob.page.evaluate(async password => {
      // Use the same authenticated local format as earlier vault fault drills.
      const db = await new Promise<IDBDatabase>((resolve,reject) => {
        const request = indexedDB.open('kymessages-mls-proof-v1');
        request.onsuccess = () => resolve(request.result); request.onerror = () => reject(request.error);
      });
      try {
        const saved = await new Promise<{version:number;salt:string;iv:string;ciphertext:string}>((resolve,reject) => {
          const tx = db.transaction('vault'); const read = tx.objectStore('vault').get('device');
          tx.oncomplete = () => resolve(read.result); tx.onabort = () => reject(tx.error);
        });
        const bytes = (value:string) => Uint8Array.from(atob(value),c => c.charCodeAt(0));
        const keyMaterial = await crypto.subtle.importKey('raw',new TextEncoder().encode(password),'PBKDF2',false,['deriveKey']);
        const key = await crypto.subtle.deriveKey({name:'PBKDF2',hash:'SHA-256',iterations:600000,salt:bytes(saved.salt)},keyMaterial,{name:'AES-GCM',length:256},false,['encrypt','decrypt']);
        const aad = new TextEncoder().encode('kymessages-mls-proof/v1');
        const raw: unknown = JSON.parse(new TextDecoder().decode(await crypto.subtle.decrypt({name:'AES-GCM',iv:bytes(saved.iv),additionalData:aad},key,bytes(saved.ciphertext))));
        if (!raw || typeof raw !== 'object' || !('delivery' in raw) || typeof raw.delivery !== 'string') throw new Error('Expected saved connection');
        const connection: unknown = JSON.parse(raw.delivery);
        if (!connection || typeof connection !== 'object') throw new Error('Expected connection');
        const seed = Array.from({length:256},(_,i) => ({id:`seed-${i}`,sender:'synthetic',text:'x'.repeat(850),sequence:0,expiresAt:null}));
        Object.assign(connection,{messages:seed});
        Object.assign(raw,{delivery:JSON.stringify(connection),inbox:seed.map(item => ({text:item.text,expiresAt:null,sequence:0}))});
        const iv = crypto.getRandomValues(new Uint8Array(12));
        const encrypted = await crypto.subtle.encrypt({name:'AES-GCM',iv,additionalData:aad},key,new TextEncoder().encode(JSON.stringify(raw)));
        const b64 = (value:Uint8Array) => btoa(Array.from(value,c => String.fromCharCode(c)).join(''));
        await new Promise<void>((resolve,reject) => {
          const tx = db.transaction('vault','readwrite',{durability:'strict'});
          tx.objectStore('vault').put({...saved,iv:b64(iv),ciphertext:b64(new Uint8Array(encrypted))},'device');
          tx.oncomplete = () => resolve(); tx.onabort = () => reject(tx.error);
        });
        return true;
      } finally { db.close(); }
    },password);
    expect(root).toBe(true);
    await bob.page.evaluate(() => window.delivery.stageSend('Pending send survives cache trimming'));
    const pending = await bob.page.evaluate(() => window.delivery.status());
    const latest = 'Latest encrypted message ' + '\0'.repeat(4000);
    await alice.page.evaluate(text => window.delivery.stageSend(text), latest);
    await accept(alice.page);
    await bob.page.evaluate(() => window.delivery.sync());
    const after = await bob.page.evaluate(() => window.delivery.status());
    expect(after.cursor).toBe(pending.cursor + 1);
    expect(after.messages.length).toBeLessThanOrEqual(256);
    expect(after.historyPruned).toBeGreaterThan(0);
    expect(after.messages.at(-1)?.text).toBe(latest);
    expect(after.pendingText).toBe(pending.pendingText);
    await reload(bob.page,bob.session);
    expect(await bob.page.evaluate(() => window.delivery.status())).toEqual(after);
    await accept(bob.page);
    await alice.page.evaluate(() => window.delivery.sync());
    expect((await alice.page.evaluate(() => window.delivery.status())).messages.at(-1)?.text).toBe(pending.pendingText);
  } finally { await alice.context.close(); await bob.context.close(); }
});
