import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { Appearance } from './Appearance';
import { ThemeSwitcher } from '../components/ThemeSwitcher';
import { THEME_CATALOG } from '../ky-ui/themes';

const root = document.documentElement;

describe('Appearance', () => {
  afterEach(() => {
    cleanup();
    localStorage.clear();
    window.dispatchEvent(new StorageEvent('storage', { key: 'ky_theme' }));
  });

  it('lists every suite theme as a native radio card, keeping a saved legacy choice', () => {
    localStorage.setItem('ky_theme', 'paper');
    window.dispatchEvent(new StorageEvent('storage', { key: 'ky_theme' }));
    render(<Appearance />);
    const radios = screen.getAllByRole('radio');
    expect(radios).toHaveLength(THEME_CATALOG.length + 1);
    expect(screen.getByRole('group', { name: 'Theme' })).toBeTruthy();
    expect(screen.getByRole('radio', { name: /Paper Clean/ })).toHaveProperty('checked', true);
    expect(screen.getByRole('radio', { name: /Match system/ })).toHaveProperty('checked', false);
    expect(document.querySelector('[data-ky-preview="ocean"]')).not.toBeNull();
  });

  it('keeps the base Patina palette for a saved patina choice, distinct from suite Patina Ky', () => {
    localStorage.setItem('ky_theme', 'patina');
    window.dispatchEvent(new StorageEvent('storage', { key: 'ky_theme' }));
    render(<Appearance />);
    expect(screen.getByRole('radio', { name: /Patina Ky \(base\)/ })).toHaveProperty('checked', true);
    expect(root.style.getPropertyValue('--ink-strong')).toBe('#f1f5f9');
    expect(root.style.getPropertyValue('--panel-hover')).toBe('#1c222c');
    fireEvent.click(document.querySelector('input[value="patina-ky"]')!);
    expect(localStorage.getItem('ky_theme')).toBe('patina-ky');
    expect(root.style.getPropertyValue('--ink-strong')).toBe('#e2e8f0');
  });

  it('applies a named palette inline and clears it again for Busnes', () => {
    render(<><ThemeSwitcher /><Appearance /></>);
    expect(screen.getByRole('radio', { name: /Match system/ })).toHaveProperty('checked', true);
    fireEvent.click(screen.getByRole('radio', { name: /Ocean/ }));
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
