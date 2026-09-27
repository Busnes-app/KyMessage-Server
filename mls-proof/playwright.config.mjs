import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  timeout: 180_000,
  testIgnore: ['**/delivery.spec.ts', '**/chat.spec.ts', '**/oidc.spec.ts'],
  workers: 1,
  use: { baseURL: 'http://127.0.0.1:4178' },
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
    { name: 'firefox', use: { browserName: 'firefox' } },
    { name: 'webkit', use: { browserName: 'webkit' } },
  ],
  webServer: { command: 'npm run preview', url: 'http://127.0.0.1:4178', reuseExistingServer: false },
});
