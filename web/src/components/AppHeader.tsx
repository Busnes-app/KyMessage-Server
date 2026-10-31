import React, { useEffect, useRef } from 'react';
import { LicenseFooter } from './LicenseFooter';
import { LogOut, Users, Settings as SettingsIcon, LayoutDashboard, Archive, MessageSquare, Activity, ScrollText, Hash, Bot, Palette, House } from 'lucide-react';

interface AppHeaderProps {
  appName: string;
  appVersion?: string;
  /** An admin-uploaded logo is never inverted in dark schemes. */
  logoCustom?: boolean;
  activeTab: string;
  onTabChange: (tab: string) => void;
  user: any;
  onLogout: () => void;
}

const adminItems = [
  { id: 'dashboard', label: 'Overview', icon: LayoutDashboard },
  { id: 'users', label: 'Users', icon: MessageSquare },
  { id: 'agents', label: 'Agents', icon: Bot },
  { id: 'rooms', label: 'Rooms', icon: Hash },
  { id: 'health', label: 'Health', icon: Activity },
  { id: 'audit', label: 'Audit', icon: ScrollText },
  { id: 'scim', label: 'Directory & SCIM', icon: Users },
  { id: 'backup', label: 'Backup & recovery', icon: Archive },
  { id: 'settings', label: 'Settings', icon: SettingsIcon },
  { id: 'appearance', label: 'Appearance', icon: Palette },
];
// Members land on their account page (tab `dashboard`) and choose a theme under Appearance.
const memberItems = [
  { id: 'dashboard', label: 'Home', icon: House },
  { id: 'appearance', label: 'Appearance', icon: Palette },
];
// Admin pages are refused server-side for non-admins; navigation only mirrors that.
export function navItemsFor(role: string) {
  return role === 'admin' ? adminItems : memberItems;
}

export const AppHeader: React.FC<AppHeaderProps> = ({ appName, appVersion, logoCustom = true, activeTab, onTabChange, user, onLogout }) => {
  const navItems = navItemsFor(user?.role ?? '');
  const role = user?.role === 'admin' ? 'Administrator' : 'Everyday account';
  // A display name that repeats the role line says nothing; show the username instead.
  const name = user?.display_name && user.display_name !== role ? user.display_name : user?.username;
  const navRef = useRef<HTMLElement>(null);
  // Phones scroll the navigation sideways: keep the current page's row in view, also after Overview links.
  useEffect(() => {
    navRef.current?.querySelector('[aria-current="page"]')?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
  }, [activeTab]);

  return (
    <header className="app-header">
      <div className="app-brand">
          <img className={logoCustom ? 'ky-mark' : 'ky-mark ky-mark-stamp'} src="/app-icon.png" width={28} height={28} alt="" />
          <span>{appName || 'KyMessages'}</span>
        </div>

        <nav ref={navRef} className="app-nav" aria-label="Primary">
          {navItems.map((item) => {
            const Icon = item.icon;
            const active = activeTab === item.id;
            return (
              <button
                key={item.id}
                onClick={() => onTabChange(item.id)}
                className={active ? 'ky-nav-item active' : 'ky-nav-item'}
                aria-current={active ? 'page' : undefined}
              >
                <Icon size={16} aria-hidden="true" />
                <span>{item.label}</span>
              </button>
            );
          })}
        </nav>
      <div className="app-sidebar-foot">
        {user && (
          <div className="ky-user">
            <div className="ky-user-copy">
              <p className="ky-user-name">{name}</p>
              <p className="ky-user-role">{role}</p>
            </div>
            <button type="button" className="ky-user-signout" onClick={onLogout}>
              <LogOut size={16} aria-hidden="true" />Sign out
            </button>
          </div>
        )}
        <LicenseFooter appVersion={appVersion} />
      </div>
    </header>

  );
};
