import React, { useCallback, useEffect, useState } from 'react';
import { Loader2, Lock, Search, Trash2, X } from 'lucide-react';
import { adminFetch, errorMessage, isMatrixDisabled } from '../api';
import { arr, bool, count, obj, oneOf, str } from '../dto';

const JOB_STATUSES = ['scheduled', 'active', 'complete', 'failed', 'cancelled', 'unknown'] as const;
const JOIN_RULES = ['public', 'invite', 'knock', 'restricted', 'knock_restricted', 'private', 'other', ''] as const;
export interface RoomRow {
  id: string; name: string; alias: string; creator: string; members: number; encrypted: boolean;
  public: boolean; join_rule: (typeof JOIN_RULES)[number]; state_events: number; closed: boolean;
}
export interface RoomsPage { rooms: RoomRow[]; total: number; offset: number; limit: number }
export interface RoomJob { delete_id: string; status: (typeof JOB_STATUSES)[number] }
export interface RoomDetail { room: RoomRow; members: string[]; members_total: number; confirm_text: string; jobs: RoomJob[] }

function parseRoom(v: unknown): RoomRow {
  const r = obj(v);
  return {
    id: str(r.id, 255), name: str(r.name, 255), alias: str(r.alias, 255), creator: str(r.creator, 255),
    members: count(r.members), encrypted: bool(r.encrypted), public: bool(r.public),
    join_rule: oneOf(r.join_rule, JOIN_RULES), state_events: count(r.state_events), closed: bool(r.closed),
  };
}
export function parseRoomsPage(v: unknown): RoomsPage {
  const p = obj(v);
  return { rooms: arr(p.rooms).map(parseRoom), total: count(p.total), offset: count(p.offset), limit: count(p.limit) };
}
export function parseJobs(v: unknown): RoomJob[] {
  return arr(v).map((x) => {
    const j = obj(x);
    return { delete_id: str(j.delete_id, 255), status: oneOf(j.status, JOB_STATUSES) };
  });
}
export function parseRoomDetail(v: unknown): RoomDetail {
  const d = obj(v);
  return {
    room: parseRoom(d.room), members: arr(d.members).map((m) => str(m, 255)), members_total: count(d.members_total),
    confirm_text: str(d.confirm_text, 255), jobs: parseJobs(d.jobs),
  };
}

const PAGE = 50;
// Synapse lists a job only once it has started; give up waiting after this many polls.
const MAX_POLLS = 90;
// A status read can fail in passing (a slow mint, a restart); give up after this many in a row.
const MAX_POLL_FAILURES = 3;
const RUNNING: readonly RoomJob['status'][] = ['scheduled', 'active'];
const errorText = (err: unknown, fallback: string) => (err instanceof Error && err.message ? err.message : fallback);
const roomPath = (id: string) => `/api/admin/matrix/rooms/${encodeURIComponent(id)}`;
const label = (r: RoomRow) => r.name || 'Unnamed room';
const access = (r: RoomRow) => (r.public ? 'Public' : r.join_rule === 'invite' ? 'Invite only' : r.join_rule || '—');

type Kind = 'close' | 'delete' | 'job';
const WORKING: Record<Kind, string> = { close: 'Closing…', delete: 'Deleting…', job: 'A close or delete of this room is running…' };
const DONE: Record<Kind, string> = {
  close: 'Room closed: everyone was removed and nobody can rejoin. Its history stays until you delete it.',
  delete: 'Room deleted permanently.',
  job: 'The close or delete that was running has finished.',
};

const RoomPanel: React.FC<{ id: string; pollMs: number; onClose: () => void; onDone: (message: string) => void }> = ({ id, pollMs, onClose, onDone }) => {
  const [detail, setDetail] = useState<RoomDetail | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [confirmClose, setConfirmClose] = useState(false);
  const [typed, setTyped] = useState('');
  const [pending, setPending] = useState<{ deleteId: string; kind: Kind } | null>(null);
  const [reads, setReads] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    fetch(roomPath(id), { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not read the room'));
        const d = parseRoomDetail(await res.json());
        setDetail(d);
        const run = d.jobs.find((j) => RUNNING.includes(j.status));
        if (run) { setPending({ deleteId: run.delete_id, kind: 'job' }); setError(''); }
      })
      .catch((err: unknown) => { if (!controller.signal.aborted) setError(errorText(err, 'Could not read the room')); });
    return () => controller.abort();
  }, [id, reads]);

  // Close and delete are Synapse background jobs: poll until this one ends.
  useEffect(() => {
    if (!pending) return;
    let stopped = false;
    let polls = 0;
    let failures = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      try {
        const res = await fetch(`${roomPath(id)}/delete-status`, { cache: 'no-store' });
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not read the job status'));
        const job = parseJobs(obj(await res.json()).jobs).find((j) => j.delete_id === pending.deleteId);
        if (stopped) return;
        failures = 0;
        if (job?.status === 'complete') { onDone(DONE[pending.kind]); return; }
        if (job && !RUNNING.includes(job.status)) {
          setPending(null);
          setError(`Synapse reports the job as ${job.status} and gives no reason; check Synapse's log.`);
          return;
        }
        if (++polls >= MAX_POLLS) {
          setPending(null);
          setError('Synapse has not finished yet. Open the room again later to see its state.');
          return;
        }
        timer = setTimeout(() => void tick(), pollMs);
      } catch (err) {
        if (stopped) return;
        if (++failures >= MAX_POLL_FAILURES) { setPending(null); setError(errorText(err, 'Could not read the job status')); return; }
        timer = setTimeout(() => void tick(), pollMs);
      }
    };
    timer = setTimeout(() => void tick(), pollMs);
    return () => { stopped = true; clearTimeout(timer); };
  }, [pending, id, pollMs, onDone]);

  const act = async (kind: 'close' | 'delete') => {
    setBusy(true);
    setError('');
    try {
      const init: RequestInit = kind === 'delete'
        ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ confirm: typed }) }
        : { method: 'POST' };
      const res = await adminFetch(`${roomPath(id)}/${kind}`, init);
      // Refused because a job runs: read the room again so that job is shown and followed.
      if (res.status === 409) setReads((n) => n + 1);
      if (!res.ok) throw new Error(await errorMessage(res, kind === 'close' ? 'Could not close the room' : 'Could not delete the room'));
      const b = obj(await res.json());
      if (oneOf(b.outcome, ['started', 'already_closed'] as const) === 'already_closed') { onDone('The room was already closed.'); return; }
      setConfirmClose(false);
      setPending({ deleteId: str(b.delete_id, 255), kind });
    } catch (err) {
      setError(errorText(err, 'The change failed'));
    } finally {
      setBusy(false);
    }
  };

  const room = detail?.room;
  const locked = busy || pending !== null;
  return (
    <section className="panel dr-section" aria-labelledby="room-title">
      <div className="panel-header">
        <h3 id="room-title">{room ? label(room) : 'Room'}</h3>
        <button type="button" className="btn-secondary" disabled={busy} onClick={onClose} aria-label="Close room details"><X size={14} /></button>
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>}
      {pending && <p role="status"><Loader2 size={14} className="animate-spin" /> {WORKING[pending.kind]}</p>}
      {!detail && !error && <p role="status">Loading room…</p>}
      {detail && room && (
        <>
          <p className="dr-hint dr-mono">{room.id}{room.alias ? ` · ${room.alias}` : ''}</p>
          <p className="dr-hint">
            {room.encrypted ? 'Encryption on' : 'Encryption off'} · {access(room)} · {room.closed ? 'Closed' : 'Open'} · {room.state_events} state events
          </p>
          <h4>Members ({detail.members_total})</h4>
          {detail.members.length === 0 && <p className="dr-hint">Nobody is in this room.</p>}
          <ul aria-label="Members" className="dr-mono">
            {detail.members.map((m) => <li key={m}>{m}</li>)}
          </ul>
          {detail.members_total > detail.members.length && <p className="dr-hint">Showing the first {detail.members.length}.</p>}
          <div className="dr-section">
            {!confirmClose ? (
              <button type="button" className="btn-secondary" disabled={locked} onClick={() => setConfirmClose(true)}>
                <Lock size={14} /><span>Close room…</span>
              </button>
            ) : (
              <div role="group" aria-label="Confirm close" className="dr-alert dr-alert-warn">
                <span>Everyone is removed and nobody can rejoin: a closed room cannot be reopened. Its history stays on the server until you delete the room.</span>
                <div className="dr-actions">
                  <button type="button" className="btn-danger" disabled={locked} onClick={() => void act('close')}>Close room</button>
                  <button type="button" className="btn-secondary" disabled={busy} onClick={() => setConfirmClose(false)}>Cancel</button>
                </div>
              </div>
            )}
          </div>
          <div className="dr-section">
            <label className="dr-field">
              <span>Type <code className="dr-mono">{detail.confirm_text}</code> to delete this room permanently</span>
              <input value={typed} maxLength={255} autoComplete="off" spellCheck={false} onChange={(e) => setTyped(e.target.value)} />
            </label>
            <p className="dr-hint">
              Purges the room's history from the server for everyone. The room's media (attachments, avatars) stays in the
              media store. This cannot be undone.
            </p>
            <button type="button" className="btn-danger" disabled={locked || typed !== detail.confirm_text} onClick={() => void act('delete')}>
              <Trash2 size={14} /><span>Delete permanently</span>
            </button>
          </div>
        </>
      )}
    </section>
  );
};

export const Rooms: React.FC<{ pollMs?: number }> = ({ pollMs = 2000 }) => {
  const [query, setQuery] = useState('');
  const [search, setSearch] = useState('');
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<RoomsPage | null>(null);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const [disabled, setDisabled] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    const params = new URLSearchParams({ search, offset: String(offset), limit: String(PAGE) });
    fetch(`/api/admin/matrix/rooms?${params}`, { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (await isMatrixDisabled(res)) return setDisabled(true);
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not list rooms'));
        setPage(parseRoomsPage(await res.json()));
        setError('');
      })
      .catch((err: unknown) => { if (!controller.signal.aborted) { setPage(null); setError(errorText(err, 'Could not list rooms')); } });
    return () => controller.abort();
  }, [search, offset, reload]);

  const done = useCallback((m: string) => { setSelected(null); setMessage(m); setReload((n) => n + 1); }, []);

  if (disabled) {
    return (
      <div className="dr-page">
        <div className="dr-header"><h1>Rooms</h1></div>
        <p role="status">Chat (Matrix) is not set up on this server.</p>
      </div>
    );
  }
  const last = page ? Math.min(page.offset + page.rooms.length, page.total) : 0;
  return (
    <div className="dr-page">
      <div className="dr-header"><h1>Rooms</h1></div>
      <p className="dr-hint">
        Room names and members are shown to admins only. Messages are never shown here. State events is the closest
        measure of a room's size Synapse reports.
      </p>
      {message && <div className="dr-alert dr-alert-success" role="status"><span>{message}</span></div>}
      {error && <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>}
      <form role="search" className="dr-row" onSubmit={(e) => { e.preventDefault(); setOffset(0); setSearch(query.trim()); }}>
        <label className="dr-field">
          Search by name or room ID
          <input type="search" value={query} maxLength={255} onChange={(e) => setQuery(e.target.value)} />
        </label>
        <button type="submit" className="btn-secondary"><Search size={14} /><span>Search</span></button>
      </form>
      {page && (
        <section className="panel dr-section" aria-label="Rooms">
          <div className="console-table-wrap">
            <table className="console-table">
              <thead>
                <tr>
                  <th scope="col">Room</th><th scope="col">Members</th><th scope="col">Encryption</th><th scope="col">Access</th>
                  <th scope="col">Creator</th><th scope="col">State events</th><th scope="col">Status</th><th scope="col">Details</th>
                </tr>
              </thead>
              <tbody>
                {page.rooms.map((r) => (
                  <tr key={r.id}>
                    <td><div>{label(r)}</div><div className="dr-mono dr-hint">{r.id}</div></td>
                    <td>{r.members}</td>
                    <td>{r.encrypted ? 'Encryption on' : 'Encryption off'}</td>
                    <td>{access(r)}</td>
                    <td className="dr-mono">{r.creator || '—'}</td>
                    <td>{r.state_events}</td>
                    <td>{r.closed ? 'Closed' : 'Open'}</td>
                    <td>
                      <button type="button" className="btn-secondary" onClick={() => { setMessage(''); setSelected(r.id); }}
                        aria-label={`Details for ${label(r)}`}>Details</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {page.rooms.length === 0 && <p className="dr-hint">No rooms{search ? ` match “${search}”` : ''}.</p>}
          <div className="dr-row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
            <span className="dr-hint">{page.total ? `${page.offset + 1}–${last} of ${page.total}` : ''}</span>
            <div className="dr-actions">
              <button type="button" className="btn-secondary" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Previous</button>
              <button type="button" className="btn-secondary" disabled={last >= page.total} onClick={() => setOffset(offset + PAGE)}>Next</button>
            </div>
          </div>
        </section>
      )}
      {selected && <RoomPanel key={selected} id={selected} pollMs={pollMs} onClose={() => setSelected(null)} onDone={done} />}
    </div>
  );
};
