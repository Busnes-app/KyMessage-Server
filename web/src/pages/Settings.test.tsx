import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { Settings } from './Settings';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

const admin = { username: 'admin', display_name: '', role: 'admin', must_change_password: false };
const renderWith = (db_driver?: string, section: 'account' | 'appearance' | 'server' = 'server', user = admin) => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'x', code: 'matrix_disabled' }), { status: 404 })));
  return render(<Settings section={section} user={user} settings={{ app_name: 'KyMessages', chat_url: '', sso_enabled: false, logo_custom: false, db_driver }} onBrandingChanged={vi.fn()} />);
};
const tabs = () => screen.getAllByRole('link').map((a) => [a.textContent, a.getAttribute('href'), a.getAttribute('aria-current')]);

it('warns on PostgreSQL instead of claiming support', () => {
  const { container } = renderWith('postgres');
  expect(screen.getByText('postgres')).toBeTruthy();
  expect(screen.getByText(/This server runs on postgres\. Only SQLite is supported/)).toBeTruthy();
  expect(container.textContent).not.toMatch(/clustering|Supported \(PostgreSQL/);
});

it('does not warn on SQLite', () => {
  renderWith('sqlite');
  expect(screen.getByText('sqlite')).toBeTruthy();
  expect(screen.queryByText(/Only SQLite is supported/)).toBeNull();
});

it('does not pretend an unknown driver is SQLite', () => {
  renderWith(undefined);
  expect(screen.getByText('unknown')).toBeTruthy();
  expect(screen.getByText(/The database driver is unknown/)).toBeTruthy();
});

it('titles the page as the navigation names it', () => {
  renderWith('sqlite');
  expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Settings');
});

it('tabs Account, Appearance, then Server for an administrator, each at its own hash', () => {
  renderWith('sqlite', 'account');
  expect(tabs()).toEqual([['Account', '#/settings/account', 'page'], ['Appearance', '#/settings/appearance', null], ['Server', '#/settings/server', null]]);
  expect(screen.getByText('Username').nextElementSibling?.textContent).toBe('admin');
  expect(screen.getByText('Administrator')).toBeTruthy();
  expect(screen.queryByText('Active storage driver')).toBeNull();
});

it('gives a member Account and Appearance only, with the Appearance description', () => {
  renderWith('sqlite', 'appearance', { username: 'bob', display_name: 'Bob', role: 'user', must_change_password: false });
  expect(tabs()).toEqual([['Account', '#/settings/account', null], ['Appearance', '#/settings/appearance', 'page']]);
  expect(screen.getByText(/Match system switches with your device’s setting; Light or Dark keeps one. Your choice applies straight away and is saved in this browser only./)).toBeTruthy();
  expect(screen.getAllByRole('radio')).toHaveLength(18);
});
