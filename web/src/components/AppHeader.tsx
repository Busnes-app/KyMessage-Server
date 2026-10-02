import React, { useState } from 'react';
import { Smartphone, LogOut, Users, Settings as SettingsIcon, LayoutDashboard, Archive, MessageSquare, Activity, ScrollText } from 'lucide-react';
import { ThemeSwitcher } from './ThemeSwitcher';
import { QRPairingModal } from './QRPairingModal';

interface AppHeaderProps {
  appName: string;
  activeTab: string;
  onTabChange: (tab: string) => void;
  user: any;
  onLogout: () => void;
}

const adminItems = [
  { id: 'dashboard', label: 'Overview', icon: LayoutDashboard },
  { id: 'users', label: 'Users', icon: MessageSquare, Activity, ScrollText },
  { id: 'health', label: 'Health', icon: Activity },
  { id: 'audit', label: 'Audit', icon: ScrollText },
  { id: 'scim', label: 'Directory & SCIM', icon: Users },
  { id: 'backup', label: 'Backup & recovery', icon: Archive },
  { id: 'settings', label: 'Settings & DB', icon: SettingsIcon },
];
// Admin pages are refused server-side for non-admins; navigation only mirrors that.
export function navItemsFor(role: string) {
  return role === 'admin' ? adminItems : [];
}

export const AppHeader: React.FC<AppHeaderProps> = ({ appName, activeTab, onTabChange, user, onLogout }) => {
  const [showPairing, setShowPairing] = useState<boolean>(false);

  const navItems = navItemsFor(user?.role ?? '');

  return (
    <>
      <header className="app-header">
        <div className="app-brand">
            <img src="/app-icon.png" width={28} height={28} alt="" />
            <span>{appName || 'KyMessages'}</span>
          </div>

          <nav className="app-nav" aria-label="Primary">
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
                  <Icon size={16} />
                  <span>{item.label}</span>
                </button>
              );
            })}
          </nav>
        <div className="app-header-actions">
          <button className="btn-secondary app-pair" onClick={() => setShowPairing(true)}>
            <Smartphone size={16} style={{ color: 'var(--accent)' }} />
            <span>Pair Device</span>
          </button>

          <ThemeSwitcher />

          {user && (
            <div className="app-user">
              <div className="app-user-copy">
                <div style={{ fontWeight: 600, color: 'var(--ink-strong)' }}>{user.display_name || user.username}</div>
                <div style={{ fontSize: '11px', color: 'var(--ink)' }}>{user.role}</div>
              </div>
              <button
                className="btn-secondary app-logout"
                onClick={onLogout}
                title="Sign out"
                aria-label="Sign out"
              >
                <LogOut size={16} />
              </button>
            </div>
          )}
        </div>
      </header>

      {showPairing && <QRPairingModal onClose={() => setShowPairing(false)} />}
    </>
  );
};
