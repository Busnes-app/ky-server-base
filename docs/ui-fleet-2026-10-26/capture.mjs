// Staged UI captures against a disposable ky-server-base with synthetic data only.
// From the repository root, after `cd web && npm ci && npm run build` and
// `go build -o .browser/server ./cmd/server`:
//   node docs/ui-fleet-2026-10-26/capture.mjs <stage nn> <before|after>
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { createRequire } from 'node:module';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { networkInterfaces, release, tmpdir, type } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync, spawn } from 'node:child_process';

const [stage, phase] = process.argv.slice(2);
if (!/^\d\d$/.test(stage ?? '') || !['before', 'after'].includes(phase)) throw new Error('usage: capture.mjs <nn> <before|after>');
const root = fileURLToPath(new URL('../../', import.meta.url));
const here = fileURLToPath(new URL('.', import.meta.url));
const { chromium } = createRequire(join(root, 'web/package.json'))('@playwright/test');
const port = process.env.CAPTURE_PORT ?? '5492';
const base = `http://127.0.0.1:${port}`;
const password = 'CaptureUpdated456!';

const dir = await mkdtemp(join(tmpdir(), 'kybase-capture-'));
const server = spawn(join(root, '.browser/server'), [], {
  cwd: dir, stdio: ['ignore', 'ignore', 'inherit'],
  env: {
    PATH: process.env.PATH, KY_APP_URL: base, KY_HOST: '127.0.0.1', KY_PORT: port,
    KY_DB_DRIVER: 'sqlite', KY_DATA_DIR: join(dir, 'data'), KY_BACKUP_DIR: join(dir, 'backups'),
    KY_ADMIN_PASSWORD: 'CaptureInitial123!', KY_CAPTCHA_PROVIDER: 'none', KY_LOG_LEVEL: 'error',
  },
});
const cleanup = async () => { server.kill('SIGTERM'); await rm(dir, { recursive: true, force: true }); };

async function api(ctx, method, path, data) {
  const csrf = (await ctx.storageState()).cookies.find(c => c.name === 'ky_csrf')?.value;
  const res = await ctx.fetch(base + path, { method, data, headers: csrf ? { 'X-CSRF-Token': csrf } : {} });
  if (!res.ok()) throw new Error(`${method} ${path}: ${res.status()} ${await res.text()}`);
  return res.json().catch(() => null);
}

function sha(bytes) { return createHash('sha256').update(bytes).digest('hex'); }
function git(...args) { return execFileSync('git', ['-C', root, ...args]); }
// Hash of uncommitted source, excluding this evidence directory.
function worktreeHash() {
  const evidence = ':(exclude)docs/ui-fleet-2026-10-26';
  const tracked = git('diff', 'HEAD', '--binary', '--', '.', evidence);
  const untracked = git('ls-files', '--others', '--exclude-standard', '-z', '--', '.', evidence).toString().split('\0').filter(Boolean);
  if (!tracked.length && !untracked.length) return null;
  const h = createHash('sha256').update(tracked);
  for (const file of untracked.sort()) h.update(file).update(execFileSync('cat', [join(root, file)]));
  return h.digest('hex');
}

// Base navigation is component state, so surfaces are reached through the Primary nav.
const surfaces = {
  overview: { nav: 'Overview', ready: /Welcome/ },
  appearance: { nav: phase === 'before' ? 'Settings & DB' : 'Appearance', ready: phase === 'before' ? 'System Settings & Architecture' : 'Appearance' },
  // Fleet review F6: a page change from an Overview action, not a nav click (which scrolls the nav itself).
  ...(stage >= '03' && { 'settings-via-overview': { action: 'Database Config', ready: 'System Settings & Architecture' } }),
  // Fleet review F7: Base renders no .btn-danger, so a labelled diagnostic button is added to the
  // real Overview under a saved light (Sky) or dark (Forest) suite palette.
  ...(stage >= '05' && {
    'diagnostic-danger-sky': { nav: 'Overview', ready: /Welcome/, saved: 'sky', diagnostic: true },
    'diagnostic-danger-forest': { nav: 'Overview', ready: /Welcome/, saved: 'forest', diagnostic: true },
  }),
};

try {
  const browser = await chromium.launch();
  const setup = await browser.newContext({ baseURL: base });
  for (let i = 0; ; i++) {
    try { await setup.request.get('/healthz'); break; } catch (e) { if (i > 100) throw e; await new Promise(r => setTimeout(r, 100)); }
  }
  await setup.request.get('/');
  await api(setup.request, 'POST', '/api/auth/login', { username: 'admin', password: 'CaptureInitial123!' });
  await api(setup.request, 'POST', '/api/auth/change-password', { current_password: 'CaptureInitial123!', new_password: password });
  await api(setup.request, 'POST', '/api/auth/login', { username: 'admin', password });
  const storage = await setup.storageState();
  await setup.close();

  const out = join(here, 'screenshots');
  await mkdir(out, { recursive: true });
  const shots = [];
  for (const theme of ['light', 'dark']) {
    for (const [device, viewport] of [['desktop', { width: 1280, height: 900 }], ['mobile', { width: 390, height: 844 }]]) {
      const ctx = await browser.newContext({ baseURL: base, colorScheme: theme, viewport, deviceScaleFactor: 1, storageState: storage, serviceWorkers: 'block' });
      const page = await ctx.newPage();
      for (const [surface, s] of Object.entries(surfaces)) {
        await page.goto('/');
        await page.evaluate(v => v ? localStorage.setItem('ky_theme', v) : localStorage.removeItem('ky_theme'), s.saved ?? null);
        await page.reload();
        if (s.action) {
          await page.getByRole('heading', { name: /Welcome/ }).waitFor();
          await page.getByRole('button', { name: s.action }).click();
        } else await page.getByRole('navigation', { name: 'Primary' }).getByRole('button', { name: s.nav, exact: true }).click();
        await page.getByRole('heading', { name: s.ready }).first().waitFor();
        if (s.diagnostic) await page.locator('main').evaluate(main => {
          const button = main.insertBefore(document.createElement('button'), main.firstChild);
          button.type = 'button';
          button.className = 'btn-danger';
          button.textContent = 'F7 diagnostic: .btn-danger (class unused in Base)';
        });
        await page.evaluate(() => document.fonts.ready);
        await page.waitForTimeout(300);
        const file = `kybase-${stage}-${phase}-${surface}-${device}-${theme}.png`;
        const bytes = await page.screenshot({ path: join(out, file), fullPage: true });
        shots.push({ file, sha256: sha(bytes), surface, nav: s.nav ?? `Overview action: ${s.action}`, device, viewport, ...(s.diagnostic && { diagnostic: 'injected labelled .btn-danger; not a Base control' }), theme_source: s.saved ? `saved choice ${s.saved}; OS ${theme} (prefers-color-scheme)` : `OS ${theme} (prefers-color-scheme), no saved choice` });
      }
      await ctx.close();
    }
  }
  const manifest = {
    product: 'ky_server_base', stage, phase, captured_at: new Date().toISOString(),
    source_commit: git('rev-parse', 'HEAD').toString().trim(), uncommitted_source_sha256: worktreeHash(),
    os: `${type()} ${release()}`, driver: `Playwright ${createRequire(join(root, 'web/package.json'))('@playwright/test/package.json').version} Chromium ${browser.version()}, device scale 1, service workers blocked`,
    server: 'disposable .browser/server on loopback with temporary SQLite data, removed after capture',
    fixture: 'synthetic: fresh install with the bootstrap admin only (password changed by the script); no SCIM, devices or backups configured',
    shots,
  };
  await writeFile(join(here, `manifest-${stage}-${phase}.json`), JSON.stringify(manifest, null, 2) + '\n');
  await browser.close();
  console.log(`${shots.length} captures, manifest-${stage}-${phase}.json`);
} finally {
  await cleanup();
}
