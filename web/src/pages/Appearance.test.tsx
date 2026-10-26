import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

afterEach(cleanup);

// theme.ts applies the saved choice on import, so each case imports it afresh.
async function page(saved: string | null) {
  vi.resetModules();
  localStorage.clear();
  if (saved !== null) localStorage.setItem('ky_theme', saved);
  const { Appearance } = await import('./Appearance');
  render(<Appearance />);
}
const root = document.documentElement;

describe('Appearance', () => {
  beforeEach(() => { delete root.dataset.theme; root.removeAttribute('style'); });

  it('offers Match system and every catalog theme as native radios, system checked by default', async () => {
    await page(null);
    const { THEME_CATALOG } = await import('../ky-ui/themes');
    expect(screen.getByRole('heading', { level: 1, name: 'Appearance' })).toBeTruthy();
    expect(screen.getAllByRole('radio')).toHaveLength(THEME_CATALOG.length + 1);
    expect((screen.getByRole('radio', { name: /^Match system/ }) as HTMLInputElement).checked).toBe(true);
    expect(document.querySelector('.ky-appearance [style]')).toBeNull();
  });

  it('keeps an earlier saved ID and applies its own palette', async () => {
    await page('nord');
    expect((screen.getByRole('radio', { name: /^Nord Slate/ }) as HTMLInputElement).checked).toBe(true);
    expect(root.dataset.theme).toBe('nord');
    expect(root.style.getPropertyValue('--line-strong')).toBe('#5e81ac');
    expect(localStorage.getItem('ky_theme')).toBe('nord');
  });

  it('saves a new named theme, then clears its colours for Busnes', async () => {
    await page(null);
    fireEvent.click(screen.getByRole('radio', { name: /^Ocean/ }));
    expect(localStorage.getItem('ky_theme')).toBe('ocean');
    expect(root.dataset.theme).toBe('ocean');
    expect(root.style.getPropertyValue('--bg')).toBe('#0f1b24');
    expect(root.style.getPropertyValue('--panel-hover')).not.toBe('');
    fireEvent.click(screen.getByRole('radio', { name: /^Busnes Light/ }));
    expect(localStorage.getItem('ky_theme')).toBe('busnes-light');
    expect(root.dataset.theme).toBe('busnes-light');
    expect(root.style.getPropertyValue('--bg')).toBe('');
    expect(root.style.colorScheme).toBe('light');
  });

  it('keeps base Patina distinct from suite Patina Ky', async () => {
    await page('patina');
    expect((screen.getByRole('radio', { name: /^Patina Ky \(base\)/ }) as HTMLInputElement).checked).toBe(true);
    expect(root.style.getPropertyValue('--ink-strong')).toBe('#f1f5f9');
  });
});
