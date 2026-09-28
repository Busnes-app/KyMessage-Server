import { test } from '@playwright/test';

test('native Ed25519 key generation succeeds repeatedly without retries',async ({page}) => {
  // Isolate the browser primitive from MLS, persistence and application code.
  // Linux WebKit has intermittently thrown OperationError at this call.
  await page.route('**/native-crypto-check',route => route.fulfill({contentType:'text/html',body:'<!doctype html><title>Native crypto check</title>'}));
  await page.goto('/native-crypto-check');
  await page.evaluate(async () => {
    for (let i = 0; i < 256; i++) {
      try { await crypto.subtle.generateKey('Ed25519',true,['sign','verify']); }
      catch (error) {
        const name = error instanceof Error ? error.name : 'unknown error';
        throw new Error(`Native Ed25519 generation ${i+1}/256 failed: ${name}`);
      }
    }
  });
});
