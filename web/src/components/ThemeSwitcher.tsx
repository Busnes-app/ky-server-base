import { THEME_OPTIONS, applyTheme, useTheme } from '../theme';

export function ThemeSwitcher() {
  const theme = useTheme();
  return <select className="theme-selector" aria-label="Color theme" value={theme} onChange={event => applyTheme(event.target.value, true)}>
    {THEME_OPTIONS.map(option => <option key={option.id} value={option.id}>{option.name}</option>)}
  </select>;
}
