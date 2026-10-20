import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { AppHeader, navItemsFor } from './AppHeader';

afterEach(cleanup);

it.each(['admin', 'user'])('keeps theme and sign-out controls without console device pairing for %s', (role) => {
  const onLogout = vi.fn();
  render(<AppHeader appName="KyMessages" activeTab="dashboard" onTabChange={() => {}} user={{ username: 'alice', role }} onLogout={onLogout} />);
  expect(screen.queryByRole('button', { name: /pair device/i })).toBeNull();
  expect(screen.getByLabelText('Color theme')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'Sign out' }));
  expect(onLogout).toHaveBeenCalledOnce();
});

it('member gets no admin navigation', () => {
  expect(navItemsFor('user')).toEqual([]);
  expect(navItemsFor('manager')).toEqual([]);
});
it('admin sees the admin pages', () => {
  expect(navItemsFor('admin').map(i => i.id)).toEqual(['dashboard', 'users', 'agents', 'rooms', 'health', 'audit', 'scim', 'backup', 'settings']);
});
