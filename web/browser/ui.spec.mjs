import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';

async function fits(page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}

test('production CSP, worker, themes, keyboard, dialog and responsive shell', async ({ page, context, browserName }, testInfo) => {
  const violations = [];
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
  await page.getByPlaceholder('admin', { exact: true }).fill('admin');
  // The server allows 10 attempts per account per 15 minutes; setup plus one login per
  // project leaves room for only two wrong passwords, one per theme, width and browser.
  if (['dark-390', 'firefox-light-1280'].includes(testInfo.project.name)) {
    await page.locator('input[type=password]').fill('IncorrectPassword123!');
    await page.getByRole('button', { name: 'Sign In', exact: true }).click();
    await expect(page.locator('form').locator('..')).toContainText(/invalid|incorrect|failed/i);
    await fits(page);
  }
  await page.locator('input[type=password]').fill('BrowserUpdated456!');
  await page.getByRole('button', { name: 'Sign In', exact: true }).click();
  const nav = page.getByRole('navigation', { name: 'Primary' });
  await expect(nav).toBeVisible();
  await expect(page.getByRole('heading', { name: /Welcome/ })).toBeVisible();
  const usage = page.getByRole('region', { name: 'Messaging storage' });
  await expect(usage.getByRole('button', { name: 'Refresh storage usage' })).toBeEnabled();
  await usage.getByText('Room limits and busiest rooms', { exact: true }).click();
  await expect(usage.getByText('No messaging rooms.', { exact: true })).toBeVisible();
  await expect(usage).toContainText('100,000 retained events');
  await usage.getByRole('button', { name: 'Refresh storage usage' }).click();
  await expect(usage.getByRole('button', { name: 'Refresh storage usage' })).toBeEnabled();
  await expect(usage.getByText('No messaging rooms.', { exact: true })).toBeVisible();
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('messaging-storage.png'), fullPage: true });
  await fits(page);
  const theme = page.getByLabel('Color theme');
  await theme.selectOption('paper');
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(248, 250, 252)');
  await theme.selectOption('busnes-dark');
  await page.reload();
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(24, 35, 38)');
  const other = await context.newPage();
  await other.goto('/');
  await other.getByLabel('Color theme').selectOption('busnes-light');
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(248, 246, 240)');
  await other.close();
  await theme.selectOption('system');
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(24, 35, 38)');
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(248, 246, 240)');
  await page.emulateMedia({ colorScheme: testInfo.project.use.colorScheme });
  await nav.getByRole('button').first().focus();
  await page.keyboard.press('Tab');
  await expect(nav.getByRole('button').nth(1)).toBeFocused();
  await expect(nav.getByRole('button').nth(1)).toHaveCSS('outline-style', 'solid');
  await nav.getByRole('button', { name: 'Settings & DB' }).click();
  await expect(nav.getByRole('button', { name: 'Settings & DB' })).toHaveAttribute('aria-current', 'page');
  await expect(page.getByRole('heading', { name: 'System Settings & Architecture' })).toBeVisible();
  await fits(page);
  const pair = page.getByRole('button', { name: 'Pair Device' });
  await pair.click();
  const dialog = page.getByRole('dialog', { name: 'Link Mobile Device' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Close modal' })).toBeFocused();
  // Native modal containment makes the rest of the document inert.
  await pair.evaluate(button => button.focus());
  await expect(dialog.getByRole('button', { name: 'Close modal' })).toBeFocused();
  await expect(dialog.getByText('Pairing PIN')).toHaveCount(0);
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('dialog.png'), fullPage: true });
  await page.keyboard.press('Escape');
  await expect(dialog).not.toBeVisible();
  await expect(pair).toBeFocused();
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
  await nav.getByRole('button', { name: 'My account' }).click();
  await expect(page.getByText('Encrypted chat is not available on this server yet.')).toBeVisible();
  await expect(page.getByText('Messaging needs a KySignOn account.')).toBeVisible();
  // Playwright's Chromium is not Chrome or Edge, so it is feature-complete but unverified.
  await expect(page.getByText(browserName === 'firefox'
    ? /This browser can run KyMessages chat\./
    : /This browser has every feature chat needs but has not been verified\./)).toBeVisible();
  await fits(page);
  expect(violations).toEqual([]);
  await context.setOffline(true);
  const offline = await page.reload();
  expect(offline.ok()).toBe(true);
  expect(await offline.text()).toContain('id="root"');
  await context.setOffline(false);
});
