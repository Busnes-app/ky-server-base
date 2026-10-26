import { test, expect } from '@playwright/test';

// Phones scroll the Primary navigation sideways. A page change that does not come from a nav
// click (an Overview action) must bring the current row into view, without moving the page
// or focus.
test('phone navigation reveals the current row after an Overview action', async ({ page }, testInfo) => {
  test.skip(testInfo.project.use.viewport.width > 390, 'phone layout only');
  await page.goto('/');
  await page.getByPlaceholder('admin', { exact: true }).fill('admin');
  await page.locator('input[type=password]').fill('BrowserUpdated456!');
  await page.getByRole('button', { name: 'Sign In', exact: true }).click();
  const nav = page.getByRole('navigation', { name: 'Primary' });
  await expect(page.getByRole('heading', { name: /Welcome/ })).toBeVisible();
  // Test-only height (CSSOM, allowed by the CSP), so the page cannot clamp its scroll:
  // any movement would come from the page change.
  await page.locator('main').evaluate(el => el.style.setProperty('min-height', '400vh'));
  const action = page.getByRole('button', { name: 'Database Config' });
  await action.scrollIntoViewIfNeeded();
  await page.evaluate(() => scrollBy(0, 200));
  const before = await page.evaluate(() => scrollY);
  expect(before).toBeGreaterThan(0);
  await action.evaluate(el => el.click()); // a DOM click does not scroll the page first
  await expect(nav.locator('[aria-current="page"]')).toHaveText('Settings & DB');
  await expect.poll(() => nav.evaluate(el => {
    const box = el.getBoundingClientRect();
    const item = el.querySelector('[aria-current="page"]').getBoundingClientRect();
    return item.left >= box.left - 1 && item.right <= box.right + 1;
  })).toBe(true);
  const after = await page.evaluate(() => ({
    y: scrollY,
    navFocused: !!document.activeElement?.closest('nav'),
  }));
  expect(after.y).toBe(before);
  expect(after.navFocused).toBe(false);
});
