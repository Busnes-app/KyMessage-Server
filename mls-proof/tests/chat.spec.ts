import { test, expect, type Browser, type Page } from '@playwright/test';

test.use({actionTimeout:10_000});

const password = 'only a disposable demo passphrase';
async function click(page: Page, name: string, result: string) {
  await page.getByRole('button',{name,exact:true}).click();
  await expect(page.getByRole('status')).toContainText(result);
}
async function start(browser: Browser, user: string) {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto('/chat.html');
  await page.getByLabel('Test account',{exact:true}).fill(user);
  await page.getByLabel('Local passphrase').fill(password);
  await click(page,'Create test device','Test device connected');
  return {context,page,fingerprint:await page.locator('#own-fingerprint').innerText()};
}
async function unlock(page: Page, user: string) {
  await page.getByLabel('Test account',{exact:true}).fill(user);
  await page.getByLabel('Local passphrase').fill(password);
  await click(page,'Unlock existing device','Test device connected');
}
async function verify(page: Page, user: string, fingerprint: string) {
  const option = page.locator('#peer-device option').filter({hasText:user});
  const id = await option.getAttribute('value');
  if (!id) throw new Error('Missing peer device');
  await page.getByLabel('Room device',{exact:true}).selectOption(id);
  await page.getByLabel('Fingerprint from your teammate').fill(fingerprint);
  await click(page,'Verify teammate','Teammate verified locally');
}

test('clickable chat: verified invitation, ciphertext retry, durable history, lock and mobile themes',async ({browser}) => {
  const aliceName = 'ui-alice-' + crypto.randomUUID().slice(0,8);
  const bobName = 'ui-bob-' + crypto.randomUUID().slice(0,8);
  const alice = await start(browser,aliceName);
  const bob = await start(browser,bobName);
  try {
    await alice.page.getByLabel('New room name').fill('Design room');
    await click(alice.page,'Create room','Room created');
    await click(alice.page,'Apply verified membership','Verified membership applied');
    await alice.page.getByLabel("Teammate's account ID").fill(bobName);
    await click(alice.page,'Invite teammate','Invitation sent');
    await click(bob.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await bob.page.route('**/api/messaging/rooms/*/join',async route => {
      expect((await route.fetch()).status()).toBe(200);
      await route.abort('failed');
    },{times:1});
    await bob.page.getByRole('button',{name:'Accept Design room',exact:true}).click();
    await expect(bob.page.getByRole('status')).not.toHaveText('Working…');
    await click(bob.page,'Accept Design room','Room selected');
    await click(bob.page,'Prepare to join','Ready to join');
    await click(alice.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await click(alice.page,'Apply verified membership','Unpinned roster');
    const bobOption = await alice.page.locator('#peer-device option').filter({hasText:bobName}).getAttribute('value');
    if (!bobOption) throw new Error('Missing Bob');
    await alice.page.getByLabel('Room device',{exact:true}).selectOption(bobOption);
    await alice.page.getByLabel('Fingerprint from your teammate').fill('0'.repeat(64));
    await click(alice.page,'Verify teammate','fingerprint mismatch');
    await verify(alice.page,bobName,bob.fingerprint);
    await verify(bob.page,aliceName,alice.fingerprint);
    await click(alice.page,'Apply verified membership','Verified membership applied');
    await click(bob.page,'Check for messages','Messages checked');
    await bob.page.clock.install();
    await bob.page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    const secret = 'Synthetic <img src=x onerror="alert(1)"> chat message';
    const requests: string[] = [];
    alice.page.on('request',r => { if (r.method() === 'POST' && r.url().endsWith('/events')) requests.push(r.postData() ?? ''); });
    await alice.page.route('**/api/messaging/rooms/*/events',async route => {
      expect((await route.fetch()).status()).toBe(200);
      await route.abort('failed');
    },{times:1});
    await alice.page.getByLabel('Message',{exact:true}).fill(secret);
    await alice.page.getByRole('button',{name:'Send encrypted message',exact:true}).click();
    await expect(alice.page.locator('#pending')).toBeVisible();
    await expect(alice.page.locator('#pending-text')).toContainText(secret);
    await expect(alice.page.getByRole('button',{name:'Send encrypted message',exact:true})).toBeDisabled();
    await alice.page.reload();
    await expect(alice.page.locator('#workspace')).toBeHidden();
    await expect(alice.page.locator('#messages')).not.toContainText(secret);
    await unlock(alice.page,aliceName);
    await expect(alice.page.locator('#pending-text')).toContainText(secret);
    await click(alice.page,'Retry pending delivery','Pending delivery confirmed');
    expect(requests).toHaveLength(2);
    expect(requests[0]).toBe(requests[1]);
    for (const request of requests) expect(request).not.toContain(secret);
    await expect(alice.page.locator('#messages li')).toHaveCount(1);
    await expect(alice.page.locator('#messages')).toContainText(secret);
    // One failed background read backs off without changing drafts or focus.
    await bob.page.getByLabel('Message',{exact:true}).fill('Unsent draft');
    await bob.page.route('**/events?after=*',route => route.abort('failed'),{times:1});
    await bob.page.clock.runFor(10_000);
    await expect(bob.page.locator('#poll-state')).toContainText('retrying in 20s');
    await expect(bob.page.getByLabel('Message',{exact:true})).toHaveValue('Unsent draft');
    await expect(bob.page.getByLabel('Message',{exact:true})).toBeFocused();
    await bob.page.clock.runFor(20_000);
    await expect(bob.page.locator('#messages')).toContainText(secret);
    await expect(bob.page.locator('#poll-state')).toContainText('Automatic checks active');
    let offlineReads = 0;
    bob.page.on('request',request => { if (request.url().includes('/events?after=')) offlineReads++; });
    await bob.context.setOffline(true);
    await expect.poll(() => bob.page.evaluate(() => navigator.onLine)).toBe(false);
    await bob.page.clock.runFor(60_000);
    expect(offlineReads).toBe(0);
    await bob.context.setOffline(false);
    await expect.poll(() => bob.page.evaluate(() => navigator.onLine)).toBe(true);
    await expect(bob.page.locator('#messages img')).toHaveCount(0);
    await expect(bob.page.locator('#messages')).toContainText(aliceName);
    await click(bob.page,'Check for messages','Messages checked');
    await expect(bob.page.locator('#messages li')).toHaveCount(1);
    await bob.page.getByLabel('Message',{exact:true}).fill('A reply from Bob');
    await bob.page.getByLabel('Message',{exact:true}).press('Tab');
    await expect(bob.page.getByRole('button',{name:'Send encrypted message',exact:true})).toBeFocused();
    await bob.page.keyboard.press('Enter');
    await expect(bob.page.getByRole('status')).toContainText('Message accepted by server');
    await click(alice.page,'Check for messages','Messages checked');
    await expect(alice.page.locator('#messages li')).toHaveCount(2);
    await alice.page.reload();
    await unlock(alice.page,aliceName);
    await expect(alice.page.locator('#messages li')).toHaveCount(2);
    await expect(alice.page.locator('#messages')).toContainText(secret);
    let release = () => {};
    const held = new Promise<void>(resolve => { release = resolve; });
    let reads = 0;
    await bob.page.route('**/events?after=*',async route => {
      reads++;
      await held;
      await route.abort('failed');
    });
    await bob.page.clock.runFor(10_000);
    await expect.poll(() => reads).toBe(1);
    await bob.page.clock.runFor(5_000);
    expect(reads).toBe(1);
    await bob.page.getByRole('button',{name:'Lock and disconnect',exact:true}).click();
    release();
    await expect(bob.page.locator('#workspace')).toBeHidden();
    await bob.page.clock.runFor(60_000);
    expect(reads).toBe(1);
    await expect(bob.page.locator('#messages')).toBeEmpty();
    await bob.page.unroute('**/events?after=*');
    await alice.page.screenshot({path:`test-results/chat-${test.info().project.name}-desktop-light.png`,fullPage:true});
    await alice.page.setViewportSize({width:390,height:844});
    await alice.page.getByLabel('Appearance').selectOption('busnes-dark');
    expect(await alice.page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await alice.page.screenshot({path:`test-results/chat-${test.info().project.name}-mobile-dark.png`,fullPage:true});
    await click(alice.page,'Lock and disconnect','Device locked');
    await expect(alice.page.locator('#device-recovery')).toBeEmpty();
    await alice.page.getByText('Lost a browser or its passphrase?',{exact:true}).click();
    await expect(alice.page.locator('#recovery-help')).toContainText('restoring a server backup cannot recover');
    await expect(alice.page.locator('#recovery-help')).toContainText('Keep any surviving browser data');
    await expect(alice.page.locator('#messages')).toBeEmpty();
    await expect(alice.page.locator('#own-fingerprint')).toBeEmpty();
    await expect(alice.page.getByLabel('Local passphrase')).toHaveValue('');
    await alice.page.reload();
    await expect(alice.page.locator('html')).toHaveAttribute('data-theme','busnes-dark');
    await alice.page.getByLabel('Local passphrase').fill('incorrect but long passphrase');
    await alice.page.getByLabel('Test account',{exact:true}).fill(aliceName);
    await alice.page.getByRole('button',{name:'Unlock existing device',exact:true}).click();
    await expect(alice.page.getByRole('status')).not.toHaveText('Working…');
    await expect(alice.page.locator('#workspace')).toBeHidden();
    await expect(alice.page.getByLabel('Local passphrase')).toHaveValue('');
  } finally { await alice.context.close(); await bob.context.close(); }
});

test('a pending replacement can revoke a lost browser without gaining approval',async ({browser}) => {
  const name = 'ui-lost-' + crypto.randomUUID().slice(0,8);
  const lost = await start(browser,name);
  await lost.context.close();
  const replacement = await start(browser,name);
  try {
    await expect(replacement.page.locator('#device-recovery')).toContainText('If none can be used');
    await replacement.page.getByText('Your devices',{exact:true}).click();
    const target = await replacement.page.locator('#revoke-device option').filter({hasText:'approved'}).getAttribute('value');
    if (!target) throw new Error('Missing lost approved device');
    await replacement.page.getByLabel('Device to revoke').selectOption(target);
    replacement.page.once('dialog',dialog => dialog.accept());
    await click(replacement.page,'Revoke device','Device revoked');
    await expect(replacement.page.locator('#account-devices li').filter({hasText:target})).toContainText('revoked');
    await expect(replacement.page.locator('#device-recovery')).toContainText('No approved devices remain');
    await expect(replacement.page.locator('#signed-in')).toContainText('pending');
    await expect(replacement.page.locator('#approve-own')).toBeDisabled();
    await expect(replacement.page.locator('#create-form')).toBeHidden();
  } finally { await replacement.context.close(); }
});

test('a second browser needs fingerprint-checked approval from its existing device',async ({browser}) => {
  const name = 'ui-device-' + crypto.randomUUID().slice(0,8);
  const first = await start(browser,name);
  await expect(first.page.locator('#device-recovery')).toContainText('only approved device');
  await first.page.getByLabel('New room name').fill('Shared account room');
  await click(first.page,'Create room','Room created');
  await click(first.page,'Apply verified membership','Verified membership applied');
  const second = await start(browser,name);
  try {
    await expect(second.page.locator('#room-state')).toContainText('needs approval');
    await expect(second.page.locator('#device-recovery')).toContainText('accessible approved browser');
    await expect(second.page.locator('#create-form')).toBeHidden();
    await click(first.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await first.page.getByText('Your devices',{exact:true}).click();
    const id = await first.page.locator('#pending-device option').nth(1).getAttribute('value');
    if (!id) throw new Error('Missing pending device');
    await first.page.getByLabel('Pending device',{exact:true}).selectOption(id);
    await first.page.getByLabel('Fingerprint from that browser').fill('0'.repeat(64));
    await click(first.page,'Approve own device','fingerprint mismatch');
    await first.page.getByLabel('Fingerprint from that browser').fill(second.fingerprint);
    await click(first.page,'Approve own device','Own device approved');
    await click(second.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await expect(second.page.locator('#signed-in')).toContainText('Device approved');
    await expect(second.page.locator('#device-recovery')).toBeHidden();
    await expect(first.page.locator('#device-recovery')).toBeHidden();
    await expect(second.page.locator('#create-form')).toBeVisible();
    await click(second.page,'Open Shared account room','Room selected');
    await click(second.page,'Prepare to join','Ready to join');
    await click(first.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await verify(first.page,name,second.fingerprint);
    await verify(second.page,name,first.fingerprint);
    await click(first.page,'Apply verified membership','Verified membership applied');
    await click(second.page,'Check for messages','Messages checked');
    await second.page.getByLabel('Message',{exact:true}).fill('Hello from my approved second browser');
    await click(second.page,'Send encrypted message','Message accepted by server');
    await click(first.page,'Check for messages','Messages checked');
    await expect(first.page.locator('#messages')).toContainText('Hello from my approved second browser');
    await first.page.getByLabel('Device to revoke').selectOption(id);
    first.page.once('dialog',dialog => dialog.dismiss());
    await click(first.page,'Revoke device','Revocation cancelled');
    await expect(first.page.locator('#account-devices li').filter({hasText:id})).toContainText('approved');
    await first.page.route('**/api/messaging/devices/*',async route => {
      if (route.request().method() !== 'DELETE') return route.continue();
      expect((await route.fetch()).status()).toBe(200);
      await route.abort('failed');
    },{times:1});
    first.page.once('dialog',dialog => dialog.accept());
    await first.page.getByRole('button',{name:'Revoke device',exact:true}).click();
    await expect(first.page.getByRole('status')).not.toHaveText('Working…');
    await expect(first.page.locator('#account-devices li').filter({hasText:id})).toContainText('revoked');
    await expect(first.page.locator('#send')).toBeDisabled();
    await second.page.clock.install();
    await second.page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await second.page.clock.runFor(10_000);
    await expect(second.page.locator('#room-state')).toContainText('device was revoked');
    await expect(second.page.locator('#device-recovery')).toContainText('accessible approved browser');
    await expect(first.page.locator('#device-recovery')).toContainText('only approved device');
    await expect(second.page.locator('#poll-state')).toContainText('checks stopped');
    await expect(second.page.locator('#send')).toBeDisabled();
    await expect(second.page.locator('#sync')).toBeDisabled();
    await expect(second.page.locator('#messages')).toContainText('Hello from my approved second browser');
    await click(first.page,'Apply verified membership','Verified membership applied');
    await first.page.getByLabel('Message',{exact:true}).fill('After device revocation');
    await click(first.page,'Send encrypted message','Message accepted by server');
    await second.page.reload();
    let enrollments = 0;
    second.page.on('request',request => { if (request.method() === 'POST' && request.url().endsWith('/api/messaging/devices')) enrollments++; });
    await unlock(second.page,name);
    expect(enrollments).toBe(0);
    await expect(second.page.locator('#signed-in')).toContainText('revoked');
    await expect(second.page.locator('#messages')).not.toContainText('After device revocation');
    await expect(second.page.locator('#messages')).toContainText('Hello from my approved second browser');
    // Revoking the last approved device leaves the account's bootstrap tombstone.
    const current = await first.page.locator('#revoke-device option').filter({hasText:'This browser'}).getAttribute('value');
    if (!current) throw new Error('Missing current browser option');
    await first.page.getByLabel('Device to revoke').selectOption(current);
    first.page.once('dialog',async dialog => {
      expect(dialog.message()).toContain('current browser');
      expect(dialog.message()).toContain('identity reset requires fresh suite authentication');
      await dialog.accept();
    });
    await click(first.page,'Revoke device','Device revoked');
    await expect(first.page.locator('#room-state')).toContainText('device was revoked');
    await expect(first.page.locator('#device-recovery')).toContainText('No approved devices remain');
    const replacement = await start(browser,name);
    try {
      await expect(replacement.page.locator('#signed-in')).toContainText('pending');
      await expect(replacement.page.locator('#room-state')).toContainText('needs approval');
      await expect(replacement.page.locator('#device-recovery')).toContainText('pending replacement can request an identity reset');
      await expect(replacement.page.locator('#approve-own')).toBeDisabled();
    } finally { await replacement.context.close(); }

  } finally { await first.context.close(); await second.context.close(); }
});

test('owner removal confirms, reconciles lost replies and requires rekey before sending',async ({browser}) => {
  const ownerName = 'remove-owner-' + crypto.randomUUID().slice(0,8);
  const memberName = 'remove-member-' + crypto.randomUUID().slice(0,8);
  const owner = await start(browser,ownerName);
  const member = await start(browser,memberName);
  try {
    await owner.page.getByLabel('New room name').fill('Removal room');
    await click(owner.page,'Create room','Room created');
    await click(owner.page,'Apply verified membership','Verified membership applied');
    const invite = async () => {
      await owner.page.getByLabel("Teammate's account ID").fill(memberName);
      await click(owner.page,'Invite teammate','Invitation sent');
      await click(owner.page,'Refresh rooms and devices','Rooms and devices refreshed');
    };
    await invite();
    await owner.page.getByLabel('Member to remove').selectOption(memberName);
    owner.page.once('dialog',dialog => dialog.dismiss());
    await click(owner.page,'Remove member','Removal cancelled');
    await expect(owner.page.locator('#members')).toContainText(memberName);
    // Revoke a pending invitation: no group key change is needed yet.
    owner.page.once('dialog',dialog => dialog.accept());
    await click(owner.page,'Remove member','Member removed');
    await expect(owner.page.locator('#members')).not.toContainText(memberName);
    await expect(owner.page.locator('#send')).toBeEnabled();
    await invite();
    await click(member.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await click(member.page,'Accept Removal room','Room selected');
    await click(member.page,'Prepare to join','Ready to join');
    await click(owner.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await verify(owner.page,memberName,member.fingerprint);
    await verify(member.page,ownerName,owner.fingerprint);
    await click(owner.page,'Apply verified membership','Verified membership applied');
    await click(member.page,'Check for messages','Messages checked');
    await expect(member.page.locator('#remove-form')).toBeHidden();
    await expect(owner.page.locator('#remove-member option')).toHaveCount(2);
    await owner.page.getByLabel('Message',{exact:true}).fill('Earlier local history');
    await click(owner.page,'Send encrypted message','Message accepted by server');
    await click(member.page,'Check for messages','Messages checked');
    await owner.page.getByLabel('Member to remove').selectOption(memberName);
    await owner.page.route('**/members/*',async route => {
      expect((await route.fetch()).status()).toBe(200);
      await route.abort('failed');
    },{times:1});
    owner.page.once('dialog',dialog => dialog.accept());
    await owner.page.getByRole('button',{name:'Remove member',exact:true}).click();
    await expect(owner.page.getByRole('status')).not.toHaveText('Working…');
    await expect(owner.page.locator('#members')).not.toContainText(memberName);
    await expect(owner.page.locator('#room-state')).toContainText('Membership changed');
    await expect(owner.page.locator('#send')).toBeDisabled();
    await click(member.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await expect(member.page.locator('#room-state')).toContainText('access was removed');
    await expect(member.page.locator('#messages')).toContainText('Earlier local history');
    await expect(member.page.locator('#send')).toBeDisabled();
    await click(owner.page,'Apply verified membership','Verified membership applied');
    await owner.page.getByLabel('Message',{exact:true}).fill('While removed');
    await click(owner.page,'Send encrypted message','Message accepted by server');
    await click(member.page,'Check for messages','HTTP 404');
    await expect(member.page.locator('#messages')).not.toContainText('While removed');
    await invite();
    await click(member.page,'Refresh rooms and devices','Rooms and devices refreshed');
    await click(member.page,'Accept reinvitation','Reinvitation accepted');
    await click(owner.page,'Apply verified membership','Verified membership applied');
    await click(member.page,'Check for messages','Messages checked');
    await member.page.getByLabel('Message',{exact:true}).fill('Returned after removal');
    await click(member.page,'Send encrypted message','Message accepted by server');
    await click(owner.page,'Check for messages','Messages checked');
    await expect(owner.page.locator('#messages')).toContainText('Returned after removal');
    await expect(member.page.locator('#messages')).not.toContainText('While removed');
  } finally { await owner.context.close(); await member.context.close(); }
});

test('room switching preserves separate encrypted histories and unresolved sends',async ({browser}) => {
  const aliceName = 'rooms-a-' + crypto.randomUUID().slice(0,8);
  const bobName = 'rooms-b-' + crypto.randomUUID().slice(0,8);
  const alice = await start(browser,aliceName), bob = await start(browser,bobName);
  try {
    for (const room of ['First room','Second room']) {
      await alice.page.getByLabel('New room name').fill(room);
      if (room === 'First room') {
        await alice.page.route('**/api/messaging/rooms',async route => {
          expect((await route.fetch()).status()).toBe(201);
          await route.abort('failed');
        },{times:1});
        await alice.page.getByRole('button',{name:'Create room',exact:true}).click();
        await expect(alice.page.getByRole('status')).not.toHaveText('Working…');
        await click(alice.page,'Refresh rooms and devices','Rooms and devices refreshed');
        await click(alice.page,'Open First room','Room selected');
      } else await click(alice.page,'Create room','Room created');
      await click(alice.page,'Apply verified membership','Verified membership applied');
      await alice.page.getByLabel("Teammate's account ID").fill(bobName);
      await click(alice.page,'Invite teammate','Invitation sent');
      await click(bob.page,'Refresh rooms and devices','Rooms and devices refreshed');
      await click(bob.page,'Accept ' + room,'Room selected');
      await click(bob.page,'Prepare to join','Ready to join');
      await click(alice.page,'Refresh rooms and devices','Rooms and devices refreshed');
      if (room === 'First room') {
        await verify(alice.page,bobName,bob.fingerprint);
        await verify(bob.page,aliceName,alice.fingerprint);
      }
      await click(alice.page,'Apply verified membership','Verified membership applied');
      await click(bob.page,'Check for messages','Messages checked');
      await alice.page.getByLabel('Message',{exact:true}).fill('Message in ' + room);
      if (room === 'First room') {
        await alice.page.route('**/api/messaging/rooms/*/events',async route => {
          if (route.request().method() !== 'POST') return route.continue();
          expect((await route.fetch()).status()).toBe(200);
          await route.abort('failed');
        },{times:1});
        await alice.page.getByRole('button',{name:'Send encrypted message',exact:true}).click();
        await expect(alice.page.locator('#pending-text')).toContainText('Message in First room');
      } else {
        await click(alice.page,'Send encrypted message','Message accepted');
        await click(bob.page,'Check for messages','Messages checked');
        await expect(bob.page.locator('#messages')).toContainText('Message in Second room');
        await expect(bob.page.locator('#messages')).not.toContainText('Message in First room');
      }
    }
    await alice.page.reload();
    await unlock(alice.page,aliceName);
    await expect(alice.page.locator('#room-title')).toHaveText('Second room');
    await expect(alice.page.locator('#messages')).toContainText('Message in Second room');
    await alice.page.getByLabel('Message',{exact:true}).fill('Keep this draft in its room');
    alice.page.once('dialog',dialog => dialog.dismiss());
    await click(alice.page,'Open First room','Room change cancelled');
    await expect(alice.page.getByLabel('Message',{exact:true})).toHaveValue('Keep this draft in its room');
    alice.page.once('dialog',dialog => dialog.accept());
    await click(alice.page,'Open First room','Room selected');
    await expect(alice.page.getByLabel('Message',{exact:true})).toHaveValue('');
    await expect(alice.page.locator('#pending-text')).toContainText('Message in First room');
    await click(alice.page,'Retry pending delivery','Pending delivery confirmed');
    await expect(alice.page.locator('#messages')).toContainText('Message in First room');
    await expect(alice.page.locator('#messages')).not.toContainText('Message in Second room');
    await click(bob.page,'Open First room','Room selected');
    await click(bob.page,'Check for messages','Messages checked');
    await expect(bob.page.locator('#messages')).toContainText('Message in First room');
    const entries = await alice.page.evaluate(async () => {
      const db = await new Promise<IDBDatabase>((resolve,reject) => {
        const request = indexedDB.open('kymessages-mls-proof-v1',1);
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
      try {
        return await new Promise<string[]>((resolve,reject) => {
          const tx = db.transaction('vault','readonly');
          const request = tx.objectStore('vault').getAllKeys();
          tx.oncomplete = () => resolve(request.result.map(String));
          tx.onabort = () => reject(tx.error);
        });
      } finally {db.close();}
    });
    expect(entries).toHaveLength(2);
    expect(entries).toContain('device');
    expect(entries.some(key => key.startsWith('room:'))).toBe(true);
  } finally { await alice.context.close(); await bob.context.close(); }
});

test('Markdown is local, escapes HTML and never loads sender images',async ({browser}) => {
  const user = 'markdown-' + crypto.randomUUID().slice(0,8);
  const alice = await start(browser,user);
  const external: string[] = [];
  alice.page.on('request',request => { if (request.url().includes('privacy-test.invalid')) external.push(request.url()); });
  try {
    await alice.page.getByLabel('New room name').fill('Formatting');
    await click(alice.page,'Create room','Room created');
    await click(alice.page,'Apply verified membership','Verified membership applied');
    const message = [
      '**Strong** and *emphasis* with `inline code`',
      '- first item\n- second item',
      '> quoted text',
      '```html\n<script>alert("code is text")</script>\n' + 'x'.repeat(200) + '\n```',
      '[Documentation](https://example.com/docs?a=1&b=2)',
      '![pixel](https://privacy-test.invalid/pixel)',
      '<img src="https://privacy-test.invalid/raw" onerror="alert(1)"><svg onload="alert(1)"></svg><script>alert(1)</script>',
      '[run](javascript:alert(1)) [encoded](&#x6a;avascript:alert(1)) [local](/api/auth/logout) [data](data:text/html;base64,PHNjcmlwdD4=)',
    ].join('\n\n');
    await alice.page.getByLabel('Message',{exact:true}).fill(message);
    await click(alice.page,'Send encrypted message','Message accepted');
    for (const reload of [false,true]) {
      if (reload) { await alice.page.reload(); await unlock(alice.page,user); }
      const body = alice.page.locator('#messages .message-body');
      await expect(body.locator('strong')).toHaveText('Strong');
      await expect(body.locator('em')).toHaveText('emphasis');
      await expect(body.locator('li')).toHaveCount(2);
      await expect(body.locator('blockquote')).toContainText('quoted text');
      await expect(body.locator('pre code')).toContainText('<script>alert("code is text")</script>');
      await expect(body.locator('img,svg,script,iframe,form,style')).toHaveCount(0);
      await expect(body).toContainText('<img src=');
      const link = body.getByRole('link',{name:'Documentation',exact:true});
      await expect(link).toHaveAttribute('rel','noopener noreferrer');
      await expect(link).toHaveAttribute('target','_blank');
      expect(await body.locator('a').evaluateAll(links => links.every(link => link instanceof HTMLAnchorElement && ['http:','https:'].includes(link.protocol)))).toBe(true);
      expect(external).toEqual([]);
    }
    await alice.page.setViewportSize({width:360,height:780});
    expect(await alice.page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  } finally {await alice.context.close();}
});
