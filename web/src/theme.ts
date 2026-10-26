import { readChoice, saveChoice, watchChoice } from './ky-ui/theme';
import { SYSTEM_THEME, THEME_CATALOG, applyThemeVariables, findTheme } from './ky-ui/themes';
// Busnes.app defaults; explicit choices are local to this browser. Saved values are catalog IDs,
// which include the earlier patina, cyber, nord, paper and oled.
export const THEME_OPTIONS = [SYSTEM_THEME, ...THEME_CATALOG];
export function storedTheme(): string {
  return findTheme(readChoice('ky_theme'))?.id ?? 'system';
}
let current = storedTheme();
const media = window.matchMedia?.('(prefers-color-scheme: dark)');
export function applyTheme(theme: string, persist = false) {
  const option = findTheme(theme);
  if (!option) return;
  current = option.id;
  const resolved = current === 'system' ? (media?.matches ? 'busnes-dark' : 'busnes-light') : current;
  document.documentElement.dataset.theme = resolved;
  document.documentElement.style.colorScheme = applyThemeVariables(resolved) ?? (resolved === 'busnes-light' ? 'light' : 'dark');
  if (persist) saveChoice('ky_theme', current);
}
applyTheme(current);
watchChoice('ky_theme', () => applyTheme(storedTheme()), () => current !== 'system');
