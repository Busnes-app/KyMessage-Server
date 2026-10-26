import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { AppHeader, navItemsFor } from './AppHeader';

afterEach(cleanup);

it.each(['admin', 'user'])('keeps theme and sign-out controls without console device pairing for %s', (role) => {
  const onLogout = vi.fn();
  render(<AppHeader appName="KyMessages" activeTab="dashboard" onTabChange={() => {}} user={{ username: 'alice', role }} onLogout={onLogout} />);
  expect(screen.queryByRole('button', { name: /pair device/i })).toBeNull();
  expect(screen.queryByLabelText('Color theme')).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: 'Sign out' }));
  expect(onLogout).toHaveBeenCalledOnce();
});

it('member gets Home and Appearance, no admin navigation', () => {
  expect(navItemsFor('user').map(i => i.id)).toEqual(['dashboard', 'appearance']);
  expect(navItemsFor('manager').map(i => i.id)).toEqual(['dashboard', 'appearance']);
});
it('admin sees the admin pages', () => {
  expect(navItemsFor('admin').map(i => i.id)).toEqual(['dashboard', 'users', 'agents', 'rooms', 'health', 'audit', 'scim', 'backup', 'settings', 'appearance']);
});

it('names the account and its role once', () => {
  const { rerender } = render(<AppHeader appName="KyMessages" activeTab="dashboard" onTabChange={() => {}} user={{ username: 'admin', display_name: 'Administrator', role: 'admin' }} onLogout={() => {}} />);
  expect(screen.getAllByText('Administrator')).toHaveLength(1);
  rerender(<AppHeader appName="KyMessages" activeTab="dashboard" onTabChange={() => {}} user={{ username: 'alice', role: 'user' }} onLogout={() => {}} />);
  expect(screen.getByText('alice')).toBeTruthy();
  expect(screen.getByText('Everyday account')).toBeTruthy();
});
