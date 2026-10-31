import React, { useEffect, useRef } from 'react';
import { LicenseFooter } from './LicenseFooter';
import { AppRail } from './AppRail';
import type { SuiteApp } from '../dto';
import { LogOut, Users, Settings as SettingsIcon, LayoutDashboard, Archive, MessageSquare, Activity, ScrollText, Hash, Bot, House } from 'lucide-react';

interface AppHeaderProps {
  appName: string;
  appVersion?: string;
  /** An admin-uploaded logo is never inverted in dark schemes. */
  logoCustom?: boolean;
  activeTab: string;
  onTabChange: (tab: string) => void;
  user: any;
  onLogout: () => void;
  suiteApps?: SuiteApp[];
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
];
// Members land on their account page (tab `dashboard`); Settings holds Account and Appearance.
const memberItems = [
  { id: 'dashboard', label: 'Home', icon: House },
  { id: 'settings', label: 'Settings', icon: SettingsIcon },
];
// Admin pages are refused server-side for non-admins; navigation only mirrors that.
export function navItemsFor(role: string) {
  return role === 'admin' ? adminItems : memberItems;
}

// Icon only, named by aria-label; title shows the same word on hover.
const SignOut = ({ onLogout }: { onLogout: () => void }) => (
  <button type="button" className="ky-icon-button" aria-label="Sign out" title="Sign out" onClick={onLogout}>
    <LogOut aria-hidden="true" />
  </button>
);

export const AppHeader: React.FC<AppHeaderProps> = ({ appName, appVersion, logoCustom = true, activeTab, onTabChange, user, onLogout, suiteApps = [] }) => {
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
    <header className="ky-sidebar">
      <div className="ky-mobile-head">
        <div className="ky-brand">
          <img className={logoCustom ? 'ky-mark' : 'ky-mark ky-mark-stamp'} src="/app-icon.png" width={28} height={28} alt="" />
          <span>{appName || 'KyMessages'}</span>
        </div>
        {user && (
          <div className="ky-mobile-actions">
            <SignOut onLogout={onLogout} />
          </div>
        )}
      </div>

      <nav ref={navRef} className="ky-nav" aria-label="Primary">
        {navItems.map((item) => {
          const Icon = item.icon;
          // Every Settings tab keeps the one Settings row current.
          const active = activeTab === item.id || (item.id === 'settings' && activeTab.startsWith('settings/'));
          return (
            <button
              key={item.id}
              type="button"
              onClick={() => onTabChange(item.id)}
              className="ky-nav-item"
              aria-current={active ? 'page' : undefined}
            >
              <Icon aria-hidden="true" />
              <span>{item.label}</span>
            </button>
          );
        })}
      </nav>
      <div className="ky-sidebar-foot">
        <AppRail apps={suiteApps} />
        {user && (
          <div className="ky-user">
            <div className="ky-user-copy">
              <p className="ky-user-name">{name}</p>
              <p className="ky-user-role">{role}</p>
            </div>
            <div className="ky-user-actions">
              <SignOut onLogout={onLogout} />
            </div>
          </div>
        )}
        <LicenseFooter appVersion={appVersion} />
      </div>
    </header>
  );
};
