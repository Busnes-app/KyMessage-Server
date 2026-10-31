import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
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
const mode = (pair: string, label: string) =>
  within(screen.getByRole('group', { name: pair })).getByRole('radio', { name: label }) as HTMLInputElement;

describe('Appearance', () => {
  beforeEach(() => { delete root.dataset.theme; delete root.dataset.scheme; root.removeAttribute('style'); });

  it('offers six pairs with Match system, Light and Dark in one radio group, Busnes Match system by default', async () => {
    await page(null);
    expect(screen.getByRole('heading', { level: 1, name: 'Appearance' })).toBeTruthy();
    expect(screen.getByRole('group', { name: 'Theme' }).querySelectorAll('[role="group"]')).toHaveLength(6);
    const radios = screen.getAllByRole('radio') as HTMLInputElement[];
    expect(radios).toHaveLength(18);
    expect(new Set(radios.map((r) => r.name)).size).toBe(1);
    expect(mode('Busnes', 'Match system').checked).toBe(true);
    expect(document.querySelector('.ky-appearance [style]')).toBeNull();
    expect(screen.getByText(/Match system switches with your device’s setting; Light or Dark keeps one. Your choice applies straight away and is saved in this browser only./)).toBeTruthy();
  });

  it('marks the selected mode with a check', async () => {
    await page('busnes-dark');
    const selected = mode('Busnes', 'Dark').closest('label')!;
    expect(selected.querySelector('svg')).not.toBeNull();
    expect(mode('Busnes', 'Light').closest('label')!.querySelector('svg')).toBeNull();
  });

  it('keeps an earlier saved ID: nord is Nord Dark', async () => {
    await page('nord');
    expect(mode('Nord', 'Dark').checked).toBe(true);
    expect(root.dataset.theme).toBe('nord');
    expect(root.dataset.scheme).toBe('dark');
    expect(root.style.getPropertyValue('--line-strong')).toBe('#5e81ac');
  });

  it('maps a saved alias to its half: base patina is Ky Dark', async () => {
    await page('patina');
    expect(mode('Ky', 'Dark').checked).toBe(true);
  });

  it('resets a removed palette to Busnes following the OS', async () => {
    await page('cyber');
    expect(mode('Busnes', 'Match system').checked).toBe(true);
  });

  it('saves a fixed half, a pair following the OS, then clears colours for Busnes', async () => {
    await page(null);
    fireEvent.click(mode('Ocean', 'Dark'));
    expect(localStorage.getItem('ky_theme')).toBe('ocean');
    expect(root.dataset.theme).toBe('ocean');
    expect(root.style.getPropertyValue('--bg')).toBe('#0f1b24');
    fireEvent.click(mode('Forest', 'Match system'));
    expect(localStorage.getItem('ky_theme')).toBe('forest-pair');
    fireEvent.click(mode('Busnes', 'Light'));
    expect(localStorage.getItem('ky_theme')).toBe('busnes-light');
    expect(root.dataset.theme).toBe('busnes-light');
    expect(root.style.getPropertyValue('--bg')).toBe('');
    expect(root.style.colorScheme).toBe('light');
  });
});
