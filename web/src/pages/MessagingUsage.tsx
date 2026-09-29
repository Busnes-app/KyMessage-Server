import { useEffect, useState } from 'react';

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}
function count(value: unknown): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) throw new Error('Invalid messaging usage response');
  return value;
}
function counts(value: unknown) {
  if (!isRecord(value)) throw new Error('Invalid messaging usage response');
  return { active_events: count(value.active_events), retained_bytes: count(value.retained_bytes), receipts: count(value.receipts) };
}
function usageResponse(value: unknown) {
  if (!isRecord(value) || !Array.isArray(value.rooms) || value.rooms.length > 100 ||
      typeof value.sampled_at !== 'string' || !Number.isFinite(Date.parse(value.sampled_at))) throw new Error('Invalid messaging usage response');
  const limits = counts(value.limits);
  if (!limits.active_events || !limits.retained_bytes || !limits.receipts) throw new Error('Invalid messaging usage limits');
  const rooms = value.rooms.map((room: unknown) => {
    if (!isRecord(room) || typeof room.id !== 'string' || !room.id || room.id.length > 128 ||
        typeof room.name !== 'string' || !room.name || room.name.length > 80 || typeof room.retired !== 'boolean') throw new Error('Invalid messaging room usage');
    return {id: room.id, name: room.name, retired: room.retired, ...counts(room)};
  });
  const roomCount = count(value.room_count);
  if (roomCount < rooms.length) throw new Error('Invalid messaging room count');
  return {roomCount, rooms, totals: counts(value.totals), limits, sampledAt: value.sampled_at};
}

type Usage = ReturnType<typeof usageResponse>;
type State = {kind:'loading'} | {kind:'ready'; value:Usage} | {kind:'error'; message:string};
const number = (value: number) => value.toLocaleString();
const bytes = (value: number) => value < 1024 ? `${number(value)} B` : value < 1024*1024 ? `${(value/1024).toFixed(1)} KiB` : `${(value/(1024*1024)).toFixed(1)} MiB`;
function capacity(room: Usage['rooms'][number], limits: Usage['limits']) {
  const ratio = Math.max(room.active_events/limits.active_events, room.retained_bytes/limits.retained_bytes, room.receipts/limits.receipts);
  return ratio >= 1 ? 'At a room limit' : ratio >= 0.8 ? 'Near a room limit' : 'Within room limits';
}

export function MessagingUsage() {
  const [state, setState] = useState<State>({kind:'loading'});
  const [revision, setRevision] = useState(0);
  const [expanded, setExpanded] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    setState({kind:'loading'});
    void (async () => {
      try {
        const response = await fetch('/api/admin/messaging/usage',{signal:controller.signal,cache:'no-store'});
        if (!response.ok) throw new Error('Messaging storage usage unavailable. Refresh or sign in again.');
        const raw: unknown = await response.json();
        const value = usageResponse(raw);
        if (!controller.signal.aborted) setState({kind:'ready',value});
      } catch (error) {
        if (!controller.signal.aborted) setState({kind:'error',message:error instanceof Error ? error.message : 'Messaging storage usage unavailable.'});
      }
    })();
    return () => controller.abort();
  },[revision]);
  return <section className="panel messaging-usage" aria-labelledby="messaging-storage-title" style={{marginTop:24}}>
    <div style={{display:'flex',flexWrap:'wrap',justifyContent:'space-between',gap:12,alignItems:'center'}}>
      <h2 id="messaging-storage-title" style={{fontSize:18}}>Messaging storage</h2>
      <button type="button" className="btn-secondary" disabled={state.kind === 'loading'} onClick={() => setRevision(value => value+1)}>Refresh storage usage</button>
    </div>
    <p>Server metadata only. Counts exclude database overhead, audit logs, backups and browser history.</p>
    {state.kind === 'loading' && <p role="status">Loading messaging storage…</p>}
    {state.kind === 'error' && <p role="alert">{state.message}</p>}
    {state.kind === 'ready' && <>
      <dl style={{display:'grid',gridTemplateColumns:'repeat(auto-fit,minmax(140px,1fr))',gap:16,margin:'20px 0'}}>
        <div><dt>Rooms</dt><dd style={{margin:0}}>{number(state.value.roomCount)}</dd></div>
        <div><dt>Stored ciphertext</dt><dd style={{margin:0}}>{bytes(state.value.totals.retained_bytes)}</dd></div>
        <div><dt>Retained encrypted events</dt><dd style={{margin:0}}>{number(state.value.totals.active_events)}</dd></div>
        <div><dt>Lifetime receipts</dt><dd style={{margin:0}}>{number(state.value.totals.receipts)}</dd></div>
      </dl>
      <p>Sampled <time dateTime={state.value.sampledAt}>{new Date(state.value.sampledAt).toLocaleString()}</time>. Includes retired rooms and data awaiting the next retention cleanup.</p>
      <details open={expanded} onToggle={event => setExpanded(event.currentTarget.open)}>
        <summary>Room limits and busiest rooms</summary>
        <p>Each room allows {number(state.value.limits.active_events)} retained events, {bytes(state.value.limits.retained_bytes)} stored ciphertext and {number(state.value.limits.receipts)} lifetime receipts. Events include messages and membership changes.</p>
        <p>Retention deletes events and their receipts. The receipts figure counts every event ever appended. A room at its receipt limit needs a new conversation; existing local history remains readable. A large next event can exceed the remaining byte allowance before the room is full.</p>
        {state.value.roomCount === 0 ? <p>No messaging rooms.</p> : <>
          <p>Showing {number(state.value.rooms.length)} of {number(state.value.roomCount)} rooms. Rooms at 80% of any limit come first, then rooms with the most stored ciphertext.</p>
          <ul style={{listStyle:'none',padding:0}}>
            {state.value.rooms.map(room => <li key={room.id} style={{borderTop:'1px solid var(--line)',padding:'12px 0',overflowWrap:'anywhere'}}>
              <strong>{room.name}</strong>{room.retired && ' — Retired'}
              <p>{capacity(room,state.value.limits)} · {number(room.active_events)} events · {bytes(room.retained_bytes)} · {number(room.receipts)} receipts</p>
              <small>Room ID: {room.id}</small>
            </li>)}
          </ul>
        </>}
      </details>
    </>}
  </section>;
}
