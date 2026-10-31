import React, { useEffect, useState } from 'react';
import { errorMessage, errorText } from '../api';
import { arr, count, iso, obj, str } from '../dto';
import { PageHead } from '../components/PageHead';

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

const KINDS = [['', 'All'], ['auth', 'Sign-in'], ['backup', 'Backup'], ['branding', 'Branding'], ['matrix', 'Matrix'], ['scim', 'SCIM']] as const;
const PAGE = 50;
const COLUMNS = ['When', 'Who', 'What', 'Target', 'Outcome', 'Details', 'IP'];
const bad = (outcome: string) => /^(error|failure|refused)/.test(outcome);

export const Audit: React.FC = () => {
  const [kind, setKind] = useState('');
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<AuditPage | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    const controller = new AbortController();
    const params = new URLSearchParams();
    if (kind) params.set('kind', kind);
    params.set('offset', String(offset));
    params.set('limit', String(PAGE));
    fetch(`/api/admin/audit?${params}`, { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not read the audit log'));
        setPage(parseAudit(await res.json()));
        setError('');
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorText(err, 'Could not read the audit log'));
      });
    return () => controller.abort();
  }, [kind, offset]);

  return (
    <div>
      <PageHead title="Audit" lede="Sign-in, backup, branding, chat and directory events, newest first. Read-only." />
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
            {/* Phones stack each record as labelled lines; explicit roles keep table semantics where display changes drop them (WebKit). */}
            <table className="console-table audit-table" role="table">
              <thead role="rowgroup">
                <tr role="row">{COLUMNS.map((c) => <th key={c} scope="col" role="columnheader">{c}</th>)}</tr>
              </thead>
              <tbody role="rowgroup">
                {page.records.map((r) => (
                  <tr key={r.id} role="row">
                    <td role="cell" data-label="When">{new Date(r.at).toLocaleString()}</td>
                    <td role="cell" data-label="Who">{r.actor}</td>
                    <td role="cell" data-label="What" className="dr-mono">{r.action}</td>
                    <td role="cell" data-label="Target" className="dr-mono">{r.target || '—'}</td>
                    <td role="cell" data-label="Outcome" className={bad(r.outcome) ? 'dr-danger' : ''}>{r.outcome || '—'}</td>
                    <td role="cell" data-label="Details" className="dr-mono dr-muted">{r.details || '—'}</td>
                    <td role="cell" data-label="IP" className="dr-mono">{r.ip || '—'}</td>
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
