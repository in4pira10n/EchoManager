const VERSION = "{{ASSET_VERSION}}";
const CACHE_NAME = `echo-manager-${VERSION}`;
const APP_SHELL = [
  "/offline.html",
  "/app.css",
  "/auth.css",
  "/setup.css",
  "/app.js",
  "/auth.js",
  "/setup.js",
  "/settings-config.js",
  "/pwa.js",
  "/manifest.webmanifest",
  "/static/assets/echomanager-icon.png",
  "/static/assets/apple-touch-icon.png",
  "/static/assets/favicon.ico",
  "/static/assets/favicon-32.png",
  "/static/assets/favicon-16.png",
  "/static/assets/pwa-icon-192.png"
];

self.addEventListener("install", event => {
  event.waitUntil(
    caches.open(CACHE_NAME)
      .then(cache => cache.addAll(APP_SHELL.map(url => new Request(url, { cache: "reload" }))))
      .then(() => self.skipWaiting())
  );
});

self.addEventListener("activate", event => {
  event.waitUntil(
    caches.keys()
      .then(keys => Promise.all(keys.filter(key => key.startsWith("echo-manager-") && key !== CACHE_NAME).map(key => caches.delete(key))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", event => {
  const request = event.request;
  const url = new URL(request.url);
  if (request.method !== "GET" || url.origin !== self.location.origin || url.pathname.startsWith("/api/")) return;

  if (request.mode === "navigate") {
    event.respondWith(fetch(request).catch(() => caches.match("/offline.html")));
    return;
  }

  event.respondWith(
    fetch(request).then(response => {
      if (response.ok) {
        const copy = response.clone();
        caches.open(CACHE_NAME).then(cache => cache.put(request, copy)).catch(() => {});
      }
      return response;
    }).catch(() => caches.match(request).then(cached => cached || Response.error()))
  );
});
