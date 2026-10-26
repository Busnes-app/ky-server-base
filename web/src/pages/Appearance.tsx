import { THEME_OPTIONS, applyTheme, themeMode, useTheme } from '../theme';

export function Appearance() {
  const theme = useTheme();
  return (
    <div className="dr-page">
      <div className="dr-header"><h1>Appearance</h1></div>
      <p className="ky-appearance-description">Pick a theme. It applies straight away and is saved in this browser only. Match system follows your device’s light or dark setting.</p>
      <fieldset className="ky-appearance">
        <legend className="ky-appearance-legend">Theme</legend>
        <div className="ky-appearance-grid">
          {THEME_OPTIONS.map(option => (
            <label key={option.id} className="ky-appearance-option">
              <input className="ky-appearance-radio" type="radio" name="kybase-theme" value={option.id} checked={theme === option.id} onChange={() => applyTheme(option.id, true)} />
              <span className="ky-appearance-preview" aria-hidden="true" data-ky-preview={option.id} />
              <span className="ky-appearance-name">{option.name}</span>
              <small className="ky-appearance-mode">{themeMode(option.id)}</small>
            </label>
          ))}
        </div>
      </fieldset>
    </div>
  );
}
