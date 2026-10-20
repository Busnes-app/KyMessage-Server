import React from 'react';
import { Settings as SettingsIcon, Database, Palette } from 'lucide-react';
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
}

export const Settings: React.FC<SettingsProps> = ({ settings, onBrandingChanged }) => {
  const driver = settings?.db_driver;
  return (
    <div style={{ maxWidth: '1080px', margin: '0 auto', padding: '32px 20px' }}>
      <div style={{ marginBottom: '24px' }}>
        <h1 style={{ fontSize: '24px', fontWeight: 'bold', display: 'flex', alignItems: 'center', gap: '10px' }}>
          <SettingsIcon size={24} style={{ color: 'var(--accent)' }} />
          <span>System Settings & Architecture</span>
        </h1>
        <p style={{ color: 'var(--ink)', fontSize: '14px', marginTop: '4px' }}>
          Database, color theme defaults, and instance configuration.
        </p>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 480px), 1fr))', gap: '20px' }}>
        {/* Pluggable Database Card */}
        <div className="panel">
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '12px' }}>
            <Database size={20} style={{ color: 'var(--accent)' }} />
            <h3 style={{ fontSize: '16px' }}>Database</h3>
          </div>
          <p style={{ color: 'var(--ink)', fontSize: '13px', marginBottom: '16px' }}>
            The supported deployment is SQLite on a single instance.
          </p>

          <div style={{ background: 'var(--bg)', border: '1px solid var(--line)', borderRadius: '6px', padding: '16px' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '13px' }}>
              <span style={{ color: 'var(--ink)' }}>Active storage driver:</span>
              <span className="badge badge-accent font-mono">{driver ?? 'unknown'}</span>
            </div>
          </div>
          {driver !== 'sqlite' && (
            <div className="dr-alert dr-alert-warn" role="alert" style={{ marginTop: '12px' }}>
              <span>
                {driver ? `This server runs on ${driver}.` : 'The database driver is unknown.'} Only SQLite is supported, and
                backup capsules can snapshot only SQLite: no capsule carries this database.
              </span>
            </div>
          )}
        </div>

        {/* Appearance */}
        <div className="panel">
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '12px' }}>
            <Palette size={20} style={{ color: 'var(--accent)' }} />
            <h3 style={{ fontSize: '16px' }}>Appearance</h3>
          </div>
          <p style={{ color: 'var(--ink)', fontSize: '13px', marginBottom: '16px' }}>
            Color tokens: Patina Ky (`#0d0f14`/`#4deeea`), Cyber, Nord, Paper, OLED with Space Grotesk and IBM Plex Mono fonts.
          </p>

          <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
            <span style={{ fontSize: '13px', color: 'var(--ink)' }}>Active Color Palette:</span>
            <ThemeSwitcher />
          </div>
        </div>
      </div>

      <div style={{ display: 'grid', gap: '20px', marginTop: '20px' }}>
        <BrandingPanel onChanged={onBrandingChanged} />
        <SyncPanel />
        <FederationPanel />
        <NotesPanel />
      </div>
    </div>
  );
};
