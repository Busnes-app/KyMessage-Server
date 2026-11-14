import React, { useEffect, useState } from 'react';
import { CalendarDays } from 'lucide-react';
import { errorMessage } from '../api';
import { bool, iso, obj, opt, str } from '../dto';
import { Status } from './Status';

export interface KyCalendarStatus { connected: boolean; source: string; url: string; reason: string; checked_at: string }

export function parseKyCalendar(v: unknown): KyCalendarStatus {
  const s = obj(v);
  const text = (v: unknown, max: number) => opt(v, (x) => str(x, max)) ?? '';
  return {
    connected: bool(s.connected), source: text(s.source, 32), url: text(s.url, 512), reason: text(s.reason, 32),
    checked_at: opt(s.checked_at, iso) ?? '',
  };
}

const SOURCE: Record<string, string> = { environment: 'KY_KYCALENDAR_URL', kyidentity: 'KyIdentity' };
export const REASON: Record<string, string> = {
  pending: 'Not looked up yet.',
  no_kyidentity: 'Suite sign-in is off, so KyIdentity cannot pair KyCalendar.',
  no_peer: 'KyIdentity has no KyCalendar paired with KyMessages.',
  unauthorized: "KyIdentity refused this console's client credentials.",
  unreachable: 'KyIdentity could not be reached; it is asked again with backoff.',
  invalid_peer: "KyIdentity's KyCalendar address is not an https origin.",
  no_credentials: 'The sign-in client secret could not be read.',
};

/** Settings' "KyCalendar" row: whether meetings from KyCalendar get rooms. Nothing without chat. */
export const KyCalendarPanel: React.FC = () => {
  const [status, setStatus] = useState<KyCalendarStatus | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    fetch('/api/admin/kycalendar', { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not load the KyCalendar status'));
        setStatus(parseKyCalendar(await res.json()));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'Could not load the KyCalendar status');
      });
    return () => controller.abort();
  }, []);
  if ((!status && !error) || status?.reason === 'matrix_off') return null;
  return (
    <section className="panel dr-section" aria-label="KyCalendar">
      <div className="panel-header">
        <h3><CalendarDays size={16} /> KyCalendar</h3>
        {status && <Status tone={status.connected ? 'success' : undefined}>{status.connected ? 'Connected' : 'Not connected'}</Status>}
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert">{error}</div>}
      {status && (
        <>
          <p className="dr-hint">KyCalendar meetings with attendees get an encrypted video room here. People outside the organisation cannot join yet.</p>
          <div className="dr-facts">
            {status.connected && (
              <>
                <div className="dr-fact"><div className="dr-fact-label">Paired by</div><div className="dr-fact-value">{SOURCE[status.source] ?? status.source}</div></div>
                <div className="dr-fact"><div className="dr-fact-label">Address</div><div className="dr-fact-value font-mono">{status.url}</div></div>
              </>
            )}
            {!status.connected && (
              <div className="dr-fact"><div className="dr-fact-label">Why</div><div className="dr-fact-value">{REASON[status.reason] ?? status.reason}</div></div>
            )}
            {status.checked_at && (
              <div className="dr-fact"><div className="dr-fact-label">Last checked</div><div className="dr-fact-value">{new Date(status.checked_at).toLocaleString()}</div></div>
            )}
          </div>
        </>
      )}
    </section>
  );
};
