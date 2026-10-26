import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';

async function fits(page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}

// A nav row's surface once its transitions end: background composited over the sidebar, text
// contrast against it, and the rail.
async function navSurface(locator) {
  return locator.evaluate(async (el) => {
    // A cancelled transition rejects `finished`; wait until none is running.
    for (let i = 0; i < 10 && el.getAnimations().length; i++) await Promise.allSettled(el.getAnimations().map((a) => a.finished));
    const ctx = Object.assign(document.createElement('canvas'), { width: 1, height: 1 }).getContext('2d', { willReadFrequently: true });
    const rgba = (c) => { ctx.clearRect(0, 0, 1, 1); ctx.fillStyle = '#000'; ctx.fillStyle = c; ctx.fillRect(0, 0, 1, 1); return [...ctx.getImageData(0, 0, 1, 1).data]; };
    const lum = (c) => c.slice(0, 3).map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; })
      .reduce((sum, v, i) => sum + v * [0.2126, 0.7152, 0.0722][i], 0);
    const cs = getComputedStyle(el);
    const under = rgba(cs.getPropertyValue('--sidebar-start').trim() || '#fff');
    const own = rgba(cs.backgroundColor);
    const bg = own.slice(0, 3).map((v, i) => Math.round(v * own[3] / 255 + under[i] * (1 - own[3] / 255)));
    const [hi, lo] = [lum(rgba(cs.color)), lum(bg)].sort((a, b) => b - a);
    return { background: bg.join(','), ratio: (hi + 0.05) / (lo + 0.05), rail: cs.boxShadow };
  });
}

test('production CSP, worker, themes, keyboard and responsive shell', async ({ page, context }, testInfo) => {
  const violations = [];
  const pairingRequests = [];
  page.on('request', request => {
    if (new URL(request.url()).pathname.startsWith('/api/devices/pair/')) pairingRequests.push(request.url());
  });
  page.on('console', message => {
    if (/Content Security Policy|violates.*directive/i.test(message.text())) violations.push(message.text());
  });
  const response = await page.goto('/');
  expect(response.headers()['content-security-policy']).toContain("script-src 'self'");
  expect(response.headers()['content-security-policy']).not.toContain("script-src 'self' 'unsafe-inline'");
  await expect.poll(() => page.evaluate(async () => (await navigator.serviceWorker.getRegistration())?.active?.state)).toBe('activated');
  await page.evaluate(async () => {
    const key = (await caches.keys())[0];
    await (await caches.open(key)).put('/', new Response('<h1>Stale shell</h1>', {
      headers: { 'Content-Type': 'text/html' },
    }));
  });
  // Online navigation must refresh the shell rather than pinning a previous deploy.
  await page.reload();
  await page.getByLabel('Username').fill('admin');
  // The server allows 10 attempts per account per 15 minutes; setup plus one login per
  // project leaves room for only two wrong passwords, one per theme, width and browser.
  if (['dark-390', 'firefox-light-1280'].includes(testInfo.project.name)) {
    await page.getByLabel('Password').fill('IncorrectPassword123!');
    await page.getByRole('button', { name: 'Sign In', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('Invalid credentials');
    await fits(page);
  }
  await page.getByLabel('Password').fill('BrowserUpdated456!');
  await page.getByRole('button', { name: 'Sign In', exact: true }).click();
  const nav = page.getByRole('navigation', { name: 'Primary' });
  await expect(nav).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Overview', exact: true })).toBeVisible();
  await fits(page);
  // Themes are chosen on the Appearance page; the shell carries no theme select.
  await expect(page.getByLabel('Color theme')).toHaveCount(0);
  const appearance = (p) => p.getByRole('navigation', { name: 'Primary' }).getByRole('button', { name: 'Appearance', exact: true }).click();
  const theme = (p, name) => p.getByRole('radio', { name: new RegExp(`^${name}`) }).check();
  await appearance(page);
  await theme(page, 'Paper Clean');
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(248, 250, 252)');
  await theme(page, 'Busnes Dark');
  await page.reload();
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(24, 35, 38)');
  const other = await context.newPage();
  await other.goto('/');
  await appearance(other);
  await theme(other, 'Busnes Light');
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(248, 246, 240)');
  await other.close();
  await appearance(page);
  await theme(page, 'Match system');
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(24, 35, 38)');
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(248, 246, 240)');
  await nav.getByRole('button', { name: 'Overview', exact: true }).click();
  await page.emulateMedia({ colorScheme: testInfo.project.use.colorScheme });
  // Busnes Light or Dark (from the OS) at this width: hovering an idle nav row gives a neutral
  // surface with readable text (the global primary-button hover once filled it with the accent),
  // and hovering the selected row keeps its surface and rail.
  const overview = nav.getByRole('button', { name: 'Overview', exact: true });
  const users = nav.getByRole('button', { name: 'Users', exact: true });
  await expect(overview).toHaveAttribute('aria-current', 'page');
  await page.mouse.move(0, 0);
  const idleAtRest = await navSurface(users);
  const selectedAtRest = await navSurface(overview);
  await users.hover();
  const idleHover = await navSurface(users);
  expect(idleHover.background).not.toBe(idleAtRest.background);
  expect(idleHover.ratio).toBeGreaterThanOrEqual(4.5);
  await overview.hover();
  expect(await navSurface(overview)).toEqual(selectedAtRest);
  await page.mouse.move(0, 0);
  await nav.getByRole('button').first().focus();
  await page.keyboard.press('Tab');
  await expect(nav.getByRole('button').nth(1)).toBeFocused();
  await expect(nav.getByRole('button').nth(1)).toHaveCSS('outline-style', 'solid');
  await nav.getByRole('button', { name: 'Settings & DB' }).click();
  await expect(nav.getByRole('button', { name: 'Settings & DB' })).toHaveAttribute('aria-current', 'page');
  await expect(page.getByRole('heading', { name: 'System Settings & Architecture' })).toBeVisible();
  await fits(page);
  await expect(page.getByRole('button', { name: 'Pair Device' })).toHaveCount(0);
  expect(pairingRequests).toEqual([]);
  await page.evaluate(() => fetch('/browser-regression-uncached'));
  const cachedDynamic = await page.evaluate(async () => {
    const keys = await caches.keys();
    for (const key of keys) for (const req of await (await caches.open(key)).keys()) {
      if (/^\/(api|scim|saml|browser-regression-uncached)(\/|$)/.test(new URL(req.url).pathname)) return true;
    }
    return false;
  });
  expect(cachedDynamic).toBe(false);
  expect(violations).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath('settings.png'), fullPage: true });
  // Branding, by keyboard, with a 64-character name that must wrap rather than widen the shell.
  // Matrix is off here: Settings has no KyIdentity sync panel. The sign-in above is recent, so
  // there is no step-up prompt. The projects share one server: start from the defaults (a failed
  // earlier project may have left a name or logo) and undo every change below.
  const branding = page.getByRole('region', { name: 'Branding' });
  await expect(branding).toBeVisible();
  await expect(page.getByRole('region', { name: 'KyIdentity sync' })).toHaveCount(0);
  await expect(branding.getByLabel('Product name')).toBeVisible();
  const resetName = branding.getByRole('button', { name: /^Use default/ });
  if (await resetName.isEnabled()) {
    await resetName.click();
    await expect(branding.getByRole('status')).toContainText('Name reset to KyMessages');
  }
  const resetLogo = branding.getByRole('button', { name: 'Reset logo' });
  if (await resetLogo.isEnabled()) {
    await resetLogo.click();
    await expect(branding.getByRole('status')).toContainText('Logo reset');
  }
  const brandName = `Browser-${testInfo.project.name}-`.padEnd(64, 'x');
  await branding.getByLabel('Product name').fill('');
  await page.keyboard.type(brandName);
  await page.keyboard.press('Enter');
  await expect(branding.getByRole('status')).toContainText('Name saved');
  await expect(page.locator('.app-brand')).toContainText(brandName);
  await fits(page);
  const icon = await readFile(new URL('../public/app-icon-192.png', import.meta.url));
  await branding.getByLabel(/^Logo/).setInputFiles({ name: 'logo.png', mimeType: 'image/png', buffer: icon });
  await branding.getByRole('button', { name: 'Upload logo' }).click();
  await expect(branding.getByRole('status')).toContainText('Logo saved');
  await expect(branding.getByAltText('Current logo')).toHaveAttribute('src', /^\/app-icon\.png\?v=[0-9a-f]{64}$/);
  expect(await page.evaluate(async () => (await fetch('/app-icon.png')).headers.get('cache-control'))).toBe('no-cache');
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('branding.png'), fullPage: true });
  await branding.getByRole('button', { name: 'Reset logo' }).click();
  await expect(branding.getByAltText('Current logo')).toHaveAttribute('src', '/app-icon.png?v=default');
  await branding.getByRole('button', { name: /^Use default/ }).focus();
  await page.keyboard.press('Enter');
  await expect(branding.getByRole('status')).toContainText('Name reset to KyMessages');
  await expect(page.locator('.app-brand')).toHaveText('KyMessages');
  expect(violations).toEqual([]);
  const fixture = JSON.parse(await readFile(new URL('../../internal/backup/testdata/pairing-v050.json', import.meta.url), 'utf8'));
  const pinned = await page.evaluate(async publicKey => {
    const csrf = document.cookie.split('; ').find(cookie => cookie.startsWith('ky_csrf='))?.slice('ky_csrf='.length);
    const response = await fetch('/api/backup/pin-key', { method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf ?? '' },
      body: JSON.stringify({ public_key: publicKey, threshold: 2, total_shares: 3 }),
    });
    return response.status;
  }, fixture.PublicKey);
  expect(pinned).toBe(200);
  await nav.getByRole('button', { name: 'Backup & recovery' }).click();
  await page.getByRole('button', { name: 'Back up now', exact: true }).click();
  await expect(page.getByText(/Last recorded backup attempt: Succeeded/)).toBeVisible();
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('backup.png'), fullPage: true });
  // Console pages. Matrix is off here: Users says so, Health checks the app and its database
  // (with the network check moved from Settings), Audit lists the backup that just ran.
  await nav.getByRole('button', { name: 'Users', exact: true }).click();
  await expect(page.getByText('Chat (Matrix) is not set up on this server.')).toBeVisible();
  await fits(page);
  // Agents, then Rooms, Matrix off: the same notice; reached from Users by keyboard.
  await nav.getByRole('button', { name: 'Users', exact: true }).focus();
  await page.keyboard.press('Tab');
  await expect(nav.getByRole('button', { name: 'Agents', exact: true })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(nav.getByRole('button', { name: 'Agents', exact: true })).toHaveAttribute('aria-current', 'page');
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible();
  await expect(page.getByText('Chat (Matrix) is not set up on this server.')).toBeVisible();
  await fits(page);
  await page.keyboard.press('Tab');
  await expect(nav.getByRole('button', { name: 'Rooms', exact: true })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(nav.getByRole('button', { name: 'Rooms', exact: true })).toHaveAttribute('aria-current', 'page');
  await expect(page.getByRole('heading', { name: 'Rooms', level: 1 })).toBeVisible();
  await expect(page.getByText('Chat (Matrix) is not set up on this server.')).toBeVisible();
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('rooms.png'), fullPage: true });
  await nav.getByRole('button', { name: 'Health', exact: true }).click();
  const components = page.getByRole('region', { name: 'Components' });
  await expect(components.getByText('Up', { exact: true })).toHaveCount(2);
  await expect(page.getByRole('heading', { name: 'Network path' })).toBeVisible();
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('health.png'), fullPage: true });
  await nav.getByRole('button', { name: 'Health', exact: true }).focus();
  await page.keyboard.press('Tab');
  await expect(nav.getByRole('button', { name: 'Audit', exact: true })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(nav.getByRole('button', { name: 'Audit', exact: true })).toHaveAttribute('aria-current', 'page');
  // Under All the sign-in rows are listed, so their absence after filtering is the filter.
  await expect(page.getByRole('cell', { name: 'auth.login', exact: true }).first()).toBeVisible();
  await page.getByLabel('Kind').selectOption('backup');
  await expect(page.getByRole('cell', { name: 'admin.backup_run', exact: true }).first()).toBeVisible();
  await expect(page.getByRole('cell', { name: 'auth.login', exact: true })).toHaveCount(0);
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('audit.png'), fullPage: true });
  await nav.getByRole('button', { name: 'Overview', exact: true }).click();
  await expect(page.getByText(/^Last backup: Succeeded/)).toBeVisible();
  await expect(page.getByText(/not set up yet/)).toHaveCount(0);
  await fits(page);
  await expect(page.getByRole('button', { name: 'Pair Device' })).toHaveCount(0);
  expect(pairingRequests).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath('overview.png'), fullPage: true });
  expect(violations).toEqual([]);
  await context.setOffline(true);
  const offline = await page.reload();
  expect(offline.ok()).toBe(true);
  expect(await offline.text()).toContain('id="root"');
  await context.setOffline(false);
});
