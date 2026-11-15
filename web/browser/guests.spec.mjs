import { test, expect } from '@playwright/test';

// The meeting guest's page, signed out (ui.spec.mjs checks the Settings panel). Matrix is off
// on this server, so /api/meet/status is stubbed with the shape the Go server answers
// (internal/api/guest_handlers.go); everything else, CSP included, is the production server's.
async function fits(page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}

test('the guest page', async ({ page }, testInfo) => {
  const violations = [];
  page.on('console', (m) => { if (/Content Security Policy|violates.*directive/i.test(m.text())) violations.push(m.text()); });
  const sent = [];
  let state = { state: 'open', closes_at: '2026-11-15T10:00:00Z' };
  await page.route('**/api/meet/status', (r) => { sent.push(r.request().postData()); return r.fulfill({ json: state }); });

  // The token stays in the fragment: the page loads from /meet and sends it only in a POST body.
  const token = 'A'.repeat(43);
  const response = await page.goto(`/meet#${token}`);
  expect(response.url()).not.toContain(token);
  await expect(page.getByRole('heading', { name: 'Join the meeting' })).toBeVisible();
  await expect(page.getByLabel('Your name')).toBeVisible();
  await expect(page.getByRole('navigation')).toHaveCount(0);
  expect(sent).toEqual([JSON.stringify({ token })]);
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('meet-open.png'), fullPage: true });
  state = { state: 'waiting', opens_at: '2026-11-15T08:50:00Z', closes_at: '2026-11-15T10:00:00Z' };
  await page.reload();
  await expect(page.getByText(/This meeting opens at/)).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('meet-waiting.png'), fullPage: true });

  expect(violations).toEqual([]);
});
