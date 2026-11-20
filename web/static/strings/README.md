# UI text resources

`zh-CN.json` is the source catalog for EchoManager text. This MVP only includes
Chinese; it does not introduce an English translation or a language picker.
Keep new labels, hints, notifications, validation messages, and empty states in
this catalog. Use stable keys and reuse an existing key when its meaning matches.
Do not edit EchoKit to localize EchoManager.

## JavaScript

Wait for the catalog before rendering dynamic content:

```js
await window.EchoManagerI18n.ready;
const t = (key, variables) => window.EchoManagerI18n.t(key, variables);
showFeedback(t("instance.starting", { name: instance.name }), "info");
```

Use named placeholders such as `{name}` instead of assembling translated sentence
fragments. Assign text through `textContent`; catalog values are plain text.

## HTML

Load `/static/i18n.js` once. Static elements can declare resource keys:

```html
<span data-i18n="auth.username"></span>
<input data-i18n-placeholder="auth.username">
```

The runtime also supports `data-i18n-aria`, `data-i18n-title`, and
`data-i18n-value`. Call `EchoManagerI18n.apply(container)` after inserting marked
HTML dynamically. Preserve nested markup by placing text keys on child spans.

## Go and remote scripts

Server messages use `uiText("backend.auth.message001")` with the same embedded
catalog. Preserve Go formatting directives (`%s`, `%d`, `%v`, `%w`) when editing
messages. Existing numbered backend keys must not be renumbered.

Backend responses currently use Chinese regardless of browser locale. Adding
translated responses later requires request locale handling or structured error
codes that the frontend can translate.

Remote shell and PowerShell scripts live in `internal/server/resources`, not in
the translation catalog. They are executable resources; localized OS tokens used
for parsing command output are protocol details, not UI text.

## Adding a language later

Add a JSON catalog beside `zh-CN.json` and register it in `locales.json`. Keep the
same keys and placeholders. The runtime uses a supported saved preference
(`echo-manager-locale-v1` in localStorage), then browser language, then Chinese.
Missing translated entries fall back to Chinese. `setLocale(code)` saves a
supported preference and reloads the page.

Also update the initialization-page asset whitelist and service-worker cache
list when adding a catalog. Catalogs and the runtime are embedded in the Go
executable; rebuild after editing them.
