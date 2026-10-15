/* Service worker: funcionamento offline (cache do "app shell"). */
const VERSAO = "calc-energia-pt-v14";
const FICHEIROS = [
  "./", "index.html", "style.css", "app.js", "engine.js",
  "manifest.webmanifest",
  "icon.svg", "icon-192.png", "icon-512.png",
];

self.addEventListener("install", (e) => {
  e.waitUntil(
    caches.open(VERSAO).then((c) =>
      // addAll falharia se um ficheiro faltasse (ex.: ficheiro em falta)
      Promise.all(FICHEIROS.map((f) => c.add(f).catch(() => null)))
    ).then(() => self.skipWaiting())
  );
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys()
      .then((ks) => Promise.all(ks.filter((k) => k !== VERSAO).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

// Stale-while-revalidate: responde da cache e atualiza em segundo plano.
self.addEventListener("fetch", (e) => {
  const req = e.request;
  const u = new URL(req.url);
  if (req.method !== "GET" || u.origin !== location.origin || u.pathname.startsWith("/api/")) return;
  e.respondWith(
    caches.open(VERSAO).then(async (cache) => {
      const emCache = await cache.match(req, { ignoreSearch: true });
      const rede = fetch(req).then((r) => {
        if (r && r.ok) cache.put(req, r.clone());
        return r;
      }).catch(() => null);
      return emCache || (await rede) || (req.mode === "navigate" ? cache.match("index.html") : Response.error());
    })
  );
});
