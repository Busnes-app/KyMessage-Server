import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { Settings } from './Settings';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

const renderWith = (db_driver?: string) => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'x', code: 'matrix_disabled' }), { status: 404 })));
  return render(<Settings settings={{ app_name: 'KyMessages', chat_url: '', sso_enabled: false, logo_custom: false, db_driver }} onBrandingChanged={vi.fn()} />);
};

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
