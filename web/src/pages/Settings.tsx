import React from 'react';
import { Database, Palette } from 'lucide-react';
import { ThemeSwitcher } from '../components/ThemeSwitcher';
import { BrandingPanel } from '../components/BrandingPanel';
import { SyncPanel } from '../components/SyncPanel';
import { FederationPanel } from '../components/FederationPanel';
import { NotesPanel } from '../components/NotesPanel';
import type { SiteSettings } from '../dto';

interface SettingsProps {
  settings: SiteSettings | null;
  /** Re-reads /api/settings so the shell shows a new name. */
  onBrandingChanged: () => void;
  /** Account security, shown after the instance settings. */
  children?: React.ReactNode;
}

export const Settings: React.FC<SettingsProps> = ({ settings, onBrandingChanged, children }) => {
  const driver = settings?.db_driver;
  return (
    <div className="dr-page">
      <div className="dr-header">
        <h1>System Settings & Architecture</h1>
        <p>Database, appearance and instance configuration.</p>
      </div>

      {/* One flat surface: each section is divided by a rule, never boxed inside another. */}
      <div className="dr-settings">
        <section className="panel dr-section" aria-labelledby="settings-database">
          <h3 id="settings-database"><Database size={16} aria-hidden="true" /> Database</h3>
          <p className="dr-hint">The supported deployment is SQLite on a single instance.</p>
          <p className="dr-pair">
            <span>Active storage driver</span>
            <span className="badge badge-accent font-mono">{driver ?? 'unknown'}</span>
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

        <section className="panel dr-section" aria-labelledby="settings-appearance">
          <h3 id="settings-appearance"><Palette size={16} aria-hidden="true" /> Appearance</h3>
          <p className="dr-hint">
            Busnes Light and Busnes Dark follow this device&rsquo;s setting until you choose a theme. The choice is kept in this browser only.
          </p>
          <ThemeSwitcher />
        </section>

        <BrandingPanel onChanged={onBrandingChanged} />
        <SyncPanel />
        <FederationPanel />
        <NotesPanel />
        {children}
      </div>
    </div>
  );
};
