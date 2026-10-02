import React, { useEffect, useState } from 'react';
import { Activity, Archive, ArrowRight, MessageSquare } from 'lucide-react';
import { isMatrixDisabled } from '../api';
import { parseHealth } from './Health';
import { parseUsersPage } from './Users';
import { backupAttempt } from './Backup';

interface DashboardProps {
  settings: { app_name?: string } | null;
  user: { display_name?: string; username?: string } | null;
  onNavigate: (tab: string) => void;
}
interface Card { text: string; tone: 'success' | 'danger' | 'muted' }
const OUTCOME = { success: 'Succeeded', warning: 'Needs attention', failure: 'Failed', unknown: 'Outcome unavailable' } as const;
const checking: Card = { text: 'Checking…', tone: 'muted' };

export const Dashboard: React.FC<DashboardProps> = ({ settings, user, onNavigate }) => {
  const [health, setHealth] = useState<Card>(checking);
  const [users, setUsers] = useState<Card>(checking);
  const [backup, setBackup] = useState<Card>(checking);

  useEffect(() => {
    const controller = new AbortController();
    const get = (path: string) => fetch(path, { signal: controller.signal, cache: 'no-store' });
    const settle = (set: (c: Card) => void, work: Promise<Card>) =>
      work.then(set, (err: unknown) => {
        if (!controller.signal.aborted) set({ text: `Unavailable: ${err instanceof Error ? err.message : 'request failed'}`, tone: 'danger' });
      });
    void settle(setHealth, get('/api/admin/health').then(async (res): Promise<Card> => {
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const { matrix, components } = parseHealth(await res.json());
      const down = components.filter((c) => c.status === 'down').length;
      const off = components.filter((c) => c.mismatch).length;
      const text = `${components.length - down} of ${components.length} components up` +
        (off ? `, ${off} off their pinned version` : '') + (matrix ? '' : ' (chat not set up)');
      return { text, tone: down || off ? 'danger' : 'success' };
    }));
    void settle(setUsers, get('/api/admin/matrix/users?limit=1').then(async (res): Promise<Card> => {
      if (await isMatrixDisabled(res)) return { text: 'Chat (Matrix) is not set up', tone: 'muted' };
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const { total } = parseUsersPage(await res.json());
      return { text: `${total} Matrix user${total === 1 ? '' : 's'}`, tone: 'muted' };
    }));
    void settle(setBackup, get('/api/backup/status').then(async (res): Promise<Card> => {
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const body: unknown = await res.json();
      const last = backupAttempt(typeof body === 'object' && body !== null ? (body as { last_run?: unknown }).last_run : undefined);
      if (!last) return { text: 'Last backup: none recorded', tone: 'muted' };
      return {
        text: `Last backup: ${OUTCOME[last.outcome]} ${new Date(last.recorded_at).toLocaleString()}`,
        tone: last.outcome === 'success' ? 'success' : last.outcome === 'unknown' ? 'muted' : 'danger',
      };
    }));
    return () => controller.abort();
  }, []);

  const cards = [
    { title: 'Chat health', card: health, Icon: Activity, tab: 'health', action: 'Open Health' },
    { title: 'Matrix users', card: users, Icon: MessageSquare, tab: 'users', action: 'Open Users' },
    { title: 'Backups', card: backup, Icon: Archive, tab: 'backup', action: 'Open Backup & recovery' },
  ];
  return (
    <div style={{ maxWidth: '1080px', margin: '0 auto', padding: '32px 20px' }}>
      <div style={{ marginBottom: '32px' }}>
        <h1 style={{ fontSize: '26px', fontWeight: 'bold', marginBottom: '6px' }}>Welcome, {user?.display_name || user?.username}!</h1>
        <p style={{ color: 'var(--ink)', fontSize: '15px' }}>{settings?.app_name || 'KyMessages'} operator console.</p>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 280px), 1fr))', gap: '20px' }}>
        {cards.map(({ title, card, Icon, tab, action }) => (
          <section key={tab} className="panel" aria-label={title} style={{ display: 'flex', flexDirection: 'column', justifyContent: 'space-between' }}>
            <div>
              <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '12px' }}>
                <div style={{ padding: '8px', background: 'var(--accent-soft)', borderRadius: '6px', color: 'var(--accent)' }}><Icon size={20} /></div>
                <h3 style={{ fontSize: '16px' }}>{title}</h3>
              </div>
              <p role="status" className={card.tone === 'danger' ? 'dr-danger' : card.tone === 'success' ? 'dr-ok' : ''} style={{ fontSize: '14px' }}>{card.text}</p>
            </div>
            <div style={{ marginTop: '20px', borderTop: '1px solid var(--line)', paddingTop: '12px' }}>
              <button type="button" className="btn-secondary" style={{ width: '100%', justifyContent: 'space-between', fontSize: '13px' }} onClick={() => onNavigate(tab)}>
                <span>{action}</span>
                <ArrowRight size={14} />
              </button>
            </div>
          </section>
        ))}
      </div>
    </div>
  );
};
