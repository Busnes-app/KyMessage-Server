import { defineConfig } from 'vite';

export default defineConfig({
  build: { rollupOptions: { input: { manual: 'index.html', chat: 'chat.html' } } },
  preview: {
    proxy: process.env.MLS_PROOF_DELIVERY === '1' ? {
      '/api': 'http://127.0.0.1:4179',
      '/proof-fixture': 'http://127.0.0.1:4179',
    } : undefined,
    headers: {
      'Content-Security-Policy': "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'",
      'X-Content-Type-Options': 'nosniff',
      'Referrer-Policy': 'no-referrer',
      'Cache-Control': 'no-store',
    },
  },
});
