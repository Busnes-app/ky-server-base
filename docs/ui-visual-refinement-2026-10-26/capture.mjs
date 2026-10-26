// Visual refinement captures against a disposable ky-server-base with synthetic data only.
// From the repository root, after `cd web && npm ci && npm run build` and
// `go build -o .browser/server ./cmd/server`:
//   node docs/ui-visual-refinement-2026-10-26/capture.mjs <stage nn> <before|after>
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { createRequire } from 'node:module';
import { connect } from 'node:net';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { networkInterfaces, release, tmpdir, type } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync, spawn } from 'node:child_process';

const [stage, phase] = process.argv.slice(2);
if (!/^\d\d$/.test(stage ?? '') || !['before', 'after'].includes(phase)) throw new Error('usage: capture.mjs <nn> <before|after>');
const evidenceDir = 'docs/ui-visual-refinement-2026-10-26';
const root = fileURLToPath(new URL('../../', import.meta.url));
const here = fileURLToPath(new URL('.', import.meta.url));
const { chromium } = createRequire(join(root, 'web/package.json'))('@playwright/test');
const port = process.env.CAPTURE_PORT ?? '5391';
const base = `http://127.0.0.1:${port}`;
const password = 'CaptureUpdated456!';

// Refuse to start when another process holds a port: the run would capture someone else's server.
for (const p of [port]) {
  const busy = await new Promise(r => { const s = connect(+p, '127.0.0.1', () => { s.destroy(); r(true); }); s.on('error', () => r(false)); });
  if (busy) throw new Error(`127.0.0.1:${p} is in use by another process; set CAPTURE_PORT`);
}
const dir = await mkdtemp(join(tmpdir(), 'kybase-capture-'));
const server = spawn(join(root, '.browser/server'), [], {
  cwd: dir, stdio: ['ignore', 'ignore', 'inherit'],
  env: {
    PATH: process.env.PATH, KY_APP_URL: base, KY_HOST: '127.0.0.1', KY_PORT: port,
    KY_DB_DRIVER: 'sqlite', KY_DATA_DIR: join(dir, 'data'), KY_BACKUP_DIR: join(dir, 'backups'),
    KY_ADMIN_PASSWORD: 'CaptureInitial123!', KY_CAPTCHA_PROVIDER: 'none', KY_LOG_LEVEL: 'error',
  },
});
const exited = new Promise(r => server.once('exit', r));
const cleanup = async () => { server.kill('SIGTERM'); await exited; await rm(dir, { recursive: true, force: true }); };

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
  const evidence = `:(exclude)${evidenceDir}`;
  const tracked = git('diff', 'HEAD', '--binary', '--', '.', evidence);
  const untracked = git('ls-files', '--others', '--exclude-standard', '-z', '--', '.', evidence).toString().split('\0').filter(Boolean);
  if (!tracked.length && !untracked.length) return null;
  const h = createHash('sha256').update(tracked);
  for (const file of untracked.sort()) h.update(file).update(execFileSync('cat', [join(root, file)]));
  return h.digest('hex');
}

// Base navigation is component state. Settings is opened by the Overview action (fleet review F6).
const surfaces = {
  overview: { nav: 'Overview', ready: /Welcome/ },
  'settings-via-overview': { action: 'Database Config', ready: 'System Settings & Architecture' },
};
const open = async (page, s) => {
  await page.goto('/');
  if (s.action) {
    await page.getByRole('heading', { name: /Welcome/ }).waitFor();
    await page.getByRole('button', { name: s.action }).click();
  } else await page.getByRole('navigation', { name: 'Primary' }).getByRole('button', { name: s.nav, exact: true }).click();
  await page.getByRole('heading', { name: s.ready }).first().waitFor();
  await page.evaluate(() => document.fonts.ready);
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
  let fonts = '';
  for (const theme of ['light', 'dark']) {
    for (const [device, viewport] of [['desktop', { width: 1280, height: 900 }], ['mobile', { width: 390, height: 844 }]]) {
      const ctx = await browser.newContext({ baseURL: base, colorScheme: theme, viewport, deviceScaleFactor: 1, storageState: storage, serviceWorkers: 'block' });
      const page = await ctx.newPage();
      for (const [surface, s] of Object.entries(surfaces)) {
        await open(page, s);
        fonts ||= await page.evaluate(() => `Space Grotesk loaded: ${document.fonts.check('15px "Space Grotesk"')}`);
        await page.waitForTimeout(300);
        const file = `kybase-${stage}-${phase}-${surface}-${device}-${theme}.png`;
        const bytes = await page.screenshot({ path: join(out, file), fullPage: true });
        shots.push({ file, sha256: sha(bytes), surface, nav: s.nav ?? `Overview action: ${s.action}`, device, viewport, theme_source: `OS ${theme} (prefers-color-scheme), no saved choice` });
      }
      await ctx.close();
    }
  }
  // Behaviour checks in the real app, both phases: each records pass/fail, none aborts.
  const checks = [];
  const check = async (name, fn) => {
    try { checks.push({ name, pass: true, detail: (await fn()) ?? '' }); } catch (e) { checks.push({ name, pass: false, detail: String(e.message ?? e).split('\n')[0] }); }
  };
  const errors = [];
  const ctx = await browser.newContext({ baseURL: base, colorScheme: 'light', viewport: { width: 1280, height: 900 }, storageState: storage });
  const page = await ctx.newPage();
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('pageerror', e => errors.push(e.message));
  for (const [surface, s] of Object.entries(surfaces)) {
    await check(`${surface}: title to introduction gap`, async () => {
      await open(page, s);
      const gap = await page.evaluate(() => Math.round(document.querySelector('.page-intro').getBoundingClientRect().top - document.querySelector('h1').getBoundingClientRect().bottom));
      if (gap !== 8) throw new Error(`${gap}px`);
      return `${gap}px`;
    });
  }
  await check('settings: no tinted box inside the database card', async () => {
    const nested = await page.locator('main .panel').first().evaluate(card => [...card.querySelectorAll('*')].filter(el => getComputedStyle(el).backgroundColor !== 'rgba(0, 0, 0, 0)' && getComputedStyle(el).borderStyle !== 'none' && el.offsetWidth > card.offsetWidth / 2).length);
    if (nested) throw new Error(`${nested} bordered, filled box(es)`);
    return 'none';
  });
  await check('settings: the card uses no accent colour', async () => {
    const hits = await page.locator('main .panel').first().evaluate(card => {
      const probe = document.body.appendChild(document.createElement('i'));
      probe.style.color = 'var(--accent)';
      const accent = getComputedStyle(probe).color;
      probe.remove();
      return [...card.querySelectorAll('*')].filter(el => { const cs = getComputedStyle(el); return [cs.color, cs.borderTopColor, cs.backgroundColor].includes(accent); }).map(el => el.tagName.toLowerCase() + (el.className?.baseVal ?? el.className ? `.${(el.className?.baseVal ?? el.className).split(' ').join('.')}` : ''));
    });
    if (hits.length) throw new Error(hits.join(', '));
    return 'none';
  });
  await check('no console errors or CSP violations', async () => { if (errors.length) throw new Error(errors.join(' | ')); return 'none'; });
  await ctx.close();

  const manifest = {
    product: 'ky_server_base', stage, phase, captured_at: new Date().toISOString(),
    source_commit: git('rev-parse', 'HEAD').toString().trim(), uncommitted_source_sha256: worktreeHash(),
    os: `${type()} ${release()}`, driver: `Playwright ${createRequire(join(root, 'web/package.json'))('@playwright/test/package.json').version} Chromium ${browser.version()}, device scale 1, service workers blocked`,
    fonts: `self-hosted Space Grotesk bundled in web/dist; ${fonts}`,
    server: 'disposable .browser/server (built from this tree, embedding web/dist) on loopback with temporary SQLite data, removed after capture',
    fixture: 'synthetic: fresh install with the bootstrap admin only (password changed by the script); no SCIM, devices or backups configured',
    shots,
    checks,
  };
  await writeFile(join(here, `manifest-${stage}-${phase}.json`), JSON.stringify(manifest, null, 2) + '\n');
  await browser.close();
  console.log(`${shots.length} captures, ${checks.filter(c => c.pass).length}/${checks.length} checks passed, manifest-${stage}-${phase}.json`);
  for (const c of checks.filter(c => !c.pass)) console.log(`FAIL ${c.name}: ${c.detail}`);
} finally {
  await cleanup();
}
