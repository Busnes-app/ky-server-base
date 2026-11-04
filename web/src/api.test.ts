import { afterEach, describe, expect, it, vi } from 'vitest';
import { secureFetch } from './api';

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function sentToken(cookie: string): string | null {
  vi.spyOn(document, 'cookie', 'get').mockReturnValue(cookie);
  const fetcher = vi.fn().mockResolvedValue(new Response('{}'));
  vi.stubGlobal('fetch', fetcher);
  void secureFetch('/api/x', { method: 'POST' });
  return new Headers(fetcher.mock.calls[0][1].headers).get('X-CSRF-Token');
}

describe('secureFetch', () => {
  it('sends the __Host- CSRF cookie on https, ignoring a bare one a sibling host planted', () => {
    expect(sentToken('ky_csrf=planted; __Host-ky_csrf=real')).toBe('real');
  });
  it('sends the bare CSRF cookie on plain HTTP', () => {
    expect(sentToken('ky_csrf=plain')).toBe('plain');
  });
});
