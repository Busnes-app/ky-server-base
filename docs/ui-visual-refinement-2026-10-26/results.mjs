// Flattens the capture manifests in this directory into spreadsheet CSVs:
// captures.csv (one row per screenshot) and behaviour-checks.csv.
//   node docs/ui-visual-refinement-2026-10-26/results.mjs
import { readdir, readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

const here = fileURLToPath(new URL('.', import.meta.url));
const cell = v => { const s = v == null ? '' : String(v); return /[",\n]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s; };
const csv = rows => rows.map(r => r.map(cell).join(',')).join('\n') + '\n';

const manifests = [];
for (const name of (await readdir(here)).filter(n => /^manifest-\d\d-(before|after)\.json$/.test(n)).sort()) {
  manifests.push(JSON.parse(await readFile(join(here, name), 'utf8')));
}
const captures = [['product', 'stage', 'phase', 'surface', 'device', 'viewport', 'theme_source', 'file', 'sha256', 'source_commit', 'uncommitted_source_sha256', 'captured_at', 'os', 'driver', 'fixture']];
const checks = [['product', 'stage', 'phase', 'check', 'result', 'detail', 'source_commit', 'uncommitted_source_sha256', 'captured_at']];
for (const m of manifests) {
  for (const s of m.shots) captures.push([m.product, m.stage, m.phase, s.surface, s.device, `${s.viewport.width}x${s.viewport.height}`, s.theme_source, `screenshots/${s.file}`, s.sha256, m.source_commit, m.uncommitted_source_sha256, m.captured_at, m.os, m.driver, m.fixture]);
  for (const c of m.checks ?? []) checks.push([m.product, m.stage, m.phase, c.name, c.pass ? 'pass' : 'fail', c.detail, m.source_commit, m.uncommitted_source_sha256, m.captured_at]);
}
await writeFile(join(here, 'captures.csv'), csv(captures));
await writeFile(join(here, 'behaviour-checks.csv'), csv(checks));
console.log(`${captures.length - 1} captures, ${checks.length - 1} checks`);
