import React, { useCallback, useEffect, useState } from 'react';
import { DoorOpen } from 'lucide-react';
import { adminFetch, errorMessage, isMatrixDisabled } from '../api';
import { bool, count, obj } from '../dto';
import { Status } from './Status';
import { Switch } from './NotesPanel';

export interface GuestsStatus { enabled: boolean; live: number }

export function parseGuests(v: unknown): GuestsStatus {
  const s = obj(v);
  return { enabled: bool(s.enabled), live: count(s.live) };
}

/** What the admin agrees to before letting people outside the organisation join meetings. */
export const GUESTS_WARNINGS = [
  'KyCalendar gives each outside attendee of a video meeting a personal link, in their own invitation only.',
  'The link works only from 10 minutes before each occurrence until it ends. The guest waits until someone in the meeting lets them in, and again after leaving the call.',
  'A guest is a temporary chat account for that one meeting: it cannot create rooms, invite, find people or message anyone outside the meeting, and it is removed when the meeting ends.',
  'Guests see and hear the meeting and read its chat from the moment they join. Turning this off removes every guest at once.',
];

/** Settings' "Meeting guests" panel, after KyCalendar's; nothing when chat is not set up. */
export const GuestsPanel: React.FC = () => {
  const [status, setStatus] = useState<GuestsStatus | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (signal?: AbortSignal) => {
    const res = await fetch('/api/admin/meeting-guests', { signal, cache: 'no-store' });
    if (await isMatrixDisabled(res)) return;
    if (!res.ok) throw new Error(await errorMessage(res, 'Could not load meeting guests'));
    setStatus(parseGuests(await res.json()));
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal).catch((err: unknown) => {
      if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'Could not load meeting guests');
    });
    return () => controller.abort();
  }, [load]);

  const change = async (on: boolean) => {
    setBusy(true);
    setError('');
    try {
      const res = await adminFetch(`/api/admin/meeting-guests/${on ? 'enable' : 'disable'}`, { method: 'POST' });
      if (!res.ok) throw new Error(await errorMessage(res, `Could not turn meeting guests ${on ? 'on' : 'off'}`));
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not change meeting guests');
    } finally {
      setBusy(false);
    }
  };

  if (!status && !error) return null;
  return (
    <section className="panel dr-section" aria-label="Meeting guests">
      <div className="panel-header">
        <h3><DoorOpen size={16} /> Meeting guests</h3>
        {status && <Status tone={status.enabled ? 'warning' : undefined}>{status.enabled ? 'On' : 'Off'}</Status>}
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert">{error}</div>}
      {status && (
        <>
          <p>Let people outside the organisation join KyCalendar video meetings, from a waiting room. Off by default.</p>
          {status.enabled && <p className="dr-hint">{status.live === 1 ? '1 guest is in a meeting now.' : `${status.live} guests are in meetings now.`}</p>}
          <Switch on={status.enabled} busy={busy} warnings={GUESTS_WARNINGS} onChange={(on) => void change(on)} />
        </>
      )}
    </section>
  );
};
