import { useEffect, useState } from 'react';
import { THEME_OPTIONS, applyTheme, storedTheme } from '../theme';

const MODE = { system: 'Follows device', light: 'Light', dark: 'Dark' };

export function Appearance() {
  const [theme, setTheme] = useState(storedTheme);
  useEffect(() => {
    const sync = () => setTheme(storedTheme());
    window.addEventListener('storage', sync);
    return () => window.removeEventListener('storage', sync);
  }, []);
  return (
    <div className="dr-page">
      <h1>Appearance</h1>
      <p className="ky-appearance-description">Pick a theme. It applies straight away and is saved in this browser only. Match system follows your device’s light or dark setting.</p>
      <fieldset className="ky-appearance">
        <legend className="ky-appearance-legend">Theme</legend>
        <div className="ky-appearance-grid">
          {THEME_OPTIONS.map((option) => (
            <label key={option.id} className="ky-appearance-option">
              <input className="ky-appearance-radio" type="radio" name="kymessages-theme" value={option.id} checked={theme === option.id}
                onChange={() => { setTheme(option.id); applyTheme(option.id, true); }} />
              <span className="ky-appearance-preview" data-ky-preview={option.id} aria-hidden="true" />
              <span className="ky-appearance-name">{option.name}</span>
              <small className="ky-appearance-mode">{MODE[option.scheme]}</small>
            </label>
          ))}
        </div>
      </fieldset>
    </div>
  );
}
