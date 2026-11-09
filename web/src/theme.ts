import { useSyncExternalStore } from 'react';
import { readChoice, saveChoice, watchChoice } from './ky-ui/theme';
import { SYSTEM_THEME, THEME_CATALOG, applyThemeVariables, findTheme, THEME_VARIABLES } from './ky-ui/themes';
import { LEGACY_THEMES } from './legacyThemes';
const findOption = (value: string | null) => LEGACY_THEMES.find(theme =>
  theme.id === value || theme.name === value || (value !== null && theme.aliases.includes(value))) ?? findTheme(value);
// The shared suite catalog; explicit choices are local to this browser under the same key.
export const THEME_OPTIONS = [SYSTEM_THEME, ...THEME_CATALOG, ...LEGACY_THEMES];
export function themeMode(theme: string): string {
  const scheme = findOption(theme)?.scheme;
  return scheme === 'light' ? 'Light' : scheme === 'dark' ? 'Dark' : 'Follows device';
}
export function storedTheme(): string {
  return findOption(readChoice('ky_theme'))?.id ?? 'system';
}
let current = storedTheme();
// Every theme control on the page reads one store, so they never disagree.
const listeners = new Set<() => void>();
function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}
export const useTheme = () => useSyncExternalStore(subscribe, () => current);
const media = window.matchMedia?.('(prefers-color-scheme: dark)');
export function applyTheme(theme: string, persist = false) {
  const id = findOption(theme)?.id;
  if (!id) return;
  current = id;
  const resolved = id === 'system' ? (media?.matches ? 'busnes-dark' : 'busnes-light') : id;
  document.documentElement.dataset.theme = resolved;
  // Named palettes set their colors inline; Busnes clears them so the token aliases apply.
  document.documentElement.style.colorScheme = applyThemeVariables(resolved) ?? (resolved === 'busnes-dark' ? 'dark' : 'light');
  const legacy = LEGACY_THEMES.find(theme => theme.id === id);
  if (legacy) {
    for (const [key, property] of Object.entries(THEME_VARIABLES)) {
      if (legacy.palette[key]) document.documentElement.style.setProperty(property, legacy.palette[key]);
    }
    for (const status of ['danger', 'success', 'warning']) {
      document.documentElement.style.setProperty(`--${status}`, legacy.palette[status] ?? `var(--ky-${status}-${legacy.scheme})`);
    }
    document.documentElement.style.colorScheme = legacy.scheme;
  }
  if (persist) saveChoice('ky_theme', id);
  listeners.forEach(listener => listener());
}
applyTheme(current);
watchChoice('ky_theme', () => applyTheme(storedTheme()), () => current !== 'system');
