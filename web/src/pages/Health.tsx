import React, { useCallback, useEffect, useState } from 'react';
import { ExternalLink, RefreshCw, XCircle } from 'lucide-react';
import { errorMessage, errorText } from '../api';
import { arr, bool, obj, oneOf, str } from '../dto';
import { NetworkCheck } from '../components/NetworkCheck';

export interface HealthComponent { name: string; status: 'up' | 'down'; version: string; pinned: string; mismatch: boolean; source: string; error: string }
export interface HealthReport { matrix: boolean; components: HealthComponent[] }

// Only the upstream repos the server links to; anything else is dropped.
const SOURCE = /^https:\/\/github\.com\/(element-hq|postgres)\/[A-Za-z0-9._-]+\/tree\/[A-Za-z0-9._-]+$/;

export function parseHealth(v: unknown): HealthReport {
  const h = obj(v);
  return {
    matrix: bool(h.matrix),
    components: arr(h.components).map((x) => {
      const c = obj(x);
      const source = str(c.source, 512);
      return {
        name: str(c.name, 64), status: oneOf(c.status, ['up', 'down'] as const), version: str(c.version, 64), pinned: str(c.pinned, 64),
        mismatch: bool(c.mismatch), source: SOURCE.test(source) ? source : '', error: str(c.error),
      };
    }),
  };
}

const LABEL: Record<string, string> = {
  'synapse-admin': 'Synapse admin access (console)',
  kymessages: 'KyMessages', database: 'KyMessages database', synapse: 'Synapse',
  mas: 'Matrix Authentication Service', element: 'Element Web', postgres: 'PostgreSQL (Matrix)',
};
const VERSIONLESS = new Set(['database', 'synapse-admin']);

export const Health: React.FC = () => {
  const [report, setReport] = useState<HealthReport | null>(null);
  const [error, setError] = useState('');
  const [checking, setChecking] = useState(true);
  const [round, setRound] = useState(0);
  const check = useCallback(async (signal?: AbortSignal) => {
    setChecking(true);
    try {
      const res = await fetch('/api/admin/health', { signal, cache: 'no-store' });
      if (!res.ok) throw new Error(await errorMessage(res, 'Health check failed'));
      setReport(parseHealth(await res.json()));
      setError('');
    } catch (err) {
      // A failed check leaves status unknown: never keep an older report on screen as if current.
      if (!signal?.aborted) { setReport(null); setError(errorText(err, 'Health check failed')); }
    } finally {
      if (!signal?.aborted) setChecking(false);
    }
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    void check(controller.signal);
    return () => controller.abort();
  }, [check]);

  return (
    <div className="dr-page">
      <div className="dr-header">
        <h1>Health</h1>
        <button type="button" className="btn-secondary" onClick={() => { setRound((r) => r + 1); void check(); }} disabled={checking}>
          <RefreshCw size={14} className={checking ? 'animate-spin' : ''} />
          <span>Check again</span>
        </button>
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert"><XCircle size={16} /><span>{error}</span></div>}
      {!report && !error && <p className="dr-hint" role="status">Checking components…</p>}
      {report && !report.matrix && <p className="dr-hint">Chat (Matrix) is not set up: only KyMessages and its database are checked.</p>}
      {report && (
        <section className="dr-facts" aria-label="Components">
          {report.components.map((c) => (
            <div key={c.name} className="dr-fact">
              <div className="dr-fact-label">
                <span>{LABEL[c.name] ?? c.name}</span>
                <span className={c.status === 'up' ? 'badge badge-success' : 'badge badge-danger'}>{c.status === 'up' ? 'Up' : 'Down'}</span>
              </div>
              <div className="dr-fact-value dr-mono">{c.version || (VERSIONLESS.has(c.name) ? '' : 'Version unknown')}</div>
              {c.mismatch && <div className="dr-fact-note dr-danger">Compose pins {c.pinned}; this is not the pinned version.</div>}
              {!c.mismatch && c.pinned && c.version && <div className="dr-fact-note">Pinned in Compose</div>}
              {c.source && (
                <a className="dr-fact-note" href={c.source} target="_blank" rel="noopener noreferrer">
                  Source for {c.version} <ExternalLink size={12} />
                </a>
              )}
              {c.error && <div className="dr-fact-note dr-danger">{c.error}</div>}
            </div>
          ))}
        </section>
      )}
      {/* Check again re-runs the network check too (remount), so its marks are never older than the page. */}
      <section className="panel dr-section"><NetworkCheck key={round} /></section>
    </div>
  );
};
