import React, { useEffect, useState } from 'react';
import { CalendarDays, Mail, type LucideIcon } from 'lucide-react';
import { errorMessage } from '../api';
import { bool, iso, obj, opt, str } from '../dto';
import { Status } from './Status';

export interface PairStatus { connected: boolean; source: string; url: string; reason: string; checked_at: string }

export function parsePair(v: unknown): PairStatus {
  const s = obj(v);
  const text = (v: unknown, max: number) => opt(v, (x) => str(x, max)) ?? '';
  return {
    connected: bool(s.connected), source: text(s.source, 32), url: text(s.url, 512), reason: text(s.reason, 32),
    checked_at: opt(s.checked_at, iso) ?? '',
  };
}

export const reasonText = (app: string, reason: string): string => ({
  pending: 'Not looked up yet.',
  no_kyidentity: `Suite sign-in is off, so KyIdentity cannot pair ${app}.`,
  no_peer: `KyIdentity has no ${app} paired with KyMessages.`,
  unauthorized: "KyIdentity refused this console's client credentials.",
  unreachable: 'KyIdentity could not be reached; it is asked again with backoff.',
  invalid_peer: `KyIdentity's ${app} address is not an https origin.`,
  no_credentials: 'The sign-in client secret could not be read.',
}[reason] ?? reason);

interface Props { app: string; route: string; env: string; icon: LucideIcon; hint: string }

/** One Settings row for a suite app paired with KyMessages. Nothing without chat. */
const SuitePairPanel: React.FC<Props> = ({ app, route, env, icon: Icon, hint }) => {
  const [status, setStatus] = useState<PairStatus | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    const failed = `Could not load the ${app} status`;
    fetch(route, { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (!res.ok) throw new Error(await errorMessage(res, failed));
        setStatus(parsePair(await res.json()));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : failed);
      });
    return () => controller.abort();
  }, [app, route]);
  if ((!status && !error) || status?.reason === 'matrix_off') return null;
  const source = status?.source === 'environment' ? env : status?.source === 'kyidentity' ? 'KyIdentity' : status?.source;
  return (
    <section className="panel dr-section" aria-label={app}>
      <div className="panel-header">
        <h3><Icon size={16} /> {app}</h3>
        {status && <Status tone={status.connected ? 'success' : undefined}>{status.connected ? 'Connected' : 'Not connected'}</Status>}
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert">{error}</div>}
      {status && (
        <>
          <p className="dr-hint">{hint}</p>
          <div className="dr-facts">
            {status.connected && (
              <>
                <div className="dr-fact"><div className="dr-fact-label">Paired by</div><div className="dr-fact-value">{source}</div></div>
                <div className="dr-fact"><div className="dr-fact-label">Address</div><div className="dr-fact-value font-mono">{status.url}</div></div>
              </>
            )}
            {!status.connected && (
              <div className="dr-fact"><div className="dr-fact-label">Why</div><div className="dr-fact-value">{reasonText(app, status.reason)}</div></div>
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

/** Whether KyCalendar meetings get rooms and attendees can open a chat. */
export const KyCalendarPanel: React.FC = () => (
  <SuitePairPanel app="KyCalendar" route="/api/admin/kycalendar" env="KY_KYCALENDAR_URL" icon={CalendarDays}
    hint="KyCalendar meetings with attendees get an encrypted video room here, and attendees can open a chat. People outside the organisation cannot join yet." />
);

/** Whether KyPost can open a chat with a sender or contact. */
export const KyPostPanel: React.FC = () => (
  <SuitePairPanel app="KyPost" route="/api/admin/kypost" env="KY_KYPOST_URL" icon={Mail}
    hint="KyPost can open a chat here with a sender or contact in the organisation." />
);
