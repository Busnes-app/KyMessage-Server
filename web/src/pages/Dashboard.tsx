import React, { useEffect, useState } from 'react';
import { Activity, CircleAlert, CircleCheck, CircleDashed, CircleX, ExternalLink, Unplug } from 'lucide-react';
import { isMatrixDisabled, unreachable } from '../api';
import { parseHealth } from './Health';
import { parseUsersPage } from './Users';
import { backupAttempt } from './Backup';
import { parseSyncStatus, syncCard } from '../components/SyncPanel';
import { PageHead } from '../components/PageHead';

interface DashboardProps {
  settings: { app_name?: string } | null;
  user: { display_name?: string; username?: string } | null;
  onNavigate: (tab: string) => void;
}
interface Card { text: string; tone: 'success' | 'warning' | 'danger' | 'muted' }
const OUTCOME = { success: 'Succeeded', warning: 'Needs attention', failure: 'Failed', unknown: 'Outcome unavailable' } as const;
const checking: Card = { text: 'Checking…', tone: 'muted' };
const unavailable = (err: unknown) =>
  `Unavailable: ${unreachable(err) ? 'the server could not be reached' : err instanceof Error ? err.message : 'request failed'}`;
const TONE: Record<Card['tone'], string> = { success: 'dr-ok', warning: 'dr-warn', danger: 'dr-danger', muted: '' };
const ICON = { success: CircleCheck, warning: CircleAlert, danger: CircleX, muted: CircleDashed };
const SEVERITY: Record<Card['tone'], number> = { success: 0, muted: 1, warning: 2, danger: 3 };
// The Matrix side of /api/admin/health; everything else is the console itself.
const CHAT = new Set(['synapse', 'synapse-admin', 'mas', 'element', 'postgres']);
export const CHAT_SETUP_GUIDE = 'https://github.com/Busnes-app/KyMessage-Server/blob/master/docs/CHAT-SETUP.md';

interface Health { matrix: boolean; chat: Card; console: Card }
interface Row { id: string; title: string; card: Card; action: string; tab?: string; href?: string }

const upText = (list: { status: string; mismatch: boolean }[]) => {
  const down = list.filter((c) => c.status === 'down').length;
  const off = list.filter((c) => c.mismatch).length;
  return {
    text: `${list.length - down} of ${list.length} components up` + (off ? `, ${off} off their pinned version` : ''),
    tone: (down || off ? 'danger' : 'success') as Card['tone'],
  };
};

export const Dashboard: React.FC<DashboardProps> = ({ onNavigate }) => {
  const [health, setHealth] = useState<Health | { error: Card } | null>(null);
  const [users, setUsers] = useState<Card>(checking);
  const [backup, setBackup] = useState<Card>(checking);
  // null until known, and stays null without Matrix: no sync, no row.
  const [sync, setSync] = useState<Card | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    const get = (path: string) => fetch(path, { signal: controller.signal, cache: 'no-store' });
    const failed = (err: unknown): Card | null => (controller.signal.aborted ? null : { text: unavailable(err), tone: 'danger' });
    const settle = (set: (c: Card) => void, work: Promise<Card>) =>
      work.then(set, (err: unknown) => { const c = failed(err); if (c) set(c); });
    get('/api/admin/health').then(async (res) => {
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const { matrix, components } = parseHealth(await res.json());
      const chat = components.filter((c) => CHAT.has(c.name));
      const { text, tone } = upText(chat);
      setHealth({
        matrix,
        chat: !matrix ? { text: 'Not configured', tone: 'warning' }
          : tone === 'success' ? { text: `Connected · ${text}`, tone } : { text, tone },
        console: upText(components.filter((c) => !CHAT.has(c.name))),
      });
    }).catch((err: unknown) => { const c = failed(err); if (c) setHealth({ error: c }); });
    void settle(setUsers, get('/api/admin/matrix/users?limit=1').then(async (res): Promise<Card> => {
      if (await isMatrixDisabled(res)) return { text: 'Available once chat is set up', tone: 'muted' };
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
    get('/api/admin/matrix/sync-status')
      .then(async (res) => {
        if (await isMatrixDisabled(res)) return;
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        setSync(syncCard(parseSyncStatus(await res.json())));
      })
      .catch((err: unknown) => {
        // Unreached, the server never said Matrix is on: show no sync row rather than invent one.
        if (!controller.signal.aborted && !unreachable(err)) setSync({ text: unavailable(err), tone: 'danger' });
      });
    return () => controller.abort();
  }, []);

  const known = health && 'matrix' in health ? health : null;
  const notConfigured = known?.matrix === false;
  const chat = health ? ('error' in health ? health.error : health.chat) : checking;
  const rows: Row[] = [
    notConfigured
      ? { id: 'chat', title: 'Chat connection', card: chat, action: 'Set up chat', href: CHAT_SETUP_GUIDE }
      : { id: 'chat', title: 'Chat connection', card: chat, action: 'Open health', tab: 'health' },
    { id: 'users', title: 'Users', card: users, action: 'Open users', tab: 'users' },
    { id: 'backup', title: 'Backups', card: backup, action: 'Open backups', tab: 'backup' },
    ...(sync ? [{ id: 'sync', title: 'KyIdentity sync', card: sync, action: 'Open settings', tab: 'settings' }] : []),
  ];
  // One filled button, on the most severe row that needs something done.
  const worst = rows.reduce((a, b) => (SEVERITY[b.card.tone] > SEVERITY[a.card.tone] ? b : a));
  const primary = SEVERITY[worst.card.tone] >= SEVERITY.warning ? worst.id : null;
  const attention = rows.filter((r) => SEVERITY[r.card.tone] >= SEVERITY.warning).length;
  const lead = notConfigured
    ? { title: 'Chat is not configured', text: 'No Matrix server is connected yet, so there is nothing to manage.' }
    : !health ? { title: 'Checking readiness…', text: '' }
    : attention ? { title: `${attention} item${attention === 1 ? ' needs' : 's need'} attention`, text: '' }
    : { title: 'Chat is ready', text: '' };

  return (
    <div>
      <PageHead title="Overview" lede="The chat service at a glance; chat itself opens from your everyday account." />
      <section className="overview" aria-labelledby="overview-lead">
        <header className={notConfigured ? 'overview-lead overview-lead-setup' : 'overview-lead'}>
          {notConfigured && <span className="overview-lead-icon" aria-hidden="true"><Unplug size={20} /></span>}
          <div>
            <h2 id="overview-lead">{lead.title}</h2>
            {lead.text && <p>{lead.text}</p>}
          </div>
        </header>
        <ul className="overview-checklist" aria-label="Readiness">
          {rows.map(({ id, title, card, action, tab, href }) => {
            const State = ICON[card.tone];
            const cls = id === primary ? 'btn' : 'btn btn-secondary';
            return (
              <li key={id} className="overview-item">
                <section aria-label={title} className="overview-item-body">
                  <span className={`overview-state-icon ${TONE[card.tone]}`} aria-hidden="true"><State size={16} /></span>
                  <h3>{title}</h3>
                  <p role="status" className={TONE[card.tone]}>{card.text}</p>
                  {href
                    ? <a className={cls} href={href} target="_blank" rel="noopener noreferrer"><span>{action}</span><ExternalLink size={14} aria-hidden="true" /></a>
                    // Waiting on chat setup: a quiet link, so Set up chat stays the one call to action.
                    : <button type="button" className={id === primary ? undefined : notConfigured && id === 'users' ? 'overview-link' : 'btn-secondary'}
                      onClick={() => onNavigate(tab!)}>{action}</button>}
                </section>
              </li>
            );
          })}
        </ul>
      </section>
      {known && (
        <p className="overview-console">
          <Activity size={14} aria-hidden="true" />
          <span>Console health</span>
          <span role="status" className={TONE[known.console.tone]}>{known.console.text}</span>
        </p>
      )}
    </div>
  );
};
