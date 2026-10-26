# Visual refinement evidence: ky-server-base (2026-10-26)

Before/after captures and real-browser checks for the fleet visual-refinement batch
(baseline review gap 7: a tinted box inside the Settings & DB card, a decorative orange
icon and `sqlite` chip; the title-to-introduction gap). Ratings belong to the independent
reviewer; this record claims no score.

## Stages

| Stage | Source | What it shows |
| --- | --- | --- |
| `00-before` | clean `3bc0780` (origin/master) | baseline; 1/5 checks (12px title gap, nested tinted box, accent icon and chip) |
| `01-after` | clean `b2c7733` | flat card; 5/5 checks. Hairlines broke at the column gap and label/value baselines differed; the overview still had an orange `sqlite (active)` chip |
| `02-after` | clean `662642b` | the one confirmation recapture after those fixes; 5/5 checks |

`overview` and `settings-via-overview` (Settings opened by the Overview "Database Config"
action, as in fleet review F6), light and dark OS scheme × 1280×900 and 390×844, full page.
Manifests record source commit, uncommitted-source hash (null), OS, Playwright/Chromium,
font state, server, fixture, time, PNG SHA-256s and checks. The F7 injected diagnostic
button is not part of this set.

Fixture: `.browser/server` built from the tree (embedding the committed `web/dist`) on
127.0.0.1:5391 with temporary SQLite; the bootstrap admin only, password changed by the
script; no SCIM, devices or backups configured.

## Changes

- Settings & DB: the database card is a titled section with one line of context and
  label/value rows on the card surface (`.settings-facts`). No nested tinted box, no
  accent icon, the driver as plain mono text. Copy and the reported driver are unchanged
  (label colons dropped).
- Overview: the database card's `sqlite (active)` state uses the neutral badge, as the
  other cards' plain states do.
- Page introductions sit 8px under the title (was 12px), on every page using `.page-intro`.
- `web/dist` rebuilt and committed.

## Checks

Per stage in `behaviour-checks.csv`: an 8px title-to-introduction gap on both pages, no
bordered filled box inside the database card, no accent colour in it, and no console or
CSP errors. Required checks (including F6 `nav.spec` and F7 `danger-button.spec`) are in
the pull request.

## Known limits

- The sidebar "Pair Device" button keeps its orange phone icon; the overview card icons
  keep their tinted tiles. Not in this batch.
- The Directory & SCIM page also uses `.page-intro`; its 4px gap change was not captured.

## Reproduce

From the repository root, after `cd web && npm ci && npm run build` and
`go build -o .browser/server ./cmd/server`:
`node docs/ui-visual-refinement-2026-10-26/capture.mjs 02 after`, then `results.mjs`.
