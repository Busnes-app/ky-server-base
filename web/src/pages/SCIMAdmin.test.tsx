import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { SCIMAdmin } from './SCIMAdmin';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

// The SCIM API refuses a browser session; the page must say so instead of reporting an empty
// directory, and must not offer placeholder text as a token to copy into the IdP.
it('reports a refused account list and offers no fake token', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/settings') return new Response(JSON.stringify({ extra_settings: {} }), { status: 200 });
    if (url === '/scim/v2/Users') return new Response('{}', { status: 401 });
    throw new Error(`unexpected fetch ${url}`);
  }));
  const { container } = render(<SCIMAdmin />);
  expect((await screen.findByRole('alert')).textContent).toMatch(/HTTP 401/);
  expect(container.textContent).not.toMatch(/No users provisioned yet/);
  expect(container.textContent).toMatch(/KY_SCIM_TOKEN/);
  expect(container.querySelector('input[type="password"]')).toBeNull();
});
