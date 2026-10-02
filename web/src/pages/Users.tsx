import React, { useCallback, useEffect, useState } from 'react';
import { ExternalLink, Loader2, LogOut, Search, X } from 'lucide-react';
import { adminFetch, errorMessage, isMatrixDisabled } from '../api';
import { arr, count, iso, obj, oneOf, str } from '../dto';

const STATUSES = ['active', 'locked', 'deactivated', 'not_linked'] as const;
const KINDS = ['oauth2', 'compat', 'browser'] as const;
export interface MatrixUser { id: string; username: string; mxid: string; status: (typeof STATUSES)[number]; kyidentity: string }
export interface UsersPage { users: MatrixUser[]; total: number; offset: number; limit: number; directory_url: string }
export interface MatrixSession {
  kind: (typeof KINDS)[number]; id: string; device: string; client: string; ip: string; created_at: string; last_active_at: string | null;
}

export function parseUsersPage(v: unknown): UsersPage {
  const p = obj(v);
  return {
    users: arr(p.users).map((x) => {
      const u = obj(x);
      return { id: str(u.id, 64), username: str(u.username, 255), mxid: str(u.mxid, 512), status: oneOf(u.status, STATUSES), kyidentity: str(u.kyidentity, 64) };
    }),
    total: count(p.total), offset: count(p.offset), limit: count(p.limit), directory_url: str(p.directory_url),
  };
}

export function parseSessions(v: unknown): MatrixSession[] {
  return arr(obj(v).sessions).map((x) => {
    const s = obj(x);
    return {
      kind: oneOf(s.kind, KINDS), id: str(s.id, 64), device: str(s.device, 255), client: str(s.client, 512), ip: str(s.ip, 64),
      created_at: iso(s.created_at), last_active_at: s.last_active_at === null ? null : iso(s.last_active_at),
    };
  });
}

const STATUS_LABEL: Record<MatrixUser['status'], string> = { active: 'Active', locked: 'Locked', deactivated: 'Deactivated', not_linked: 'Not linked' };
const KIND_LABEL: Record<MatrixSession['kind'], string> = { oauth2: 'Matrix app', compat: 'Legacy login', browser: 'Account web' };
const PAGE = 50;
const errorText = (err: unknown, fallback: string) => (err instanceof Error && err.message ? err.message : fallback);
const when = (at: string | null) => (at ? new Date(at).toLocaleString() : '—');

const UserSessions: React.FC<{ user: MatrixUser; onClose: () => void }> = ({ user, onClose }) => {
  const [sessions, setSessions] = useState<MatrixSession[] | null>(null);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await fetch(`/api/admin/matrix/users/${encodeURIComponent(user.id)}/sessions`, { cache: 'no-store' });
      if (!res.ok) throw new Error(await errorMessage(res, 'Could not list sessions'));
      setSessions(parseSessions(await res.json()));
    } catch (err) {
      setError(errorText(err, 'Could not list sessions'));
    }
  }, [user.id]);
  useEffect(() => { void load(); }, [load]);

  // In the server's order: browser sessions first, so MAS cannot sign an app straight back in.
  const end = async (targets: MatrixSession[]) => {
    setBusy(true);
    setMessage('');
    setError('');
    const failures: string[] = [];
    let ended = 0;
    try {
      for (const s of targets) {
        const res = await adminFetch(`/api/admin/matrix/sessions/${s.kind}/${encodeURIComponent(s.id)}/finish`, { method: 'POST' });
        if (res.ok) { ended++; continue; }
        failures.push(await errorMessage(res, 'Could not end the session'));
        if (res.status === 403) break; // the admin did not confirm it's them
      }
    } catch (err) {
      failures.push(errorText(err, 'Could not end the session'));
    }
    if (ended) setMessage(`Ended ${ended} session${ended === 1 ? '' : 's'}.`);
    if (failures.length) setError(failures.join(' '));
    setBusy(false);
    await load();
  };

  return (
    <section className="panel dr-section" aria-labelledby="sessions-title">
      <div className="panel-header">
        <h3 id="sessions-title">Sessions for {user.mxid}</h3>
        <div className="dr-actions">
          <button type="button" className="btn-danger" disabled={busy || !sessions?.length} onClick={() => sessions && void end(sessions)}>
            {busy ? <Loader2 size={14} className="animate-spin" /> : <LogOut size={14} />}
            <span>End all sessions</span>
          </button>
          <button type="button" className="btn-secondary" onClick={onClose} aria-label="Close sessions"><X size={14} /></button>
        </div>
      </div>
      <p className="dr-hint">Ending a session signs that device out. The person can sign in again unless KyIdentity refuses them.</p>
      {message && <div className="dr-alert dr-alert-success" role="status"><span>{message}</span></div>}
      {error && <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>}
      {sessions === null && !error && <p role="status">Loading sessions…</p>}
      {sessions?.length === 0 && <p className="dr-hint">No active sessions.</p>}
      {sessions && sessions.length > 0 && (
        <div className="console-table-wrap">
          <table className="console-table">
            <thead>
              <tr><th scope="col">Kind</th><th scope="col">Client</th><th scope="col">Device</th><th scope="col">Last active</th><th scope="col">IP</th><th scope="col">Action</th></tr>
            </thead>
            <tbody>
              {sessions.map((s) => (
                <tr key={`${s.kind}/${s.id}`}>
                  <td>{KIND_LABEL[s.kind]}</td>
                  <td>{s.client || '—'}</td>
                  <td className="dr-mono">{s.device || '—'}</td>
                  <td>{when(s.last_active_at)}</td>
                  <td className="dr-mono">{s.ip || '—'}</td>
                  <td>
                    <button type="button" className="btn-secondary" disabled={busy} onClick={() => void end([s])}
                      aria-label={`End ${KIND_LABEL[s.kind]} session ${s.device || s.id}`}>End</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
};

export const Users: React.FC = () => {
  const [query, setQuery] = useState('');
  const [search, setSearch] = useState('');
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<UsersPage | null>(null);
  const [error, setError] = useState('');
  const [disabled, setDisabled] = useState(false);
  const [selected, setSelected] = useState<MatrixUser | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    const params = new URLSearchParams({ search, offset: String(offset), limit: String(PAGE) });
    fetch(`/api/admin/matrix/users?${params}`, { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (await isMatrixDisabled(res)) return setDisabled(true);
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not list Matrix users'));
        setPage(parseUsersPage(await res.json()));
        setError('');
      })
      .catch((err: unknown) => { if (!controller.signal.aborted) setError(errorText(err, 'Could not list Matrix users')); });
    return () => controller.abort();
  }, [search, offset]);

  if (disabled) {
    return (
      <div className="dr-page">
        <div className="dr-header"><h1>Users</h1></div>
        <p role="status">Chat (Matrix) is not set up on this server.</p>
      </div>
    );
  }
  const last = page ? Math.min(page.offset + page.users.length, page.total) : 0;
  return (
    <div className="dr-page">
      <div className="dr-header"><h1>Users</h1></div>
      <div className="dr-alert dr-alert-warn" role="note">
        <span>
          Access is controlled in KyIdentity, which the sync enforces: lock, unlock or remove people there.{' '}
          {page?.directory_url.startsWith('https://') && (
            <a href={page.directory_url} target="_blank" rel="noopener noreferrer">Open KyIdentity <ExternalLink size={12} /></a>
          )}
        </span>
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>}
      <form role="search" className="dr-row" onSubmit={(e) => { e.preventDefault(); setOffset(0); setSearch(query.trim()); }}>
        <label className="dr-field">
          Search by username
          <input type="search" value={query} maxLength={255} onChange={(e) => setQuery(e.target.value)} />
        </label>
        <button type="submit" className="btn-secondary"><Search size={14} /><span>Search</span></button>
      </form>
      {page && (
        <section className="panel dr-section" aria-label="Matrix users">
          <div className="console-table-wrap">
            <table className="console-table">
              <thead><tr><th scope="col">User</th><th scope="col">Status</th><th scope="col">KyIdentity</th><th scope="col">Sessions</th></tr></thead>
              <tbody>
                {page.users.map((u) => (
                  <tr key={u.id}>
                    <td className="dr-mono">{u.mxid}</td>
                    <td>{STATUS_LABEL[u.status]}</td>
                    <td>{u.kyidentity || 'Not linked'}</td>
                    <td>
                      <button type="button" className="btn-secondary" onClick={() => setSelected(u)} aria-label={`Sessions for ${u.username}`}>Sessions</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {page.users.length === 0 && <p className="dr-hint">No Matrix users{search ? ` match “${search}”` : ''}.</p>}
          <div className="dr-row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
            <span className="dr-hint">{page.total ? `${page.offset + 1}–${last} of ${page.total}` : ''}</span>
            <div className="dr-actions">
              <button type="button" className="btn-secondary" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Previous</button>
              <button type="button" className="btn-secondary" disabled={last >= page.total} onClick={() => setOffset(offset + PAGE)}>Next</button>
            </div>
          </div>
        </section>
      )}
      {selected && <UserSessions key={selected.id} user={selected} onClose={() => setSelected(null)} />}
    </div>
  );
};
