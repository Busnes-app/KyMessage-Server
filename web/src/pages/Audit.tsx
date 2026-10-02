import React, { useEffect, useState } from 'react';
import { errorMessage } from '../api';
import { arr, count, iso, obj, str } from '../dto';

export interface AuditRecord { id: number; at: string; actor: string; action: string; target: string; outcome: string; details: string; ip: string }
export interface AuditPage { records: AuditRecord[]; total: number; offset: number; limit: number }

export function parseAudit(v: unknown): AuditPage {
  const p = obj(v);
  return {
    records: arr(p.records).map((x) => {
      const r = obj(x);
      return { id: count(r.id), at: iso(r.at), actor: str(r.actor, 255), action: str(r.action, 128), target: str(r.target),
        outcome: str(r.outcome), details: str(r.details, 4096), ip: str(r.ip, 64) };
    }),
    total: count(p.total), offset: count(p.offset), limit: count(p.limit),
  };
}

const KINDS = [['', 'All'], ['auth', 'Sign-in'], ['backup', 'Backup'], ['matrix', 'Matrix'], ['scim', 'SCIM']] as const;
const PAGE = 50;
const bad = (outcome: string) => /^(error|failure|refused)/.test(outcome);

export const Audit: React.FC = () => {
  const [kind, setKind] = useState('');
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<AuditPage | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    const controller = new AbortController();
    const params = new URLSearchParams({ kind, offset: String(offset), limit: String(PAGE) });
    fetch(`/api/admin/audit?${params}`, { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not read the audit log'));
        setPage(parseAudit(await res.json()));
        setError('');
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(err instanceof Error && err.message ? err.message : 'Could not read the audit log');
      });
    return () => controller.abort();
  }, [kind, offset]);

  return (
    <div className="dr-page">
      <div className="dr-header"><h1>Audit log</h1></div>
      <p className="dr-hint">Read-only. Newest first.</p>
      <div className="dr-row">
        <label className="dr-field dr-narrow" style={{ flex: '0 0 12rem' }}>
          Kind
          <select value={kind} onChange={(e) => { setKind(e.target.value); setOffset(0); }}>
            {KINDS.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </select>
        </label>
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>}
      {page && (
        <section className="panel dr-section" aria-label="Audit records">
          <div className="console-table-wrap">
            <table className="console-table">
              <thead>
                <tr><th scope="col">When</th><th scope="col">Who</th><th scope="col">What</th><th scope="col">Target</th><th scope="col">Outcome</th><th scope="col">Details</th><th scope="col">IP</th></tr>
              </thead>
              <tbody>
                {page.records.map((r) => (
                  <tr key={r.id}>
                    <td>{new Date(r.at).toLocaleString()}</td>
                    <td>{r.actor}</td>
                    <td className="dr-mono">{r.action}</td>
                    <td className="dr-mono">{r.target || '—'}</td>
                    <td className={bad(r.outcome) ? 'dr-danger' : ''}>{r.outcome || '—'}</td>
                    <td className="dr-mono" style={{ fontSize: '12px', color: 'var(--ink)' }}>{r.details || '—'}</td>
                    <td className="dr-mono">{r.ip || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {page.records.length === 0 && <p className="dr-hint">No audit records.</p>}
          <div className="dr-row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
            <span className="dr-hint">{page.total ? `${page.offset + 1}–${page.offset + page.records.length} of ${page.total}` : ''}</span>
            <div className="dr-actions">
              <button type="button" className="btn-secondary" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Newer</button>
              <button type="button" className="btn-secondary" disabled={page.offset + page.records.length >= page.total} onClick={() => setOffset(offset + PAGE)}>Older</button>
            </div>
          </div>
        </section>
      )}
    </div>
  );
};
