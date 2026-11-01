import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { AppHeader, navItemsFor } from './AppHeader';

afterEach(cleanup);

it.each(['admin', 'user'])('keeps theme and sign-out controls without console device pairing for %s', (role) => {
  const onLogout = vi.fn();
  render(<AppHeader appName="KyMessages" activeTab="dashboard" onTabChange={() => {}} user={{ username: 'alice', role }} onLogout={onLogout} />);
  expect(screen.queryByRole('button', { name: /pair device/i })).toBeNull();
  expect(screen.queryByLabelText('Color theme')).toBeNull();
  // The sidebar row and the phone header's icon (CSS shows one at a time) both sign out.
  const signOuts = screen.getAllByRole('button', { name: 'Sign out' });
  expect(signOuts).toHaveLength(2);
  signOuts.forEach((button) => fireEvent.click(button));
  expect(onLogout).toHaveBeenCalledTimes(2);
});

it('member gets Home and Settings, no admin navigation', () => {
  expect(navItemsFor('user').map(i => i.id)).toEqual(['dashboard', 'settings']);
  expect(navItemsFor('manager').map(i => i.id)).toEqual(['dashboard', 'settings']);
});
it('admin sees the admin pages, then Settings; Appearance is a Settings tab', () => {
  expect(navItemsFor('admin').map(i => i.id)).toEqual(['dashboard', 'users', 'agents', 'rooms', 'health', 'audit', 'scim', 'backup', 'settings']);
});

it('signs out with icon-only buttons and keeps Settings current on every tab', () => {
  const { container } = render(<AppHeader appName="KyMessages" activeTab="settings/appearance" onTabChange={() => {}} user={{ username: 'alice', role: 'admin' }} onLogout={() => {}} />);
  for (const holder of ['.ky-user-actions', '.ky-mobile-actions']) {
    const button = container.querySelector(`${holder} > button.ky-icon-button`)!;
    expect(button.getAttribute('aria-label')).toBe('Sign out');
    expect(button.getAttribute('title')).toBe('Sign out');
    expect(button.textContent).toBe('');
    expect(button.querySelector('svg.lucide-log-out')).not.toBeNull();
  }
  expect(container.querySelector('[aria-label="Settings"]')).toBeNull();
  const current = container.querySelectorAll('.ky-nav [aria-current="page"]');
  expect([...current].map((e) => e.textContent)).toEqual(['Settings']);
});

it('names the account and its role once', () => {
  const { rerender } = render(<AppHeader appName="KyMessages" activeTab="dashboard" onTabChange={() => {}} user={{ username: 'admin', display_name: 'Administrator', role: 'admin' }} onLogout={() => {}} />);
  expect(screen.getAllByText('Administrator')).toHaveLength(1);
  rerender(<AppHeader appName="KyMessages" activeTab="dashboard" onTabChange={() => {}} user={{ username: 'alice', role: 'user' }} onLogout={() => {}} />);
  expect(screen.getByText('alice')).toBeTruthy();
  expect(screen.getByText('Everyday account')).toBeTruthy();
});
