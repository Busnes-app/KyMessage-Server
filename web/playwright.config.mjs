import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './browser',
  testMatch: '*.spec.mjs',
  globalSetup: './browser/setup.mjs',
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  timeout: 45000,
  use: { baseURL: 'http://127.0.0.1:5391', trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  projects: ['light', 'dark'].flatMap(colorScheme => [390, 1280].map(width => ({
    name: `${colorScheme}-${width}`,
    use: { browserName: 'chromium', colorScheme, viewport: { width, height: 900 } },
  }))),
  webServer: {
    command: 'node browser/server.mjs',
    url: 'http://127.0.0.1:5391',
    reuseExistingServer: false,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10000 },
  },
});
