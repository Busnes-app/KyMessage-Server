import { test, expect } from '@playwright/test';

// Text contrast of an element on its nearest opaque background, after transitions finish.
const contrast = (locator) => locator.evaluate(async (el) => {
  while (el.getAnimations().length) await Promise.all(el.getAnimations().map((a) => a.finished.catch(() => {})));
  const g = new OffscreenCanvas(1, 1).getContext('2d');
  const rgba = (css) => { g.clearRect(0, 0, 1, 1); g.fillStyle = css; g.fillRect(0, 0, 1, 1); return [...g.getImageData(0, 0, 1, 1).data]; };
  const lum = (c) => c.slice(0, 3).map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }).reduce((t, v, i) => t + v * [0.2126, 0.7152, 0.0722][i], 0);
  let bg = null;
  for (let e = el; e && !bg; e = e.parentElement) { const c = rgba(getComputedStyle(e).backgroundColor); if (c[3] === 255) bg = c; }
  const [hi, lo] = [lum(rgba(getComputedStyle(el).color)), lum(bg ?? [255, 255, 255])].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
});

test('Appearance: native radio cards, named palettes and the return to Busnes', async ({ browser }, testInfo) => {
  const { viewport, colorScheme } = testInfo.project.use;
  const context = await browser.newContext({ viewport, colorScheme });
  const page = await context.newPage();
  const violations = [];
  page.on('console', (m) => { if (/Content Security Policy|violates.*directive/i.test(m.text())) violations.push(m.text()); });
  await page.goto('/');
  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password').fill('BrowserUpdated456!');
  await page.getByRole('button', { name: 'Sign In', exact: true }).click();
  const nav = page.getByRole('navigation', { name: 'Primary' });
  await nav.getByRole('button', { name: 'Appearance', exact: true }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Appearance' })).toBeVisible();
  const system = page.getByRole('radio', { name: /^Match system/ });
  await expect(system).toBeChecked();

  // Arrow keys move the native group's selection, which applies and saves straight away.
  await system.focus();
  await page.keyboard.press('ArrowDown');
  const light = page.getByRole('radio', { name: /^Busnes Light/ });
  await expect(light).toBeChecked();
  await expect(light).toBeFocused();
  await expect(page.locator('.ky-appearance-option', { has: light })).toHaveCSS('outline-style', 'solid');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);

  // A light suite palette: idle and hovered navigation rows stay readable.
  await page.getByRole('radio', { name: /^Sky/ }).check();
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(223, 241, 255)');
  for (const name of ['Overview', 'Users']) {
    const row = nav.getByRole('button', { name, exact: true });
    await page.mouse.move(0, 0);
    const idle = await contrast(row);
    await row.hover();
    const hover = await contrast(row);
    testInfo.annotations.push({ type: `Sky ${name}`, description: `idle ${idle.toFixed(2)}, hover ${hover.toFixed(2)}` });
    expect(idle).toBeGreaterThanOrEqual(4.5);
    expect(hover).toBeGreaterThanOrEqual(4.5);
  }
  await page.reload();
  await nav.getByRole('button', { name: 'Appearance', exact: true }).click();
  await expect(page.getByRole('radio', { name: /^Sky/ })).toBeChecked();

  // Back to Busnes Dark: the inline palette and colour scheme give way to the shared tokens.
  await page.getByRole('radio', { name: /^Busnes Dark/ }).check();
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(24, 35, 38)');
  expect(await page.evaluate(() => [document.documentElement.style.getPropertyValue('--bg'), getComputedStyle(document.documentElement).colorScheme])).toEqual(['', 'dark']);

  // Match system follows the device again.
  await system.check();
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(248, 246, 240)');
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(24, 35, 38)');
  expect(violations).toEqual([]);
  await context.close();
});
