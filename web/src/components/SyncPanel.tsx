import React, { useEffect, useState } from 'react';
import { ExternalLink, RefreshCw } from 'lucide-react';
import { errorMessage, isMatrixDisabled } from '../api';
import { bool, count, iso, obj, str } from '../dto';

export interface SyncStatus {
  webhook: { at: string; kind: string } | null;
  rejected: { count: number; last_at: string | null; last_reason: string };
  sweep: { finished_at: string; ok: boolean; error: string; applied: number; failed: number; failing_since: string | null } | null;
  kyidentity_url: string;
}
export type SyncState = 'ok' | 'warning' | 'failing';

export const UNCERTAIN = 'A change made while KyMessages was down waits in KyIdentity as an uncertain write; resume it there.';
const REASON: Record<string, string> = {
  not_configured: 'secret not set', bad_signature: 'bad signature', stale: 'outside the 5-minute window',
  bad_headers: 'missing or malformed headers', malformed: 'signed, but not a usable user',
};
// Refusals that mean the secret is wrong or missing, not one odd delivery.
const SECRET_REASONS = new Set(['bad_signature', 'not_configured']);

const nullable = <T,>(v: unknown, read: (x: unknown) => T): T | null => (v === null ? null : read(v));
const when = (t: string) => new Date(t).toLocaleString();

export function parseSyncStatus(v: unknown): SyncStatus {
  const s = obj(v);
  const r = obj(s.rejected);
  return {
    webhook: nullable(s.webhook, (x) => {
      const w = obj(x);
      return { at: iso(w.at), kind: str(w.kind, 64) };
    }),
    rejected: { count: count(r.count), last_at: nullable(r.last_at, iso), last_reason: str(r.last_reason, 32) },
    sweep: nullable(s.sweep, (x) => {
      const w = obj(x);
      return {
        finished_at: iso(w.finished_at), ok: bool(w.ok), error: str(w.error, 512),
        applied: count(w.applied), failed: count(w.failed), failing_since: nullable(w.failing_since, iso),
      };
    }),
    kyidentity_url: str(s.kyidentity_url, 512),
  };
}

/** The last refusal's reason, unless a webhook was accepted after it (the fault is fixed). */
function openRejection(s: SyncStatus): string {
  const at = s.rejected.last_at;
  if (!at || (s.webhook && Date.parse(at) < Date.parse(s.webhook.at))) return '';
  return s.rejected.last_reason;
}

export function syncState(s: SyncStatus): SyncState {
  if (s.sweep && !s.sweep.ok) return 'failing';
  if (!s.webhook || !s.sweep || SECRET_REASONS.has(openRejection(s))) return 'warning';
  return 'ok';
}

/** What to fix, most urgent first; the last hint always applies. */
export function syncHints(s: SyncStatus): string[] {
  const hints: string[] = [];
  const reason = openRejection(s);
  if (s.sweep && !s.sweep.ok) hints.push(`The offboarding sweep has failed since ${when(s.sweep.failing_since ?? s.sweep.finished_at)}: ${s.sweep.error}`);
  if (!s.webhook) hints.push('No webhook has been accepted yet: check the suite_webhook system in KyIdentity and KY_KYIDENTITY_HMAC_SECRET.');
  if (reason === 'bad_signature') hints.push('Deliveries fail the signature check: the secret in KyIdentity and KY_KYIDENTITY_HMAC_SECRET differ.');
  if (reason === 'not_configured') hints.push('KY_KYIDENTITY_HMAC_SECRET is not set, or is shorter than 16 bytes.');
  if (reason === 'stale') hints.push("Deliveries arrive more than 5 minutes off this server's clock: check both clocks.");
  if (!s.sweep) hints.push('No offboarding sweep has finished yet.');
  hints.push(UNCERTAIN);
  return hints;
}

/** The Overview card: one line and a tone. */
export function syncCard(s: SyncStatus): { text: string; tone: 'success' | 'warning' | 'danger' } {
  switch (syncState(s)) {
    case 'failing':
      return { text: `Offboarding sweep failing since ${when(s.sweep?.failing_since ?? s.sweep?.finished_at ?? '')}`, tone: 'danger' };
    case 'warning':
      return { text: `Needs attention: ${syncHints(s)[0]}`, tone: 'warning' };
    default:
      return { text: `Last webhook ${s.webhook ? when(s.webhook.at) : ''}; last sweep ok`, tone: 'success' };
  }
}

const LABEL: Record<SyncState, string> = { ok: 'Working', warning: 'Needs attention', failing: 'Failing' };
const BADGE: Record<SyncState, string> = { ok: 'badge badge-success', warning: 'badge badge-accent', failing: 'badge badge-danger' };

/** Settings' "KyIdentity sync" panel; nothing at all when chat is not set up. */
export const SyncPanel: React.FC = () => {
  const [status, setStatus] = useState<SyncStatus | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    fetch('/api/admin/matrix/sync-status', { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (await isMatrixDisabled(res)) return;
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not load the sync status'));
        setStatus(parseSyncStatus(await res.json()));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'Could not load the sync status');
      });
    return () => controller.abort();
  }, []);
  if (!status && !error) return null;
  const state = status ? syncState(status) : null;
  return (
    <section className="panel dr-section" aria-label="KyIdentity sync">
      <div className="panel-header">
        <h3><RefreshCw size={16} /> KyIdentity sync</h3>
        {state && <span className={BADGE[state]}>{LABEL[state]}</span>}
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert">{error}</div>}
      {status && (
        <>
          <div className="dr-facts">
            <div className="dr-fact">
              <div className="dr-fact-label">Last accepted webhook</div>
              <div className="dr-fact-value">{status.webhook ? `${when(status.webhook.at)} (${status.webhook.kind})` : 'None yet'}</div>
            </div>
            <div className="dr-fact">
              <div className="dr-fact-label">Rejected since restart</div>
              <div className="dr-fact-value">
                {status.rejected.last_at
                  ? `${status.rejected.count}, last ${when(status.rejected.last_at)} (${REASON[status.rejected.last_reason] ?? status.rejected.last_reason})`
                  : String(status.rejected.count)}
              </div>
            </div>
            <div className="dr-fact">
              <div className="dr-fact-label">Last sweep</div>
              <div className="dr-fact-value">
                {status.sweep
                  ? `${when(status.sweep.finished_at)}: ${status.sweep.ok ? 'ok' : 'failed'}, ${status.sweep.applied} applied, ${status.sweep.failed} failed`
                  : 'None yet'}
              </div>
            </div>
          </div>
          {syncHints(status).map((h) => <p key={h} className="dr-hint">{h}</p>)}
          {status.kyidentity_url.startsWith('https://') && (
            <a href={status.kyidentity_url} target="_blank" rel="noopener noreferrer">Open KyIdentity <ExternalLink size={12} /></a>
          )}
        </>
      )}
    </section>
  );
};
