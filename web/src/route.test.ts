import { describe, expect, it } from 'vitest';
import { hashFor, settingsSections, tabFromHash } from './route';

describe('hash routes', () => {
  it('opens each view at its own hash', () => {
    expect(tabFromHash('#/users', 'admin')).toBe('users');
    expect(tabFromHash('#/settings/server', 'admin')).toBe('settings/server');
    expect(tabFromHash(hashFor('settings/appearance'), 'user')).toBe('settings/appearance');
  });

  it('sends the pages Settings replaced to their tab', () => {
    expect(tabFromHash('#/appearance', 'admin')).toBe('settings/appearance');
    expect(tabFromHash('#/appearance', 'user')).toBe('settings/appearance');
    expect(tabFromHash('#/settings', 'admin')).toBe('settings/account');
    expect(tabFromHash('#/settings', 'user')).toBe('settings/account');
  });

  it('keeps members off admin views and falls back to the home page', () => {
    expect(tabFromHash('#/users', 'user')).toBe('dashboard');
    expect(tabFromHash('#/settings/server', 'user')).toBe('dashboard');
    expect(tabFromHash('', 'admin')).toBe('dashboard');
    expect(tabFromHash('#/constructor', 'admin')).toBe('dashboard');
    expect(hashFor('dashboard')).toBe('#/');
  });

  it('orders the tabs Account, Appearance, then the server', () => {
    expect(settingsSections('admin').map((s) => s.label)).toEqual(['Account', 'Appearance', 'Server']);
    expect(settingsSections('user').map((s) => s.label)).toEqual(['Account', 'Appearance']);
  });
});
