export type SettingsSection = 'account' | 'appearance' | 'server';

/** Settings tabs in order: the personal ones, then the server's for an administrator. */
export function settingsSections(role: string): { id: SettingsSection; label: string }[] {
  return [
    { id: 'account', label: 'Account' },
    { id: 'appearance', label: 'Appearance' },
    ...(role === 'admin' ? [{ id: 'server' as const, label: 'Server' }] : []),
  ];
}

// Admin pages are refused server-side for non-admins; routing only mirrors that.
const ADMIN_TABS = ['users', 'agents', 'rooms', 'health', 'audit', 'scim', 'backup'];

/** The view named by the URL hash (`#/users`, `#/settings/server`); anything unknown is the home page. */
export function tabFromHash(hash: string, role: string): string {
  let tab = hash.replace(/^#\/?/, '');
  // Appearance was a page of its own before Settings held it; the Settings row opens Account.
  if (tab === 'appearance') tab = 'settings/appearance';
  if (tab === 'settings') tab = 'settings/account';
  const ok = (role === 'admin' && ADMIN_TABS.includes(tab)) || settingsSections(role).some((s) => tab === `settings/${s.id}`);
  return ok ? tab : 'dashboard';
}

export const hashFor = (tab: string) => (tab === 'dashboard' ? '#/' : `#/${tab}`);
