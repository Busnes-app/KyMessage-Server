import { test, expect, type Browser, type Page } from '@playwright/test';

const password = 'disposable OIDC vault passphrase';
test.use({actionTimeout:10_000});
async function click(page: Page, name: string, message: string) {
  await page.getByRole('button',{name,exact:true}).click();
  await expect(page.getByRole('status')).toContainText(message);
}
async function login(page: Page, subject: string) {
  await page.getByRole('link',{name:'Sign in with suite identity'}).click();
  await page.getByLabel('Test identity').fill(subject);
  const auth = new URL(page.url());
  expect(auth.searchParams.get('code_challenge_method')).toBe('S256');
  await page.getByRole('button',{name:'Continue to KyMessages'}).click();
  await page.waitForURL('**/chat.html?auth=oidc');
  await expect(page.locator('#suite-account')).toContainText('Account ID: usr_');
  const id = (await page.locator('#suite-account').innerText()).match(/usr_[a-f0-9]{24}/)?.[0];
  if (!id) throw new Error('Missing server account ID');
  return id;
}
async function open(browser: Browser, subject: string, controlledClock = false) {
  const context = await browser.newContext();
  const page = await context.newPage();
  if (controlledClock) await page.clock.install();
  await page.goto('/chat.html?auth=oidc');
  await expect(page.locator('#access-form')).toBeHidden();
  const id = await login(page,subject);
  await page.getByLabel('Local passphrase').fill(password);
  await click(page,'Create test device','Test device connected');
  return {context,page,id,subject,fingerprint:await page.locator('#own-fingerprint').innerText()};
}
async function verify(page: Page, id: string, fingerprint: string) {
  const device = await page.locator('#peer-device option').filter({hasText:id}).getAttribute('value');
  if (!device) throw new Error('Missing peer');
  await page.getByLabel('Room device',{exact:true}).selectOption(device);
  await page.getByLabel('Fingerprint from your teammate').fill(fingerprint);
  await click(page,'Verify teammate','Teammate verified locally');
}
async function unlock(page: Page) {
  await page.getByLabel('Local passphrase').fill(password);
  await click(page,'Unlock existing device','Test device connected');
}

test('suite redirect and signed callback bind cookie-authenticated devices, chat and reauthentication',async ({browser}) => {
  const subject = 'oidc-alice-' + crypto.randomUUID().slice(0,8);
  const alice = await open(browser,subject,true);
  const bob = await open(browser,'oidc-bob-' + crypto.randomUUID().slice(0,8));
  try {
    expect((await alice.page.request.post('/proof-fixture/session/bypass')).status()).toBe(404);
    const cookies = await alice.context.cookies();
    expect(cookies.find(c => c.name === 'ky_session')?.httpOnly).toBe(true);
    expect(await alice.page.evaluate(() => document.cookie)).not.toContain('ky_session=');
    const requests: {url:string;body:string;headers:Record<string,string>}[] = [];
    alice.page.on('request',r => { if (r.url().includes('/api/messaging/')) requests.push({url:r.url(),body:r.postData() ?? '',headers:r.headers()}); });
    // The browser must send the existing CSRF cookie/header pair with writes.
    await alice.page.route('**/api/messaging/rooms',async route => {
      if (route.request().method() !== 'POST') return route.continue();
      const headers = route.request().headers();
      delete headers['x-csrf-token'];
      await route.continue({headers});
    });
    await alice.page.getByLabel('New room name').fill('OIDC team');
    await click(alice.page,'Create room','Invalid CSRF token');
    await alice.page.unroute('**/api/messaging/rooms');
    await click(alice.page,'Create room','Room created');
    await click(alice.page,'Apply verified membership','Verified membership applied');
    await alice.page.getByLabel("Teammate's account ID").fill(bob.id);
    await click(alice.page,'Invite teammate','Invitation sent');
    await click(bob.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await click(bob.page,'Accept OIDC team','Room selected');
    await click(bob.page,'Prepare to join','Ready to join');
    await click(alice.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await verify(alice.page,bob.id,bob.fingerprint);
    await verify(bob.page,alice.id,alice.fingerprint);
    await click(alice.page,'Apply verified membership','Verified membership applied');
    await click(bob.page,'Check for messages','Messages checked');
    const secret = 'Only the enrolled OIDC browsers read this message';
    await alice.page.route('**/api/messaging/rooms/*/events',async route => {
      expect((await route.fetch()).status()).toBe(200);
      await route.abort('failed');
    },{times:1});
    await alice.page.getByLabel('Message',{exact:true}).fill(secret);
    await alice.page.getByRole('button',{name:'Send encrypted message',exact:true}).click();
    await expect(alice.page.locator('#pending-text')).toContainText(secret);
    await alice.context.clearCookies();
    await click(alice.page,'Refresh rooms and devices','changed or expired');
    await expect(alice.page.locator('#workspace')).toBeHidden();
    await expect(alice.page.locator('#pending-text')).toBeEmpty();
    expect(await login(alice.page,subject)).toBe(alice.id);
    await unlock(alice.page);
    await expect(alice.page.locator('#pending-text')).toContainText(secret);
    await click(alice.page,'Retry pending delivery','Pending delivery confirmed');
    const sends = requests.filter(r => r.url.endsWith('/events') && r.body.includes('"kind":"application"'));
    expect(sends).toHaveLength(2);
    expect(sends[0]?.body).toBe(sends[1]?.body);
    await click(bob.page,'Check for messages','Messages checked');
    await expect(bob.page.locator('#messages')).toContainText(secret);
    for (const r of requests) { expect(r.headers.authorization).toBeUndefined(); expect(r.body).not.toContain(secret); expect(r.body).not.toContain(password); }
    await alice.page.reload();
    await expect(alice.page.locator('#workspace')).toBeHidden();
    await expect(alice.page.locator('#suite-account')).toContainText(alice.id);
    await unlock(alice.page);
    await expect(alice.page.locator('#messages')).toContainText(secret);
    await click(alice.page,'Lock and disconnect','Device locked');
    await click(alice.page,'Sign out of suite','Signed out of suite');
    expect((await alice.context.cookies()).find(c => c.name === 'ky_session')).toBeUndefined();
    const other = await login(alice.page,'other-' + crypto.randomUUID().slice(0,8));
    expect(other).not.toBe(alice.id);
    await alice.page.getByLabel('Local passphrase').fill(password);
    await click(alice.page,'Unlock existing device','belongs to a different account');
    await expect(alice.page.locator('#workspace')).toBeHidden();
    await expect(alice.page.locator('#messages')).toBeEmpty();
    await click(alice.page,'Sign out of suite','Signed out of suite');
    expect(await login(alice.page,subject)).toBe(alice.id);
    await unlock(alice.page);
    await expect(alice.page.locator('#messages')).toContainText(secret);
    expect(await alice.page.locator('#own-fingerprint').innerText()).toBe(alice.fingerprint);
    await alice.page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await alice.context.clearCookies();
    await alice.page.clock.runFor(10_000);
    await expect(alice.page.locator('#workspace')).toBeHidden();
    await expect(alice.page.locator('#messages')).toBeEmpty();
    await expect(alice.page.getByRole('status')).toContainText('changed or expired');
  } finally { await alice.context.close(); await bob.context.close(); }
});

test('the real callback rejects altered nonce and PKCE before creating a session',async ({browser}) => {
  for (const parameter of ['nonce','code_challenge']) {
    const context = await browser.newContext();
    const page = await context.newPage();
    try {
      await page.goto('/chat.html?auth=oidc');
      await page.getByRole('link',{name:'Sign in with suite identity'}).click();
      // Wait for the identity-provider navigation before altering the auth request.
      await page.getByLabel('Test identity').waitFor();
      const authorize = new URL(page.url());
      authorize.searchParams.set(parameter,parameter === 'nonce' ? 'incorrect-nonce' : 'Z'.repeat(43));
      await page.goto(authorize.toString());
      await page.getByLabel('Test identity').fill('rejected-' + crypto.randomUUID().slice(0,8));
      const failed = page.waitForResponse(r => r.url().includes('/api/sso/kysignon/callback'));
      await page.getByRole('button',{name:'Continue to KyMessages'}).click();
      expect((await failed).status()).toBe(401);
      expect((await context.cookies()).some(c => c.name === 'ky_session')).toBe(false);
      expect((await page.request.get('/api/auth/me')).headers()['cache-control']).toBe('no-store');
    } finally { await context.close(); }
  }
});

test('confirmed identity reset revokes old browsers and rejoins only future verified traffic',async ({browser}) => {
  test.setTimeout(60_000);
  const subject = 'reset-' + crypto.randomUUID().slice(0,8);
  const lost = await open(browser,subject);
  const owner = await open(browser,'owner-' + crypto.randomUUID().slice(0,8));
  const replacement = await open(browser,subject);
  try {
    await owner.page.getByLabel('New room name').fill('Recovery team');
    await click(owner.page,'Create room','Room created');
    await click(owner.page,'Apply verified membership','Verified membership applied');
    await owner.page.getByLabel("Teammate's account ID").fill(lost.id);
    await click(owner.page,'Invite teammate','Invitation sent');
    await click(lost.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await click(lost.page,'Accept Recovery team','Room selected');
    await click(lost.page,'Prepare to join','Ready to join');
    await click(owner.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await verify(owner.page,lost.id,lost.fingerprint);
    await verify(lost.page,owner.id,owner.fingerprint);
    await click(owner.page,'Apply verified membership','Verified membership applied');
    await click(lost.page,'Check for messages','Messages checked');
    await owner.page.getByLabel('Message',{exact:true}).fill('Before the identity reset');
    await click(owner.page,'Send encrypted message','Message accepted');
    await click(lost.page,'Check for messages','Messages checked');
    const sessionBefore = (await replacement.context.cookies()).find(c => c.name === 'ky_session')?.value;
    replacement.page.once('dialog',dialog => dialog.dismiss());
    await click(replacement.page,'Reset messaging identity','Identity reset cancelled');
    await expect(replacement.page.locator('#signed-in')).toContainText('pending');
    replacement.page.once('dialog',dialog => dialog.accept());
    await replacement.page.getByRole('button',{name:'Reset messaging identity',exact:true}).click();
    await expect(replacement.page.getByLabel('Test identity')).toBeVisible();
    const auth = new URL(replacement.page.url());
    expect(auth.searchParams.get('prompt')).toBe('login');
    expect(auth.searchParams.get('max_age')).toBe('0');
    await replacement.page.getByLabel('Test identity').fill(subject);
    await replacement.page.getByRole('button',{name:'Continue to KyMessages'}).click();
    await replacement.page.waitForURL('**/chat.html?auth=oidc');
    expect((await replacement.context.cookies()).find(c => c.name === 'ky_session')?.value).toBe(sessionBefore);
    await unlock(replacement.page);
    await expect(replacement.page.locator('#signed-in')).toContainText('approved · Identity 2');
    await expect(replacement.page.locator('#rooms')).toBeEmpty();
    await click(lost.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await expect(lost.page.locator('#signed-in')).toContainText('revoked');
    await expect(lost.page.locator('#messages')).toContainText('Before the identity reset');
    await expect(lost.page.locator('#send')).toBeDisabled();
    await click(owner.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await expect(owner.page.locator('#send')).toBeDisabled();
    await expect(owner.page.locator('#members')).toContainText('identity changed from 1 to 2');
    await click(owner.page,'Apply verified membership','Verified membership applied');
    await owner.page.getByLabel("Teammate's account ID").fill(lost.id);
    await click(owner.page,'Invite teammate','Invitation sent');
    await click(replacement.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await click(replacement.page,'Accept Recovery team','Room selected');
    await click(replacement.page,'Prepare to join','Ready to join');
    await click(owner.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await expect(owner.page.locator('#peers')).toContainText('Identity 2');
    await click(owner.page,'Apply verified membership','Unpinned roster identity/key');
    await verify(owner.page,replacement.id,replacement.fingerprint);
    await verify(replacement.page,owner.id,owner.fingerprint);
    await click(owner.page,'Apply verified membership','Verified membership applied');
    await click(replacement.page,'Check for messages','Messages checked');
    await owner.page.getByLabel('Message',{exact:true}).fill('After independent verification');
    await click(owner.page,'Send encrypted message','Message accepted');
    await click(replacement.page,'Check for messages','Messages checked');
    await expect(replacement.page.locator('#messages')).toContainText('After independent verification');
    await expect(replacement.page.locator('#messages')).not.toContainText('Before the identity reset');
    // A later browser enrolls directly into generation 2, without needing reload
    // to repair a generation-1 local self pin.
    const extra = await open(browser,subject);
    try {
      await click(replacement.page,'Refresh rooms and devices','Rooms and devices refreshed');
      await replacement.page.getByText('Your devices',{exact:true}).click();
      const target = await replacement.page.locator('#pending-device option:not([value=""])').first().getAttribute('value');
      if (!target) throw new Error('Missing generation-2 pending device');
      await replacement.page.getByLabel('Pending device',{exact:true}).selectOption(target);
      await replacement.page.getByLabel('Fingerprint from that browser').fill(extra.fingerprint);
      await click(replacement.page,'Approve own device','Own device approved');
      await click(extra.page,'Refresh rooms and devices','Rooms and devices refreshed');
      await extra.page.getByText('Your devices',{exact:true}).click();
      const prior = await extra.page.locator('#revoke-device option').filter({hasText:'approved'}).filter({hasNotText:'This browser'}).getAttribute('value');
      if (!prior) throw new Error('Missing prior replacement');
      await extra.page.getByLabel('Device to revoke').selectOption(prior);
      extra.page.once('dialog',dialog => dialog.accept());
      await click(extra.page,'Revoke device','Device revoked');
      await extra.page.getByLabel('New room name').fill('New identity room');
      await click(extra.page,'Create room','Room created');
      await click(extra.page,'Apply verified membership','Verified membership applied');
    } finally { await extra.context.close(); }

  } finally {
    await Promise.all([lost.context.close(),owner.context.close(),replacement.context.close()]);
  }
});

test('live wakeups fetch encrypted history without a poll and reconnect after offline',async ({browser}) => {
  const alice = await open(browser,'live-a-' + crypto.randomUUID().slice(0,8));
  const bob = await open(browser,'live-b-' + crypto.randomUUID().slice(0,8),true);
  const wakeFrames: string[] = [];
  const urls: string[] = [];
  let closed = 0;
  bob.page.on('websocket',socket => {
    urls.push(socket.url());
    socket.on('framereceived',frame => wakeFrames.push(String(frame.payload)));
    socket.on('close',() => closed++);
  });
  try {
    await alice.page.getByLabel('Direct message account ID').fill(bob.id);
    await click(alice.page,'Start direct message','Direct conversation selected');
    await click(alice.page,'Apply verified membership','Verified membership applied');
    await click(bob.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await click(bob.page,'Accept Direct: ' + bob.id,'Room selected');
    await click(bob.page,'Prepare to join','Ready to join');
    await click(alice.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await verify(alice.page,bob.id,bob.fingerprint);
    await verify(bob.page,alice.id,alice.fingerprint);
    await click(alice.page,'Apply verified membership','Verified membership applied');
    await expect(bob.page.locator('#send')).toBeEnabled();
    await bob.page.clock.pauseAt(new Date(Date.now()+1000));
    await bob.page.getByLabel('Message',{exact:true}).fill('Keep this draft');
    await alice.page.getByLabel('Message',{exact:true}).fill('Live encrypted message');
    await click(alice.page,'Send encrypted message','Message accepted');
    await expect(bob.page.locator('#messages')).toContainText('Live encrypted message');
    await expect(bob.page.getByLabel('Message',{exact:true})).toHaveValue('Keep this draft');
    await expect(bob.page.getByLabel('Message',{exact:true})).toBeFocused();
    await expect.poll(() => wakeFrames.length).toBeGreaterThan(0);
    for (const frame of wakeFrames) {
      const parsed: unknown = JSON.parse(frame);
      expect(parsed).toMatchObject({kind:'wake'});
      expect(frame).not.toContain('Live encrypted message');
      expect(frame).not.toContain(password);
    }
    await bob.context.setOffline(true);
    await expect.poll(() => closed).toBeGreaterThan(0);
    const prior = urls.length;
    await alice.page.getByLabel('Message',{exact:true}).fill('Catch up after offline');
    await click(alice.page,'Send encrypted message','Message accepted');
    await expect(bob.page.locator('#messages')).not.toContainText('Catch up after offline');
    await bob.context.setOffline(false);
    await expect.poll(() => urls.length).toBeGreaterThan(prior);
    await expect(bob.page.locator('#messages')).toContainText('Catch up after offline');
    await expect(bob.page.locator('#messages li')).toHaveCount(2);
    for (const url of urls) expect(new URL(url).search).toBe('');
    const closedBefore = closed;
    await click(bob.page,'Lock and disconnect','Device locked');
    await expect.poll(() => closed).toBeGreaterThan(closedBefore);
    await expect(bob.page.locator('#messages')).toBeEmpty();
  } finally { await alice.context.close(); await bob.context.close(); }
});

test('cross-tab removal cancels setup waiting for the account response',async ({browser}) => {
  const owner = await open(browser,'cancel-setup-' + crypto.randomUUID().slice(0,8));
  const waiting = await owner.context.newPage();
  let release = () => {};
  const held = new Promise<void>(resolve => { release = resolve; });
  try {
    await waiting.goto('/chat.html?auth=oidc');
    await expect(waiting.locator('#suite-account')).toContainText(owner.id);
    let intercepted = false;
    await waiting.route('**/api/auth/me',async route => {
      if (!intercepted) { intercepted = true; await held; }
      await route.continue();
    });
    await waiting.getByLabel('Local passphrase').fill(password);
    await waiting.getByRole('button',{name:'Create test device',exact:true}).click();
    await expect.poll(() => intercepted).toBe(true);
    owner.page.once('dialog',dialog => dialog.accept());
    await click(owner.page,'Remove this browser’s local messaging data','Local messaging data removed');
    await expect(waiting.getByRole('status')).toContainText('removed in another tab');
    release();
    await expect(waiting.getByRole('button',{name:'Create test device',exact:true})).toBeEnabled();
    // No new root may appear after the removal notice and canceled setup finish.
    const entries = await owner.page.evaluate(() => new Promise<number>((resolve,reject) => {
      const request = indexedDB.open('kymessages-mls-proof-v1',1);
      request.onerror = () => reject(request.error);
      request.onsuccess = () => {
        const db = request.result;
        const tx = db.transaction('vault','readonly');
        const count = tx.objectStore('vault').count();
        tx.oncomplete = () => { db.close(); resolve(count.result); };
        tx.onabort = () => { db.close(); reject(tx.error); };
      };
    }));
    expect(entries).toBe(0);
    await expect(waiting.locator('#workspace')).toBeHidden();
    await expect(waiting.getByRole('status')).toContainText('removed in another tab');
    // A subsequent explicit setup remains available and requires fresh input.
    await waiting.unroute('**/api/auth/me');
    await waiting.getByLabel('Local passphrase').fill(password);
    await click(waiting,'Create test device','Test device connected');
    await expect(waiting.locator('#signed-in')).toContainText('pending');
  } finally { release(); await owner.context.close(); }
});

type Member = Awaited<ReturnType<typeof open>>;
async function sharedRoom(owner: Member, peer: Member, name: string) {
  await owner.page.getByLabel('New room name').fill(name);
  await click(owner.page,'Create room','Room created');
  await click(owner.page,'Apply verified membership','Verified membership applied');
  await owner.page.getByLabel("Teammate's account ID").fill(peer.id);
  await click(owner.page,'Invite teammate','Invitation sent');
  await click(peer.page,'Refresh rooms and devices','Rooms and devices refreshed');
  await click(peer.page,'Accept ' + name,'Room selected');
  await click(peer.page,'Prepare to join','Ready to join');
  await click(owner.page,'Refresh rooms and devices','Rooms and devices refreshed');
  await verify(owner.page,peer.id,peer.fingerprint);
  await verify(peer.page,owner.id,owner.fingerprint);
  await click(owner.page,'Apply verified membership','Verified membership applied');
  await click(peer.page,'Check for messages','Messages checked');
}
async function send(from: Page, to: Page, message: string) {
  await from.getByLabel('Message',{exact:true}).fill(message);
  await click(from,'Send encrypted message','Message accepted');
  await click(to,'Check for messages','Messages checked');
  await expect(to.locator('#messages')).toContainText(message);
}
// Mirrors restore-messages: the server keeps the approved device but drops its token.
async function suspend(member: Member) {
  expect((await member.page.request.post('/proof-fixture/suspend-devices/' + member.id)).status()).toBe(204);
  await click(member.page,'Refresh rooms and devices','Rooms and devices refreshed');
  await expect(member.page.locator('#signed-in')).toContainText('Device suspended');
  await expect(member.page.locator('#room-state')).toContainText('This device is suspended after a server restore');
  await expect(member.page.locator('#send')).toBeDisabled();
}

test('a device suspended by a message restore resumes its unchanged thread',async ({browser}) => {
  const alice = await open(browser,'resume-a-' + crypto.randomUUID().slice(0,8));
  const bob = await open(browser,'resume-b-' + crypto.randomUUID().slice(0,8));
  try {
    await sharedRoom(alice,bob,'Restored team');
    await send(alice.page,bob.page,'Before the restore');
    // A second saved room holds its own copy of the device token.
    await alice.page.getByLabel('New room name').fill('Second room');
    await click(alice.page,'Create room','Room created');
    await click(alice.page,'Open Restored team','Room selected');
    await suspend(alice);
    await expect(alice.page.locator('#messages')).toContainText('Before the restore');
    await click(alice.page,'Resume this device','Device resumed');
    await expect(alice.page.locator('#signed-in')).toContainText('Device approved');
    await expect(alice.page.getByRole('button',{name:'Resume this device'})).toBeHidden();
    await send(alice.page,bob.page,'After the restore');
    await send(bob.page,alice.page,'Reply after the restore');
    await click(alice.page,'Open Second room','Room selected');
  } finally { await alice.context.close(); await bob.context.close(); }
});

test('a resumed device follows the epoch its room advanced to while suspended',async ({browser}) => {
  const alice = await open(browser,'epoch-a-' + crypto.randomUUID().slice(0,8));
  const bob = await open(browser,'epoch-b-' + crypto.randomUUID().slice(0,8));
  try {
    await sharedRoom(alice,bob,'Advancing team');
    await send(alice.page,bob.page,'Before the restore');
    await suspend(alice);
    await click(bob.page,'Apply verified membership','Verified membership applied');
    await bob.page.getByLabel('Message',{exact:true}).fill('Sent in the next epoch');
    await click(bob.page,'Send encrypted message','Message accepted');
    // A stale suite sign-in at either step sends the browser through fresh authentication.
    for (const step of ['resume','resume/verify']) {
      await alice.page.route('**/api/messaging/devices/*/' + step,route => route.fulfill({status:403,contentType:'application/json',body:JSON.stringify({error:'Sign in again',code:'reauthentication_required',reauth_url:'/api/sso/kysignon/login?fresh=1'})}),{times:1});
      await alice.page.getByRole('button',{name:'Resume this device',exact:true}).click();
      await alice.page.getByLabel('Test identity').fill(alice.subject);
      await alice.page.getByRole('button',{name:'Continue to KyMessages'}).click();
      await alice.page.waitForURL('**/chat.html?auth=oidc');
      await unlock(alice.page);
      await expect(alice.page.locator('#room-state')).toContainText('This device is suspended after a server restore');
      await expect(alice.page.locator('#room-tools')).toBeHidden();
    }
    await click(alice.page,'Resume this device','Device resumed');
    await click(alice.page,'Check for messages','Messages checked');
    await expect(alice.page.locator('#messages')).toContainText('Sent in the next epoch');
    // Only a successful background check after the resume sets this text.
    await expect(alice.page.locator('#poll-state')).toHaveText('Automatic checks active.',{timeout:20_000});
    await send(alice.page,bob.page,'Caught up after resuming');
  } finally { await alice.context.close(); await bob.context.close(); }
});

for (const deadConfirm of [false,true]) test(`a resume whose verify response was lost recovers on the next unlock${deadConfirm ? ', even after a failed confirm' : ''}`,async ({browser}) => {
  const alice = await open(browser,'lost-a-' + crypto.randomUUID().slice(0,8));
  const bob = await open(browser,'lost-b-' + crypto.randomUUID().slice(0,8));
  try {
    await sharedRoom(alice,bob,'Lost reply team');
    await send(alice.page,bob.page,'Before the restore');
    await alice.page.getByLabel('New room name').fill('Second room');
    await click(alice.page,'Create room','Room created');
    await click(alice.page,'Open Lost reply team','Room selected');
    await suspend(alice);
    // The server commits the resume; the browser never sees the reply.
    await alice.page.route('**/api/messaging/devices/*/resume/verify',async route => {
      expect((await route.fetch()).status()).toBe(200);
      await route.abort('failed');
    },{times:1});
    await alice.page.getByRole('button',{name:'Resume this device',exact:true}).click();
    await expect(alice.page.getByRole('status')).not.toContainText('Working');
    await expect(alice.page.getByRole('status')).not.toContainText('Device resumed');
    await alice.page.reload();
    if (deadConfirm) {
      // The first read after unlock confirms the pending token. Fake its refusal; the old
      // token really is dead, so nothing proves the pending one wrong and it must be kept.
      await alice.page.route('**/api/messaging/rooms',route => route.fulfill({status:403,contentType:'application/json',body:JSON.stringify({error:'Messaging access denied'})}),{times:1});
      await alice.page.getByLabel('Local passphrase').fill(password);
      await click(alice.page,'Unlock existing device','could not confirm its resumed credential');
    }
    await unlock(alice.page);
    await expect(alice.page.locator('#signed-in')).toContainText('Device approved');
    await send(alice.page,bob.page,'After the lost reply');
    await click(alice.page,'Open Second room','Room selected');
  } finally { await alice.context.close(); await bob.context.close(); }
});
