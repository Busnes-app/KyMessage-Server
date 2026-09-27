import { test, expect } from '@playwright/test';

async function fits(page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}

test('production CSP, worker, themes, keyboard, dialog and responsive shell', async ({ page, context }, testInfo) => {
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
  await page.locator('input[type=password]').fill('IncorrectPassword123!');
  await page.getByRole('button', { name: 'Sign In', exact: true }).click();
  await expect(page.locator('form').locator('..')).toContainText(/invalid|incorrect|failed/i);
  await fits(page);
  await page.locator('input[type=password]').fill('BrowserUpdated456!');
  await page.getByRole('button', { name: 'Sign In', exact: true }).click();
  const nav = page.getByRole('navigation', { name: 'Primary' });
  await expect(nav).toBeVisible();
  await expect(page.getByRole('heading', { name: /Welcome/ })).toBeVisible();
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
  await page.keyboard.press('Tab');
  await expect(dialog.getByRole('button', { name: 'Copy PIN' })).toBeFocused();
  // Native modal containment makes the rest of the document inert.
  await pair.evaluate(button => button.focus());
  await expect(dialog.getByRole('button', { name: 'Copy PIN' })).toBeFocused();
  await page.keyboard.press('Shift+Tab');
  await expect(dialog.getByRole('button', { name: 'Close modal' })).toBeFocused();
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
  await context.setOffline(true);
  const offline = await page.reload();
  expect(offline.ok()).toBe(true);
  expect(await offline.text()).toContain('id="root"');
  await context.setOffline(false);
});
