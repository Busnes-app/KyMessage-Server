import { defineConfig } from '@playwright/test';
import delivery from './playwright.delivery.config.mjs';

export default defineConfig({
  ...delivery,
  testMatch: '**/oidc.spec.ts',
  webServer: [
    { command: 'cd .. && MLS_PROOF_OIDC=1 go run -buildvcs=false -tags=mlsproof ./mls-proof/server', url: 'http://127.0.0.1:4179/proof-fixture/health', reuseExistingServer: false },
    delivery.webServer[1],
  ],
});
