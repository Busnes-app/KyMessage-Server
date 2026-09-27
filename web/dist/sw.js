const CACHE_NAME = 'ky-base-pwa-busnes-v3';
const ASSETS_TO_CACHE = [
  '/',
  '/index.html',
  '/manifest.json',
];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => {
      return cache.addAll(ASSETS_TO_CACHE);
    })
  );
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) => {
      return Promise.all(
        keys.map((key) => {
          if (key !== CACHE_NAME) {
            return caches.delete(key);
          }
        })
      );
    })
  );
  self.clients.claim();
});

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  if (event.request.method !== 'GET' || url.origin !== self.location.origin) return;
  const shell = url.pathname === '/' || url.pathname === '/index.html';
  // Allow public shell/assets only, never API, SCIM, SSO or future dynamic routes.
  if (!shell && url.pathname !== '/manifest.json' && !url.pathname.startsWith('/assets/')) return;

  event.respondWith((async () => {
    const cache = await caches.open(CACHE_NAME);
    // HTML must refresh online so a deploy can advance its hashed asset URLs.
    if (!shell) {
      const cached = await cache.match(event.request);
      if (cached) return cached;
    }
    let response;
    try {
      response = await fetch(event.request);
    } catch (error) {
      const cached = await cache.match(event.request);
      if (cached) return cached;
      throw error;
    }
    if (response.status === 200 && response.type === 'basic' && !response.redirected) {
      // Storage/quota failures must not hide a successful network response.
      try { await cache.put(event.request, response.clone()); } catch {}
    }
    return response;
  })());
});
