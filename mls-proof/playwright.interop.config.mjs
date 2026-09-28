import { defineConfig } from '@playwright/test';
import manual from './playwright.config.mjs';

export default defineConfig({
  ...manual,
  testMatch: '**/interop.spec.ts',
  testIgnore: [],
});
