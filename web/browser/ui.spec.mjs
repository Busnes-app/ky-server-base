import { test, expect } from '@playwright/test';
import { THEME_CATALOG } from '../src/ky-ui/themes.js';

// Relative luminance of a computed colour: rgb() channels are 0-255, color(srgb …) 0-1.
const luminance = color => {
  const scale = color.startsWith('color(') ? 1 : 255;
  const [r, g, b] = color.match(/[\d.]+/g).slice(0, 3).map(v => Number(v) / scale).map(c => c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};

async function fits(page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}

test('production CSP, worker, themes, keyboard and responsive shell', async ({ page, context }, testInfo) => {
  const violations = [];
  page.on('console', message => {
    if (/Content Security Policy|violates.*directive/i.test(message.text())) violations.push(message.text());
  });
  const response = await page.goto('/');
  expect(response.headers()['content-security-policy']).toContain("script-src 'self'");
  expect(response.headers()['content-security-policy']).not.toContain("script-src 'self' 'unsafe-inline'");
  await expect.poll(() => page.evaluate(async () => (await navigator.serviceWorker.getRegistration())?.active?.state)).toBe('activated');
  await page.evaluate(async () => {
    const key = (await caches.keys())[0];
    await (await caches.open(key)).put('/', new Response('<h1>Stale shell</h1>', {
      headers: { 'Content-Type': 'text/html' },
    }));
  });
  // Online navigation must refresh the shell rather than pinning a previous deploy.
  await page.reload();
  await page.getByPlaceholder('admin', { exact: true }).fill('admin');
  await page.locator('input[type=password]').fill('IncorrectPassword123!');
  await page.getByRole('button', { name: 'Sign In', exact: true }).click();
  await expect(page.locator('form').locator('..')).toContainText(/invalid|incorrect|failed/i);
  await fits(page);
  await page.locator('input[type=password]').fill('BrowserUpdated456!');
  await page.getByRole('button', { name: 'Sign In', exact: true }).click();
  const nav = page.getByRole('navigation', { name: 'Primary' });
  await expect(nav).toBeVisible();
  await expect(page.getByRole('heading', { name: /Welcome/ })).toBeVisible();
  await fits(page);
  // The Appearance page is the only theme control in the signed-in shell.
  await expect(page.locator('select[aria-label="Color theme"]')).toHaveCount(0);
  const openAppearance = async p => {
    await p.getByRole('navigation', { name: 'Primary' }).getByRole('button', { name: 'Appearance' }).click();
    await p.getByRole('group', { name: 'Theme' }).waitFor();
  };
  const theme = (p, id) => p.getByRole('group', { name: 'Theme' }).locator(`input[value="${id}"]`);
  await openAppearance(page);
  await theme(page, 'paper').check();
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(248, 250, 252)');
  await theme(page, 'busnes-dark').check();
  await page.reload();
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(24, 35, 38)');
  const other = await context.newPage();
  await other.goto('/');
  await openAppearance(other);
  await theme(other, 'busnes-light').check();
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(248, 246, 240)');
  await other.close();
  await openAppearance(page);
  await theme(page, 'system').check();
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(24, 35, 38)');
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(248, 246, 240)');
  await page.emulateMedia({ colorScheme: testInfo.project.use.colorScheme });
  await nav.getByRole('button').first().focus();
  await page.keyboard.press('Tab');
  await expect(nav.getByRole('button').nth(1)).toBeFocused();
  await expect(nav.getByRole('button').nth(1)).toHaveCSS('outline-style', 'solid');
  await nav.getByRole('button', { name: 'Settings & DB' }).click();
  await expect(nav.getByRole('button', { name: 'Settings & DB' })).toHaveAttribute('aria-current', 'page');
  await expect(page.getByRole('heading', { name: 'System Settings & Architecture' })).toBeVisible();
  await fits(page);
  // Appearance: native radio cards for every catalog theme, chosen by keyboard.
  await nav.getByRole('button', { name: 'Appearance' }).click();
  const picker = page.getByRole('group', { name: 'Theme' });
  await expect(picker.getByRole('radio')).toHaveCount(THEME_CATALOG.length + 1);
  await expect(picker.getByRole('radio', { name: /Match system/ })).toBeChecked();
  await picker.getByRole('radio', { name: /Coffee/ }).check();
  await page.keyboard.press('ArrowLeft');
  await expect(picker.getByRole('radio', { name: /Ocean/ })).toBeChecked();
  await expect(picker.getByRole('radio', { name: /Ocean/ })).toBeFocused();
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(15, 27, 36)');
  await fits(page);
  // A new light named theme has no product CSS block: hover, selection and focus must
  // come from its own palette, not the dark base :root values.
  await picker.getByRole('radio', { name: /^Sky Light$/ }).check();
  const idle = nav.getByRole('button').first();
  await idle.hover();
  const hovered = await idle.evaluate(el => [getComputedStyle(el).backgroundColor, getComputedStyle(el).color]);
  expect(luminance(hovered[0])).toBeGreaterThan(0.6);
  expect(luminance(hovered[1])).toBeLessThan(0.2);
  const current = nav.locator('[aria-current="page"]');
  expect(luminance(await current.evaluate(el => getComputedStyle(el).backgroundColor))).toBeGreaterThan(0.5);
  // Keyboard round trip so the radio has :focus-visible, ending back on Sky.
  await picker.getByRole('radio', { name: /^Sky Light$/ }).focus();
  await page.keyboard.press('ArrowRight');
  await page.keyboard.press('ArrowLeft');
  await expect(picker.getByRole('radio', { name: /^Sky Light$/ })).toBeChecked();
  await expect(picker.locator('label', { hasText: 'Sky' })).toHaveCSS('outline-style', 'solid');
  await picker.getByRole('radio', { name: /^Busnes Light Light$/ }).check();
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(248, 246, 240)');
  expect(await page.evaluate(() => document.documentElement.style.getPropertyValue('--panel-hover'))).toBe('');
  await picker.getByRole('radio', { name: /Match system/ }).check();
  await expect(page.locator('body')).toHaveCSS('background-color', testInfo.project.use.colorScheme === 'dark' ? 'rgb(24, 35, 38)' : 'rgb(248, 246, 240)');
  // New suite palettes must keep status text readable against both app surfaces.
  await nav.getByRole('button', { name: 'Appearance' }).click();
  const statusContrast = [];
  for (const id of ['light-matter', 'tropics', 'white-cliffs', 'sun', 'polished-ky', 'sky']) {
    await page.locator(`.ky-appearance-radio[value="${id}"]`).check();
    statusContrast.push(...await page.evaluate((theme) => {
      const root = document.documentElement;
      const probe = document.createElement('span');
      root.append(probe);
      const luminance = (value) => {
        probe.style.color = value;
        const color = getComputedStyle(probe).color;
        const scale = color.startsWith('color(') ? 1 : 255;
        return color.match(/[\d.]+/g).slice(0, 3).map(Number)
          .map(c => c / scale).map(c => c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4)
          .reduce((sum, c, i) => sum + c * [0.2126, 0.7152, 0.0722][i], 0);
      };
      const rows = ['--danger', '--success'].flatMap(status => ['--bg', '--panel'].map(surface => {
        const [a, b] = [status, surface].map(v => luminance(`var(${v})`));
        return { theme, status, surface, ratio: (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05) };
      }));
      probe.remove();
      return rows;
    }, id));
  }
  await page.mouse.move(0, 0);
  await testInfo.attach('status-contrast', { body: JSON.stringify(statusContrast, null, 2), contentType: 'application/json' });
  await page.screenshot({ path: testInfo.outputPath('sky-status-contrast.png'), fullPage: true });
  for (const row of statusContrast) expect(row.ratio, JSON.stringify(row)).toBeGreaterThanOrEqual(4.5);
  await page.evaluate(() => fetch('/browser-regression-uncached'));
  const cachedDynamic = await page.evaluate(async () => {
    const keys = await caches.keys();
    for (const key of keys) for (const req of await (await caches.open(key)).keys()) {
      if (/^\/(api|scim|saml|browser-regression-uncached)(\/|$)/.test(new URL(req.url).pathname)) return true;
    }
    return false;
  });
  expect(cachedDynamic).toBe(false);
  expect(violations).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath('settings.png'), fullPage: true });
  await context.setOffline(true);
  const offline = await page.reload();
  expect(offline.ok()).toBe(true);
  expect(await offline.text()).toContain('id="root"');
  await context.setOffline(false);
});
