import React, { useCallback, useEffect, useState } from 'react';
import { Mic } from 'lucide-react';
import { adminFetch, errorMessage, isMatrixDisabled } from '../api';
import { bool, obj, str } from '../dto';

export interface NotesStatus { available: boolean; enabled: boolean; screens: boolean; user: string; checkin: string }

export function parseNotes(v: unknown): NotesStatus {
  const s = obj(v);
  return { available: bool(s.available), enabled: bool(s.enabled), screens: bool(s.screens), user: str(s.user, 255), checkin: str(s.checkin, 64) };
}

/** What the admin agrees to before turning meeting notes on. */
export const NOTES_WARNINGS = [
  "The bot joins a call only when the room's creator or an admin invites it, and leaves after that one meeting.",
  'It says out loud and in the chat that the meeting is recorded for note taking; anyone in the call can pause it or make it leave.',
  'It hears the meeting and holds the transcript in plaintext on this server until it posts the notes to the room. Speech is processed here; nothing leaves the server.',
  'English only.',
];

/** What the admin agrees to before allowing shared-screen snapshots. */
export const SCREENS_WARNINGS = [
  "Each meeting still starts without them: the room's creator or an admin turns them on in the call with !notes screens on, and the bot says so out loud and in the chat.",
  'The bot looks at shared screens only, never cameras, and says whose screen it is capturing.',
  'It posts still snapshots of what was shared with the notes, encrypted, in the room; they stay in the room like any shared image.',
];

const when = (t: string) => new Date(t).toLocaleString();

interface SwitchProps { on: boolean; busy: boolean; warnings: string[]; onChange: (on: boolean) => void }

/** Off is one click; on first shows the warnings with Turn on/Cancel (Cancel sends nothing). */
const Switch: React.FC<SwitchProps> = ({ on, busy, warnings, onChange }) => {
  const [confirming, setConfirming] = useState(false);
  if (on) return <button type="button" className="btn-secondary" disabled={busy} onClick={() => onChange(false)}>Turn off</button>;
  if (!confirming) return <button type="button" onClick={() => setConfirming(true)}>Turn on…</button>;
  return (
    <div className="dr-alert dr-alert-warn" role="alert">
      <ul>{warnings.map((w) => <li key={w}>{w}</li>)}</ul>
      <button type="button" disabled={busy} onClick={() => { setConfirming(false); onChange(true); }}>Turn on</button>{' '}
      <button type="button" className="btn-secondary" disabled={busy} onClick={() => setConfirming(false)}>Cancel</button>
    </div>
  );
};

/** Settings' "Meeting notes" panel; nothing at all when chat is not set up. */
export const NotesPanel: React.FC = () => {
  const [status, setStatus] = useState<NotesStatus | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (signal?: AbortSignal) => {
    const res = await fetch('/api/admin/notes', { signal, cache: 'no-store' });
    if (await isMatrixDisabled(res)) return;
    if (!res.ok) throw new Error(await errorMessage(res, 'Could not load meeting notes'));
    setStatus(parseNotes(await res.json()));
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal).catch((err: unknown) => {
      if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'Could not load meeting notes');
    });
    return () => controller.abort();
  }, [load]);

  const change = (path: string, what: string) => async (on: boolean) => {
    setBusy(true);
    setError('');
    try {
      const res = await adminFetch(`/api/admin/notes/${path}${on ? 'enable' : 'disable'}`, { method: 'POST' });
      if (!res.ok) throw new Error(await errorMessage(res, `Could not turn ${what} ${on ? 'on' : 'off'}`));
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not change meeting notes');
    } finally {
      setBusy(false);
    }
  };

  if (!status && !error) return null;
  return (
    <section className="panel dr-section" aria-label="Meeting notes">
      <div className="panel-header">
        <h3><Mic size={16} /> Meeting notes</h3>
        {status?.available && <span className={status.enabled ? 'badge badge-success' : 'badge'}>{status.enabled ? 'On' : 'Off'}</span>}
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert">{error}</div>}
      {status && !status.available && (
        <p className="dr-hint">Meeting notes are not set up. Run <code>matrix-init</code> again (it adds the bot's secret), add <code>docker-compose.notes.yml</code> to <code>COMPOSE_FILE</code> and restart the app.</p>
      )}
      {status?.available && (
        <>
          <p>A bot that takes notes in calls. Members invite <code>{status.user}</code> to a room before a meeting; it records only that meeting, then posts the notes in the room and leaves.</p>
          <div className="dr-facts">
            <div className="dr-fact">
              <div className="dr-fact-label">Bot</div>
              <div className="dr-fact-value">{status.checkin ? `Last checked in ${when(status.checkin)}` : 'Has not checked in: start the notes service (docker-compose.notes.yml).'}</div>
            </div>
          </div>
          <Switch on={status.enabled} busy={busy} warnings={NOTES_WARNINGS} onChange={change('', 'meeting notes')} />
          {status.enabled && (
            <div className="dr-section">
              <h4>Shared-screen snapshots <span className={status.screens ? 'badge badge-success' : 'badge'}>{status.screens ? 'Allowed' : 'Off'}</span></h4>
              <p>Lets the bot add still pictures of shared screens to the notes, when a meeting turns them on.</p>
              <Switch on={status.screens} busy={busy} warnings={SCREENS_WARNINGS} onChange={change('screens/', 'shared-screen snapshots')} />
            </div>
          )}
        </>
      )}
    </section>
  );
};
