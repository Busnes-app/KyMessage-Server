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
async function open(browser: Browser, subject: string) {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto('/chat.html?auth=oidc');
  await expect(page.locator('#access-form')).toBeHidden();
  const id = await login(page,subject);
  await page.getByLabel('Local passphrase').fill(password);
  await click(page,'Create test device','Test device connected');
  return {context,page,id,fingerprint:await page.locator('#own-fingerprint').innerText()};
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
  const alice = await open(browser,subject);
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
    await alice.page.clock.install();
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
