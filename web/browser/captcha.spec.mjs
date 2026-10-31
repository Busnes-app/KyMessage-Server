import { test, expect } from '@playwright/test';

// The server spends a proof-of-work token before it checks the password, so the page must solve
// one challenge per attempt: never one per render (a loop), never reuse a spent one.
test('proof-of-work sign-in: one challenge while idle, a fresh one after a wrong password', async ({ page }) => {
  let challenges = 0;
  page.on('request', request => {
    if (new URL(request.url()).pathname === '/api/auth/pow-challenge') challenges++;
  });
  await page.goto('/');
  await expect(page.getByText('Browser check passed')).toBeVisible({ timeout: 20000 });
  await page.waitForTimeout(3000);
  expect(challenges).toBe(1);

  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password').fill('IncorrectPassword123!');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Invalid credentials');
  await expect(page.getByText('Browser check passed')).toBeVisible({ timeout: 20000 });
  await page.waitForTimeout(3000);
  expect(challenges).toBe(2);

  await page.getByLabel('Password').fill('BrowserInitial123!');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  // The bootstrap password must be replaced first, so a working sign-in lands here.
  await expect(page.getByRole('heading', { name: 'Change your password' })).toBeVisible();
});
