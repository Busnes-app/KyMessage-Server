import { defineConfig } from '@playwright/test';
import base from './playwright.config.mjs';

export default defineConfig({
  ...base,
  testIgnore: [],
  testMatch: ['**/delivery.spec.ts', '**/chat.spec.ts'],
  webServer: [
    { command: 'cd .. && go run -buildvcs=false -tags=mlsproof ./mls-proof/server', url: 'http://127.0.0.1:4179/proof-fixture/health', reuseExistingServer: false },
    { command: 'MLS_PROOF_DELIVERY=1 npm run preview', url: 'http://127.0.0.1:4178', reuseExistingServer: false },
  ],
});
