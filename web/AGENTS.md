# Web

## Purpose
React 19 + TypeScript + Vite PWA frontend using the shared ky-ui theme catalog (Busnes light/dark defaults, the suite palettes and the base-family `patina`, `cyber`, `nord`, `paper`, `oled`), 90-second ephemeral QR device pairing modals, client-side WebCrypto PoW CAPTCHA, and administrative management panels.

## Ownership
Owns user interface components, service worker caching, PWA installation manifests, and frontend theme switching.

## Local Contracts
- Newly added suite palettes tint error/success text toward their ink for readable status text on light and dark surfaces; the five shipped base palettes retain their original status values. Browser regressions measure all six new light palettes against both page and panel surfaces. The filled `.btn-danger` takes the page colour (`--bg`) as its label under the same 15 suite palettes, because the tint makes the fill too dark for a dark `--button-text`. Busnes and the five base palettes keep `--button-text`. Base renders no `.btn-danger`, so `browser/danger-button.spec.mjs` adds a labelled diagnostic button to the real page and measures its rendered label at rest, hover and focus.
- Web themes default to the Busnes.app cream/light and charcoal/dark palettes with orange accents, following the OS until a browser-local choice is saved. Preserve existing named themes and saved choices.
- A signed-in user with `must_change_password` sees only password replacement and sign-out. Replacement uses `secureFetch`, returns to login after session revocation, and never exposes the normal navigation before completion.
- Strict TypeScript type safety without unused imports.
- The authenticated shell uses a persistent sidebar; the selected page is marked by a quiet surface and slim accent rail, with a horizontal overflow navigation on small screens that keeps the current row in view on every page change (`scrollIntoView` nearest; the header is sticky, so the page does not move; focus stays put). `browser/nav.spec.mjs` covers it.
- `src/theme.ts` adapts the shared catalog (`ky-ui/themes.js`): `Match system` plus every catalog theme, saved by catalog ID under `ky_theme`; older saved IDs (including base `patina`, distinct from suite `patina-ky`) resolve unchanged. It sets `data-theme` and, for named palettes, the colours inline through `applyThemeVariables`; Busnes clears them so the token aliases apply. Every theme control reads the one `useTheme()` store.
- The Appearance page (`src/pages/Appearance.tsx`) is the theme picker for every signed-in role: shared native radio cards (`ky-ui/appearance.css`, previews from `appearance-themes.css`, imported after product CSS). The signed-in shell has no other theme selector; only the sign-in page keeps `ThemeSwitcher`, over the same store. The page uses the shared `ky-appearance-description` copy.
- Authenticated state-changing requests use `secureFetch` so the `ky_csrf` cookie is mirrored into `X-CSRF-Token`.
- Register the service worker from the production JS bundle; keep `script-src 'self'` intact. Pairing uses a native modal dialog for focus containment, Escape and focus restoration. It shows only the QR code (no typed PIN) and treats poll status `consumed` as linked.
- Worker caching is limited to the same-origin public shell, manifest and assets. HTML is network-first with offline fallback so deployments refresh; dynamic/auth routes stay uncached.
- `Backup.tsx` warns for as long as `database_driver` from `/api/backup/status` is not `sqlite`: only the SQLite path can snapshot a database into a capsule, so a Postgres deployment makes no capsules at all.

## Verification
- Browser setup: build the frontend, run `go build -o .browser/server ./cmd/server` at the repo root, then `cd web && npx playwright install chromium && npm run test:browser`. CI also installs browser OS dependencies.
- `make test-web` or `cd web && npm ci && npm test`, then `npm run build` (vitest with jsdom; `src/pages/Backup.test.tsx` renders the recovery screen against a stubbed status route). Commit `web/dist` after a build; CI diffs it.

## Shared browser UI

- `src/ky-ui/` is generated from Busnes-app/ky-ui, pinned by `VERSION` file hashes. Change shared colors, navigation states and storage helpers upstream, then run its consumer sync with an explicit worktree map; do not hand-edit vendored files.
- Shell geometry (sidebar width, 40px navigation and control rows, 8px radius, title/body type) reads ky-ui tokens in the suite-shell block at the end of `src/styles/theme.css`.
- Shared files include `appearance.css`, `appearance-themes.css` and `themes.js`; the catalog and palette values come only from there. Products own layout, routes and saved choice keys. Busnes aliases consume shared tokens; mark primary navigation with `ky-nav-item` while preserving current-page semantics.
- Verify vendored files with `node src/ky-ui/check-vendor.mjs` from this document's directory. Builds/CI run that check. Rendered evidence and capture limitations are recorded in the repository-root `UI-VERIFICATION.md`; the fleet Appearance stages (screenshots, manifests with hashes and behaviour checks, results CSV, reproducible `capture.mjs`) live in `docs/ui-fleet-2026-10-26/`.

## Child DOX Index
- [browser/AGENTS.md](./browser/AGENTS.md): Production-server browser regression harness and disposable test data.
