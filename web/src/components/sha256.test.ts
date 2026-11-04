import { expect, it } from 'vitest';
import { sha256Hex } from './sha256';

async function reference(text: string): Promise<string> {
  const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
  return Array.from(new Uint8Array(buf)).map((b) => b.toString(16).padStart(2, '0')).join('');
}

it('matches SHA-256 across block boundaries and non-ASCII input', async () => {
  expect(sha256Hex('')).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855');
  expect(sha256Hex('abc')).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
  for (const text of ['a'.repeat(55), 'a'.repeat(56), 'a'.repeat(64), 'a'.repeat(1000), 'ünïcødé ✓', 'f3a9c0d2e1b4a5c6123456']) {
    expect(sha256Hex(text)).toBe(await reference(text));
  }
});
