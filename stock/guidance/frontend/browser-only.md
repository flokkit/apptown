---
name: frontend/browser-only
tags: frontend,simplicity
---
The app runs entirely in the browser. There is no server of its own:
AppTown serves the built files and nothing else, so every feature works from
those files and from what the browser keeps.

- **State lives in the browser.** Small settings and lists go in
  `localStorage`; larger or structured data goes in IndexedDB. Reading saved
  data must survive a value from an older version, a damaged value, or
  storage being switched off: fall back to an empty state and keep the app
  usable. What a person saves stays on their device; say so where it
  matters (a note in the page, the README), and offer an export (a file
  download) when losing it would hurt.
- **The app's own files are its only network.** At run time the code loads
  only files the build produced or `public/` holds, from its own origin
  (relative paths or paths from `/`). No third-party scripts, fonts or
  styles from a CDN, no analytics, no calls to someone else's API: each one
  is a way for the app to break offline or leak what people do. Data from
  elsewhere is brought in when the app is built (guidance
  `data/large-data-files`), not fetched by each visitor.
- **No server, ever, in this project.** If a request needs one (accounts,
  data shared between people, sending email, a secret key), say so and
  stop: it belongs in a project with a server, not in a workaround here.
  An API key written into browser code is public.

**Working offline, when the app needs it.** An app people use away from a
connection (on a phone, travelling) installs a service worker so it opens
with no network after the first visit. Use `vite-plugin-pwa`: it generates
the worker and its list of files from the build, so the cached copy always
matches a release, and it writes the web app manifest so the app can be
added to a home screen. Precache the app itself (HTML, scripts, styles,
icons). Data files the app loads in pieces are cached as each piece is
first used (a cache-first runtime rule on their path), unless the app must
work fully offline from the start; then precache them too and keep their
total small. Use `registerType: 'autoUpdate'` so a new release replaces the
old copy on the next visit. Test offline in the preview: load the app, turn
the network off in the browser's developer tools, reload, and use it.
An app that is always used online does not need a service worker; do not
add one by habit, since a stale cached copy is harder to fix than no copy.
