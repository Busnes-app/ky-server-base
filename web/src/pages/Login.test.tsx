import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { Login } from './Login';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('offers single sign-on once, as one labelled link', () => {
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
  const { container } = render(<Login onSuccess={vi.fn()} appName="Test" />);
  expect(screen.getByRole('link', { name: 'Continue with Single Sign On' }).getAttribute('href')).toBe('/api/sso/kysignon/login');
  expect(container.textContent?.match(/Single Sign.?On/gi)).toHaveLength(1);
});
