import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { Login } from './Login';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('offers single sign-on once, as one labelled link', () => {
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
  const { container } = render(<Login onSuccess={vi.fn()} appName="Test" />);
  expect(screen.getByRole('link', { name: 'Continue with Single Sign On' }).getAttribute('href')).toBe('/api/sso/kysignon/login');
  expect(container.textContent?.match(/Single Sign.?On/gi)).toHaveLength(1);
});

async function sha256Hex(text: string): Promise<string> {
  const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
  return Array.from(new Uint8Array(buf)).map((b) => b.toString(16).padStart(2, '0')).join('');
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

async function stubAuth(routes: Record<string, () => Response>) {
  const challenge = { algorithm: 'SHA-256', salt: 's', challenge: await sha256Hex('s1'), maxnumber: 1, expires_at: 0, signature: 'x' };
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/auth/pow-challenge') return json(challenge);
    if (routes[url]) return routes[url]();
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

// Each solve sets the token in Login's state; with an unstable callback that restarted the
// widget, and an idle login tab fetched and solved challenges forever.
it('solves one CAPTCHA challenge, not a stream of them', async () => {
  const fetchMock = await stubAuth({});
  render(<Login onSuccess={vi.fn()} appName="Test" />);
  await screen.findByText('Security check verified.');
  await new Promise((r) => setTimeout(r, 200));
  expect(fetchMock.mock.calls.filter(([u]) => String(u) === '/api/auth/pow-challenge')).toHaveLength(1);
});

// A token can expire while the tab sleeps; a failed sign-in solves a fresh one for the retry.
it('solves a fresh CAPTCHA after a failed sign-in', async () => {
  const fetchMock = await stubAuth({ '/api/auth/login': () => json({ error: 'Invalid credentials' }, 401) });
  const { container } = render(<Login onSuccess={vi.fn()} appName="Test" />);
  await screen.findByText('Security check verified.');
  fireEvent.change(container.querySelector('input[type="text"]')!, { target: { value: 'admin' } });
  fireEvent.change(container.querySelector('input[type="password"]')!, { target: { value: 'wrong password!' } });
  fireEvent.click(screen.getByRole('button', { name: /sign in/i }));
  await screen.findByText('Invalid credentials');
  await vi.waitFor(() => expect(fetchMock.mock.calls.filter(([u]) => String(u) === '/api/auth/pow-challenge')).toHaveLength(2));
});

// Sign In waits for a token, so a failed security check must offer its own retry.
it('retries a failed security check without a reload', async () => {
  let calls = 0;
  const challenge = { algorithm: 'SHA-256', salt: 's', challenge: await sha256Hex('s1'), maxnumber: 1, expires_at: 0, signature: 'x' };
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input) !== '/api/auth/pow-challenge') throw new Error(`unexpected fetch ${String(input)}`);
    calls++;
    return calls === 1 ? json({ error: 'down' }, 503) : json(challenge);
  }));
  render(<Login onSuccess={vi.fn()} appName="Test" />);
  fireEvent.click(await screen.findByRole('button', { name: 'Try again' }));
  await screen.findByText('Security check verified.');
  expect((screen.getByRole('button', { name: /sign in/i }) as HTMLButtonElement).disabled).toBe(false);
});

// At the default difficulty the solve takes about a second; Enter before that would be refused.
it('keeps Sign In disabled until the security check is solved', async () => {
  await stubAuth({});
  render(<Login onSuccess={vi.fn()} appName="Test" />);
  const button = screen.getByRole('button', { name: /sign in/i }) as HTMLButtonElement;
  expect(button.disabled).toBe(true);
  await screen.findByText('Security check verified.');
  expect(button.disabled).toBe(false);
});
