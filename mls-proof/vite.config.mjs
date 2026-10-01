import { defineConfig } from 'vite';

export default defineConfig({
  build: { rollupOptions: { input: { manual: 'index.html', chat: 'chat.html' } } },
  // Dev server: this directory, the shared chat core and the one shared stylesheet.
  // Not '..', which would serve the repository's data/ and backups/.
  server: { fs: { allow: ['.', '../chat-core/src', '../web/src/ky-ui/tokens.css'] } },
  preview: {
    proxy: process.env.MLS_PROOF_DELIVERY === '1' ? {
      '/api': {target:'http://127.0.0.1:4179',ws:true},
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
