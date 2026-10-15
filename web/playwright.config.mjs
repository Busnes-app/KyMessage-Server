import { defineConfig } from '@playwright/test';

const server = (port, captcha) => ({
  command: `node browser/server.mjs ${port} ${captcha}`,
  url: `http://127.0.0.1:${port}`,
  reuseExistingServer: false,
  gracefulShutdown: { signal: 'SIGTERM', timeout: 10000 },
});

export default defineConfig({
  testDir: './browser',
  testMatch: '*.spec.mjs',
  globalSetup: './browser/setup.mjs',
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  timeout: 45000,
  use: { baseURL: 'http://127.0.0.1:5391', trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  projects: [
    ...['light', 'dark'].flatMap(colorScheme => [390, 1280].map(width => ({
      name: `${colorScheme}-${width}`,
      testIgnore: 'captcha.spec.mjs',
      use: { browserName: 'chromium', colorScheme, viewport: { width, height: 900 } },
    }))),
    { name: 'firefox-light-1280', testIgnore: 'captcha.spec.mjs', use: { browserName: 'firefox', colorScheme: 'light', viewport: { width: 1280, height: 900 } } },
    { name: 'firefox-dark-390', testIgnore: 'captcha.spec.mjs', use: { browserName: 'firefox', colorScheme: 'dark', viewport: { width: 390, height: 900 } } },
    // Its own server with the shipped proof-of-work captcha; globalSetup never signs in there.
    { name: 'captcha-pow', testMatch: 'captcha.spec.mjs', use: { browserName: 'chromium', baseURL: 'http://127.0.0.1:5392' } },
  ],
  webServer: [server(5391, 'none'), server(5392, 'pow')],
});
