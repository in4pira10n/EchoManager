(function () {
  if (window.EchoManagerI18n) return;
  const DEFAULT_LOCALE = "zh-CN";
  const STORAGE_KEY = "echo-manager-locale-v1";
  const state = { catalog: {}, fallback: {}, locales: [], locale: DEFAULT_LOCALE };

  function normalizeLocale(value) {
    const normalized = String(value || "").trim().replace(/_/g, "-");
    if (!normalized || !/^[A-Za-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$/.test(normalized)) return "";
    if (/^zh(?:-|$)/i.test(normalized)) return "zh-CN";
    if (/^en(?:-|$)/i.test(normalized)) return "en-US";
    return normalized;
  }

  function readPath(catalog, path) {
    return String(path || "").split(".").reduce((value, key) => value?.[key], catalog);
  }

  function interpolate(value, variables) {
    if (typeof value !== "string") return value;
    return value.replace(/\{([A-Za-z0-9_.-]+)\}/g, (match, key) => {
      const replacement = variables && Object.prototype.hasOwnProperty.call(variables, key)
        ? variables[key]
        : match;
      return replacement == null ? "" : String(replacement);
    });
  }

  function mergeCatalog(base, additions) {
    if (!additions || typeof additions !== "object") return base;
    const merged = Array.isArray(base) ? [...base] : { ...base };
    for (const [key, value] of Object.entries(additions)) {
      if (value && typeof value === "object" && !Array.isArray(value)) {
        merged[key] = mergeCatalog(merged[key] || {}, value);
      } else if (value !== undefined && value !== null) {
        merged[key] = value;
      }
    }
    return merged;
  }

  function bestBrowserLocale(locales) {
    const candidates = navigator.languages || [navigator.language];
    for (const candidate of candidates) {
      const normalized = normalizeLocale(candidate);
      const exact = locales.find((item) => item.code.toLowerCase() === normalized.toLowerCase());
      if (exact) return exact.code;
      const language = normalized.split("-")[0].toLowerCase();
      const regional = locales.find((item) => item.code.split("-")[0].toLowerCase() === language);
      if (regional) return regional.code;
    }
    return DEFAULT_LOCALE;
  }

  async function fetchJSON(path) {
    const response = await fetch(path, { cache: "no-cache" });
    if (!response.ok) throw new Error(`Unable to load locale resource: ${path}`);
    return response.json();
  }

  function translate(path, variables) {
    const value = readPath(state.catalog, path);
    const fallback = readPath(state.fallback, path);
    return interpolate(typeof value === "string" ? value : fallback ?? path, variables);
  }

  function apply(root = document) {
    root.querySelectorAll("[data-i18n]").forEach((element) => {
      const value = translate(element.dataset.i18n);
      if (typeof value === "string") element.textContent = value;
    });
    root.querySelectorAll("[data-i18n-aria]").forEach((element) => {
      const value = translate(element.dataset.i18nAria);
      if (typeof value === "string") element.setAttribute("aria-label", value);
    });
    root.querySelectorAll("[data-i18n-title]").forEach((element) => {
      const value = translate(element.dataset.i18nTitle);
      if (typeof value === "string") element.setAttribute("title", value);
    });
    root.querySelectorAll("[data-i18n-placeholder]").forEach((element) => {
      const value = translate(element.dataset.i18nPlaceholder);
      if (typeof value === "string") element.setAttribute("placeholder", value);
    });
    root.querySelectorAll("[data-i18n-value]").forEach((element) => {
      const value = translate(element.dataset.i18nValue);
      if (typeof value === "string") element.value = value;
    });
    document.documentElement.lang = state.locale;
  }

  async function load() {
    const manifest = await fetchJSON("/static/strings/locales.json").catch(() => ({ locales: [] }));
    state.locales = Array.isArray(manifest.locales) && manifest.locales.length
      ? manifest.locales
      : [{ code: DEFAULT_LOCALE }];
    state.fallback = await fetchJSON(`/static/strings/${DEFAULT_LOCALE}.json`);
    let requested = "";
    try { requested = normalizeLocale(localStorage.getItem(STORAGE_KEY)); } catch {}
    const selected = state.locales.find((item) => item.code === requested)?.code || bestBrowserLocale(state.locales);
    state.locale = selected || DEFAULT_LOCALE;
    const selectedCatalog = state.locale === DEFAULT_LOCALE
      ? state.fallback
      : await fetchJSON(`/static/strings/${encodeURIComponent(state.locale)}.json`).catch(() => ({}));
    state.catalog = mergeCatalog(state.fallback, selectedCatalog);
    if (document.readyState === "loading") {
      await new Promise((resolve) => document.addEventListener("DOMContentLoaded", resolve, { once: true }));
    }
    apply();
    return api;
  }

  const api = {
    ready: null,
    t: translate,
    apply,
    get locale() { return state.locale; },
    get locales() { return [...state.locales]; },
    async setLocale(locale) {
      const normalized = normalizeLocale(locale);
      if (!state.locales.some((item) => item.code === normalized)) return false;
      try { localStorage.setItem(STORAGE_KEY, normalized); } catch {}
      window.location.reload();
      return true;
    },
  };
  api.ready = load().catch((error) => {
    console.error(error);
    state.fallback = {};
    state.catalog = {};
    return api;
  });
  window.EchoManagerI18n = api;
})();
