(() => {
  "use strict";

  if (!("serviceWorker" in navigator)) return;
  const secureOrigin = location.protocol === "https:" || ["localhost", "127.0.0.1", "::1", "[::1]"].includes(location.hostname);
  if (!secureOrigin) return;

  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch(() => {});
  }, { once: true });
})();
