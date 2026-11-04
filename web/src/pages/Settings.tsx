import React from 'react';
import { Database, UserRound } from 'lucide-react';
import { BrandingPanel } from '../components/BrandingPanel';
import { SyncPanel } from '../components/SyncPanel';
import { FederationPanel } from '../components/FederationPanel';
import { NotesPanel } from '../components/NotesPanel';
import { TwoFactorSetup } from '../components/TwoFactorSetup';
import { PasswordChange } from '../components/PasswordChange';
import type { SessionUser, SiteSettings } from '../dto';
import { PageHead } from '../components/PageHead';
import { Appearance } from './Appearance';
import { settingsSections, type SettingsSection } from '../route';

interface SettingsProps {
  section: SettingsSection;
  user: SessionUser;
  settings: SiteSettings | null;
  /** Re-reads /api/settings so the shell shows a new name. */
  onBrandingChanged: () => void;
}

const INTRO: Record<SettingsSection, string> = {
  account: 'Who you are signed in as, and how you sign in.',
  appearance: 'Pick a palette and how it handles light and dark. Match system switches with your device’s setting; Light or Dark keeps one. Your choice applies straight away and is saved in this browser only.',
  server: 'Options for this server and everyone on it.',
};

/** Settings: the page head, one tab per section, then the open section. */
export const Settings: React.FC<SettingsProps> = ({ section, user, settings, onBrandingChanged }) => {
  const sections = settingsSections(user.role);
  const open = sections.find((s) => s.id === section) ?? sections[0];
  return (
    <div>
      <PageHead title="Settings" lede={user.role === 'admin' ? 'Your account, how this console looks, and the options for this server.' : 'Your account, and how this console looks.'} />
      <nav className="ky-settings-tabs" aria-label="Settings sections">
        {sections.map((s) => (
          <a key={s.id} className="ky-nav-item" href={`#/settings/${s.id}`} aria-current={s === open ? 'page' : undefined}>{s.label}</a>
        ))}
      </nav>
      <section aria-label={open.label}>
        <p className="settings-intro">{INTRO[open.id]}</p>
        {open.id === 'account' && <Account user={user} />}
        {open.id === 'appearance' && <Appearance />}
        {open.id === 'server' && <Server settings={settings} onBrandingChanged={onBrandingChanged} />}
      </section>
    </div>
  );
};

function Account({ user }: { user: SessionUser }) {
  const role = user.role === 'admin' ? 'Administrator' : 'Everyday account';
  return (
    <>
      <div className="dr-settings">
        <section className="panel dr-section" aria-labelledby="account-who">
          <h3 id="account-who"><UserRound size={16} aria-hidden="true" /> Signed in as</h3>
          {/* Never the same word twice: a name that repeats the username or role is left out. */}
          {user.display_name && ![user.username, role].includes(user.display_name) && <p className="dr-pair"><span>Name</span><span>{user.display_name}</span></p>}
          <p className="dr-pair"><span>Username</span><span className="font-mono">{user.username}</span></p>
          <p className="dr-pair"><span>Role</span><span>{role}</span></p>
        </section>
      </div>
      <PasswordChange />
      {/* Two-factor sign-in is an administrator's: its routes refuse everyone else. */}
      {user.role === 'admin' && <TwoFactorSetup />}
    </>
  );
}

function Server({ settings, onBrandingChanged }: { settings: SiteSettings | null; onBrandingChanged: () => void }) {
  const driver = settings?.db_driver;
  return (
    <>
      {/* One flat surface: each section is divided by a rule, never boxed inside another. */}
      <div className="dr-settings">
        <section className="panel dr-section" aria-labelledby="settings-database">
          <h3 id="settings-database"><Database size={16} aria-hidden="true" /> Database</h3>
          <p className="dr-hint">The supported deployment is SQLite on a single instance.</p>
          <p className="dr-pair">
            <span>Active storage driver</span>
            <span className="badge font-mono">{driver ?? 'unknown'}</span>
          </p>
          {driver !== 'sqlite' && (
            <div className="dr-alert dr-alert-warn" role="alert">
              <span>
                {driver ? `This server runs on ${driver}.` : 'The database driver is unknown.'} Only SQLite is supported, and
                backup capsules can snapshot only SQLite: no capsule carries this database.
              </span>
            </div>
          )}
        </section>

        <BrandingPanel onChanged={onBrandingChanged} />
        <SyncPanel />
        <FederationPanel />
        <NotesPanel />
      </div>
    </>
  );
}
