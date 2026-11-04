import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { SCIMAdmin } from './SCIMAdmin';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

// The page lists accounts through the admin route (the SCIM API takes only the IdP's token).
it('lists every account with its source from the admin route', async () => {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/settings') return json({ extra_settings: {} });
    if (url.startsWith('/api/admin/users')) {
      return json({
        users: [
          { id: 'u1', username: 'admin', display_name: 'Administrator', email: '', role: 'admin', status: 'active', sso_provider: 'local', scim_managed: false },
          { id: 'u2', username: 'frank', display_name: 'Frank', email: 'f@corp.test', role: 'user', status: 'inactive', sso_provider: 'kysignon', scim_managed: true },
        ],
        total: 2, offset: 0, limit: 50,
      });
    }
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  const { container } = render(<SCIMAdmin />);
  expect(await screen.findByText('frank')).toBeTruthy();
  expect(container.textContent).toMatch(/Accounts \(2\)/);
  expect(container.textContent).toMatch(/Local/);
  expect(container.textContent).toMatch(/SCIM/);
  expect(fetchMock.mock.calls.some(([u]) => String(u).startsWith('/scim/'))).toBe(false);
});

// A failed load is reported, never shown as an empty directory, and no placeholder token is offered.
it('reports a refused account list and offers no fake token', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/settings') return json({ extra_settings: {} });
    if (url.startsWith('/api/admin/users')) return json({ error: 'Administrator role required' }, 403);
    throw new Error(`unexpected fetch ${url}`);
  }));
  const { container } = render(<SCIMAdmin />);
  expect((await screen.findByRole('alert')).textContent).toMatch(/HTTP 403/);
  expect(container.textContent).not.toMatch(/No accounts yet/);
  expect(container.textContent).toMatch(/KY_SCIM_TOKEN/);
  expect(container.querySelector('input[type="password"]')).toBeNull();
});
