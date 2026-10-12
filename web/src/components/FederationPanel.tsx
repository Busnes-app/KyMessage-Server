import React, { useCallback, useEffect, useState } from 'react';
import { Copy, Globe, RefreshCw, Trash2 } from 'lucide-react';
import { adminFetch, errorMessage, isMatrixDisabled } from '../api';
import { arr, bool, obj, oneOf, str } from '../dto';

const LEVELS = ['closed', 'manual', 'curated'] as const;
export type Level = (typeof LEVELS)[number];
const STATUSES = ['connected', 'not_allowed', 'federation_off', 'unreachable', 'unknown'] as const;
export type Status = (typeof STATUSES)[number];

export interface Preset { name: string; operator: string; open_signup: boolean; source: string; checked: string; ticked: boolean }
export interface Federation {
  level: Level;
  server_name: string;
  servers: string[];
  presets: Preset[];
  restart_hint: string;
  file_error: string;
}

export function parseFederation(v: unknown): Federation {
  const d = obj(v);
  return {
    level: oneOf(d.level, LEVELS),
    server_name: str(d.server_name, 253),
    servers: arr(d.servers).map((s) => str(obj(s).name, 253)),
    presets: arr(d.presets).map((p) => {
      const o = obj(p);
      return { name: str(o.name, 253), operator: str(o.operator, 200), open_signup: bool(o.open_signup), source: str(o.source, 2048), checked: str(o.checked, 32), ticked: bool(o.ticked) };
    }),
    restart_hint: str(d.restart_hint, 256),
    file_error: d.file_error === undefined ? '' : str(d.file_error),
  };
}

export function parseStatuses(v: unknown): Record<string, Status> {
  const out: Record<string, Status> = {};
  for (const [name, s] of Object.entries(obj(obj(v).statuses))) out[name] = oneOf(s, STATUSES);
  return out;
}

const LEVEL_TEXT: Record<Level, { label: string; help: string }> = {
  closed: { label: 'Closed', help: 'Only people on this server. No other server can reach it.' },
  manual: { label: 'Manual approval', help: 'Servers you add by name. Their operator must add this server too.' },
  curated: { label: 'Curated list + approval', help: 'As manual, plus public servers you tick from a vetted list.' },
};

/** What a wider level exposes; shown before it is chosen. */
export function wideningWarnings(to: Level): string[] {
  if (to === 'closed') return [];
  const w = [
    'Allowed servers see room names, topics, membership, display names and timestamps of rooms their users join.',
    'Chat stays “End-to-end encrypted in Element (not independently audited)”, but Synapse does not enforce encryption: a client that does not encrypt can post plaintext. Encryption across servers is not covered by our acceptance tests.',
    'Offboarding someone here does not remove what was already sent to other servers.',
    'Closing a room removes this server from it; other servers keep it. Deleting a room deletes only this server’s copy.',
    'People on allowed servers can join calls in rooms they share with your users; only your users start calls, and their media uses your bandwidth.',
  ];
  if (to === 'curated') {
    w.push('Rooms on public servers can include servers you never allowed, and what your members post there reaches all of them. Servers with open sign-up let strangers invite your members.');
  }
  return w;
}

const STATUS_TEXT: Record<Status, { label: string; badge: string; hint?: string }> = {
  connected: { label: 'Connected', badge: 'badge badge-success' },
  not_allowed: { label: 'Not allowed yet', badge: 'badge badge-accent', hint: 'Both servers must list each other, and Synapse must restart after a change.' },
  federation_off: { label: 'Their federation is off', badge: 'badge badge-accent', hint: 'That server does not accept federation.' },
  unreachable: { label: 'Unreachable', badge: 'badge badge-danger', hint: 'Check the name, and that the server publishes /.well-known/matrix/server on a public address.' },
  unknown: { label: 'Unknown', badge: 'badge' },
};

function StatusBadge({ status }: { status?: Status }) {
  if (!status) return <span className="badge">Not checked</span>;
  const s = STATUS_TEXT[status];
  return <span className={s.badge} title={s.hint}>{s.label}</span>;
}

/** Settings' "Federation" panel; nothing at all when chat is not set up. */
export const FederationPanel: React.FC = () => {
  const [state, setState] = useState<Federation | null>(null);
  const [statuses, setStatuses] = useState<Record<string, Status>>({});
  const [checking, setChecking] = useState(false);
  const [pending, setPending] = useState<Level | null>(null);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [changed, setChanged] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');

  const check = useCallback(async () => {
    setChecking(true);
    try {
      const res = await fetch('/api/admin/federation/status', { cache: 'no-store' });
      if (!res.ok) throw new Error(await errorMessage(res, 'Could not check the servers'));
      setStatuses(parseStatuses(await res.json()));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not check the servers');
    } finally {
      setChecking(false);
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    fetch('/api/admin/federation', { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (await isMatrixDisabled(res)) return;
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not load federation settings'));
        const f = parseFederation(await res.json());
        setState(f);
        if (f.level !== 'closed') void check();
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'Could not load federation settings');
      });
    return () => controller.abort();
  }, [check]);

  const change = async (path: string, init: RequestInit, done: string) => {
    setBusy(true);
    setMessage('');
    setError('');
    try {
      const res = await adminFetch(path, init);
      if (!res.ok) throw new Error(await errorMessage(res, 'The change was refused'));
      const f = parseFederation(await res.json());
      setState(f);
      setChanged(true);
      setMessage(done);
      setPending(null);
      return f;
    } catch (err) {
      setError(err instanceof Error ? err.message : 'The change was refused');
      return null;
    } finally {
      setBusy(false);
    }
  };
  const json = (method: string, body: unknown): RequestInit => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const setLevel = (level: Level) => void change('/api/admin/federation/level', json('PUT', { level }), `Federation set to ${LEVEL_TEXT[level].label}.`);
  const pick = (level: Level) => {
    if (!state || level === state.level) return;
    // Wider levels show what they expose first; narrowing applies at once.
    if (LEVELS.indexOf(level) > LEVELS.indexOf(state.level)) setPending(level);
    else setLevel(level);
  };
  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    if (await change('/api/admin/federation/servers', json('POST', { name }), `Added ${name.trim().toLowerCase()}.`)) setName('');
  };
  const copy = () => {
    if (!state) return;
    navigator.clipboard?.writeText(state.server_name).then(() => setMessage('Server name copied.'), () => setError('Copy failed: select the name and copy it.'));
  };

  if (!state && !error) return null;
  const open = state ? state.level !== 'closed' : false;
  return (
    <section className="panel dr-section" aria-label="Federation">
      <div className="panel-header">
        <h3><Globe size={16} /> Federation</h3>
        {state && <span className={open ? 'badge badge-accent' : 'badge'}>{LEVEL_TEXT[state.level].label}</span>}
      </div>
      <p className="dr-hint">
        Lets people here chat with people on other Matrix servers you allow. Both servers must add each other. Changes apply after Synapse restarts; widening needs a sign-in from the last 10 minutes.
      </p>
      {state && (
        <>
          <fieldset className="dr-fieldset" disabled={busy}>
            <legend>Who can reach this server</legend>
            {LEVELS.map((l) => (
              <label key={l} className="dr-choice">
                <input type="radio" name="federation-level" value={l} checked={(pending ?? state.level) === l} onChange={() => pick(l)} />
                <span><strong>{LEVEL_TEXT[l].label}</strong> — {LEVEL_TEXT[l].help}</span>
              </label>
            ))}
          </fieldset>
          {pending && (
            <div className="dr-alert dr-alert-warn" role="alert">
              <div>
                <strong>Before choosing {LEVEL_TEXT[pending].label}:</strong>
                <ul>{wideningWarnings(pending).map((w) => <li key={w}>{w}</li>)}</ul>
                <div className="dr-actions">
                  <button type="button" disabled={busy} onClick={() => setLevel(pending)}>Allow {LEVEL_TEXT[pending].label}</button>
                  <button type="button" className="btn-secondary" disabled={busy} onClick={() => setPending(null)}>Cancel</button>
                </div>
              </div>
            </div>
          )}
          <div className="dr-row">
            <span>Your server: <code className="dr-mono">{state.server_name}</code></span>
            <button type="button" className="btn-secondary" onClick={copy}><Copy size={14} /><span>Copy</span></button>
            <span className="dr-hint">Send this name to the other server’s operator.</span>
          </div>
          {open && (
            <>
              <form className="dr-row" onSubmit={(e) => void add(e)}>
                <label className="dr-field">
                  <span>Add a server</span>
                  <input value={name} placeholder="partner.example" autoComplete="off" spellCheck={false} onChange={(e) => setName(e.target.value)} />
                </label>
                <button type="submit" disabled={busy || !name.trim()}>Add server</button>
                <button type="button" className="btn-secondary" disabled={checking} onClick={() => void check()}>
                  <RefreshCw size={14} /><span>{checking ? 'Checking…' : 'Check connections'}</span>
                </button>
              </form>
              {state.servers.length === 0 ? (
                <p className="dr-hint">No servers added yet.</p>
              ) : (
                <ul className="dr-list" aria-label="Allowed servers">
                  {state.servers.map((s) => (
                    <li key={s} className="dr-row">
                      <code className="dr-mono">{s}</code>
                      <StatusBadge status={statuses[s]} />
                      {statuses[s] && STATUS_TEXT[statuses[s]].hint && <span className="dr-hint">{STATUS_TEXT[statuses[s]].hint}</span>}
                      <button type="button" className="btn-secondary" disabled={busy} aria-label={`Remove ${s}`}
                        onClick={() => void change(`/api/admin/federation/servers/${encodeURIComponent(s)}`, { method: 'DELETE' }, `Removed ${s}.`)}>
                        <Trash2 size={14} /><span>Remove</span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </>
          )}
          {state.level === 'curated' && (
            <fieldset className="dr-fieldset" disabled={busy}>
              <legend>Public servers</legend>
              <p className="dr-hint">Best for direct messages and small rooms with people on these servers.</p>
              {state.presets.length === 0 ? (
                <p className="dr-hint">No public servers are listed in this release yet.</p>
              ) : state.presets.map((p) => (
                <label key={p.name} className="dr-choice">
                  <input type="checkbox" checked={p.ticked}
                    onChange={(e) => void change(`/api/admin/federation/presets/${encodeURIComponent(p.name)}`, json('PUT', { ticked: e.target.checked }),
                      `${e.target.checked ? 'Allowed' : 'Removed'} ${p.name}.`)} />
                  <span>
                    <code className="dr-mono">{p.name}</code> — {p.operator}
                    {p.open_signup && <span className="badge badge-accent" title="Anyone can register there, so strangers can invite your members.">Open sign-up</span>}
                    {p.ticked && <StatusBadge status={statuses[p.name]} />}
                  </span>
                </label>
              ))}
            </fieldset>
          )}
          {changed && (
            <div className="dr-alert dr-alert-warn" role="status">
              <span>Restart Synapse to apply{state.restart_hint && <>: <code>{state.restart_hint}</code></>}. Calls follow the new list at once.</span>
            </div>
          )}
          {state.file_error && <div className="dr-alert dr-alert-error" role="alert">Synapse’s federation file was not updated: {state.file_error}</div>}
        </>
      )}
      {message && <div className="dr-alert dr-alert-success" role="status">{message}</div>}
      {error && <div className="dr-alert dr-alert-error" role="alert">{error}</div>}
    </section>
  );
};
