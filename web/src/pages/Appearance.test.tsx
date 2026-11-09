import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { Appearance } from './Appearance';
import { ThemeSwitcher } from '../components/ThemeSwitcher';
import { THEME_CATALOG } from '../ky-ui/themes';

const root = document.documentElement;
const retired = ['tropics', 'tropic-night', 'coffee', 'cyber-punk', 'neon-purple', 'space', 'sky', 'sun', 'cyber', 'oled'];

describe('Appearance', () => {
  afterEach(() => {
    cleanup();
    localStorage.clear();
    fireEvent(window, new StorageEvent('storage', { key: 'ky_theme' }));
  });

  it('lists the current suite catalog and keeps a saved current choice', () => {
    localStorage.setItem('ky_theme', 'paper');
    fireEvent(window, new StorageEvent('storage', { key: 'ky_theme' }));
    render(<Appearance />);
    expect(screen.getAllByRole('radio')).toHaveLength(THEME_CATALOG.length + 1);
    expect(screen.getByRole('group', { name: 'Theme' })).toBeTruthy();
    expect(screen.getByRole('radio', { name: /Nord Light/ })).toHaveProperty('checked', true);
    expect(screen.getByRole('radio', { name: /Match system/ })).toHaveProperty('checked', false);
    expect(document.querySelector('[data-ky-preview="ocean"]')).not.toBeNull();
  });

  it.each(retired)('falls back to system Busnes for the retired %s choice', id => {
    render(<Appearance />);
    fireEvent.click(screen.getByRole('radio', { name: /Ocean Dark/ }));
    localStorage.setItem('ky_theme', id);
    fireEvent(window, new StorageEvent('storage', { key: 'ky_theme' }));
    expect(screen.getByRole('radio', { name: /Match system/ })).toHaveProperty('checked', true);
    expect(root.dataset.theme).toBe('busnes-light');
    expect(root.style.getPropertyValue('--bg')).toBe('');
    expect(root.style.getPropertyValue('--ink-strong')).toBe('');
    expect(document.querySelector(`input[value="${id}"]`)).toBeNull();
  });

  it('uses aliases retained in the current catalog', () => {
    localStorage.setItem('ky_theme', 'patina');
    fireEvent(window, new StorageEvent('storage', { key: 'ky_theme' }));
    render(<Appearance />);
    expect(screen.getByRole('radio', { name: /Ky Dark/ })).toHaveProperty('checked', true);
    expect(root.dataset.theme).toBe('patina-ky');
    expect(root.style.getPropertyValue('--ink-strong')).toBe('#e2e8f0');
  });

  it('applies a named palette inline and clears it again for Busnes', () => {
    render(<><ThemeSwitcher /><Appearance /></>);
    expect(screen.getByRole('radio', { name: /Match system/ })).toHaveProperty('checked', true);
    fireEvent.click(screen.getByRole('radio', { name: /Ocean Dark/ }));
    expect(screen.getByLabelText('Color theme')).toHaveProperty('value', 'ocean');
    expect(localStorage.getItem('ky_theme')).toBe('ocean');
    expect(root.dataset.theme).toBe('ocean');
    expect(root.style.getPropertyValue('--bg')).toBe('#0f1b24');
    expect(root.style.colorScheme).toBe('dark');
    fireEvent.change(screen.getByLabelText('Color theme'), { target: { value: 'busnes-light' } });
    expect(screen.getByRole('radio', { name: /Busnes Light/ })).toHaveProperty('checked', true);
    expect(localStorage.getItem('ky_theme')).toBe('busnes-light');
    expect(root.dataset.theme).toBe('busnes-light');
    expect(root.style.getPropertyValue('--bg')).toBe('');
    expect(root.style.colorScheme).toBe('light');
  });
});
