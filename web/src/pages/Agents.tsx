import React, { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Bot, Copy, Download, KeyRound, Trash2, X } from 'lucide-react';
import { adminFetch, errorMessage, isMatrixDisabled } from '../api';
import { arr, bool, count, iso, obj, oneOf, opt, str } from '../dto';

const TOKEN_STATES = ['active', 'revoked', 'expired'] as const;
export interface AgentOwner { id: string; username: string; display_name: string; mxid: string; status: string }
export interface Agent {
  id: string; username: string; mxid: string; device_id: string; owner: AgentOwner; token_state: (typeof TOKEN_STATES)[number];
  locked: boolean; last_used_at: string | null; last_used_error: string; created_at: string;
}
export interface AgentList { agents: Agent[]; owners: AgentOwner[]; idle_days: number }
export interface AgentToken { agent: Agent; token: string; configs: { openclaw?: string; hermes?: string } }

function parseOwner(v: unknown): AgentOwner {
  const o = obj(v);
  return { id: str(o.id, 64), username: str(o.username, 255), display_name: str(o.display_name, 255), mxid: str(o.mxid, 512), status: str(o.status, 32) };
}
function parseAgent(v: unknown): Agent {
  const a = obj(v);
  return {
    id: str(a.id, 64), username: str(a.username, 64), mxid: str(a.mxid, 512), device_id: str(a.device_id, 64), owner: parseOwner(a.owner),
    token_state: oneOf(a.token_state, TOKEN_STATES), locked: bool(a.locked), last_used_at: opt(a.last_used_at, iso) ?? null,
    last_used_error: opt(a.last_used_error, str) ?? '', created_at: iso(a.created_at),
  };
}
export function parseAgents(v: unknown): AgentList {
  const d = obj(v);
  return { agents: arr(d.agents).map(parseAgent), owners: arr(d.owners).map(parseOwner), idle_days: count(d.idle_days) };
}
// A runtime whose block the server could not fill is left out; the token still shows.
export function parseAgentToken(v: unknown): AgentToken {
  const d = obj(v);
  const c = obj(d.configs);
  return { agent: parseAgent(d.agent), token: str(d.token, 4096), configs: { openclaw: opt(c.openclaw, (x) => str(x, 8192)), hermes: opt(c.hermes, (x) => str(x, 8192)) } };
}

const DAY = 24 * 60 * 60 * 1000;
/** Days left before an unused token is revoked, once 14 or fewer remain; null before that. */
export function expiresInDays(since: string, idleDays: number, now = Date.now()): number | null {
  const idle = Math.floor((now - Date.parse(since)) / DAY);
  return idle < idleDays - 14 ? null : Math.max(0, idleDays - idle);
}

export const NAME = /^agent-[a-z0-9-]{1,32}$/;
export const AGENT_WARNINGS = [
  'The agent reads every message in rooms it joins and sends them to its model. If that model is hosted, room content leaves your infrastructure. The room keeps Element’s encryption label, but the agent’s machine and model see plaintext.',
  'The token gives full chat access as the agent. OpenClaw and Hermes store it on disk; Hermes in plaintext. A token in use does not expire; one unused for 90 days is revoked. Rotate on any doubt; revoke from here.',
  'Anyone in a room with the agent can try to steer it. Keep its tools as limited as you would trust every room member with.',
  'With federation on, users of allowed servers can invite and message the agent; the config blocks make it join and answer only for its owner.',
  'An agent can change its display name to anything; its Matrix ID always starts with @agent-. Offboarding the owner locks the agent within 2 minutes.',
];

const STATE_BADGE: Record<Agent['token_state'], { label: string; badge: string }> = {
  active: { label: 'Active', badge: 'badge badge-success' },
  revoked: { label: 'Revoked', badge: 'badge' },
  expired: { label: 'Expired', badge: 'badge badge-danger' },
};
const SKILL = '/api/admin/agents/skill';
const errorText = (err: unknown, fallback: string) => (err instanceof Error && err.message ? err.message : fallback);
const agentPath = (id: string) => `/api/admin/agents/${encodeURIComponent(id)}`;
const ownerName = (o: AgentOwner) => o.display_name || o.username || 'Unknown member';

function LastUsed({ agent, idleDays }: { agent: Agent; idleDays: number }) {
  if (agent.last_used_error) return <><div>unknown</div><div className="dr-hint">{agent.last_used_error}</div></>;
  const left = agent.token_state === 'active' ? expiresInDays(agent.last_used_at ?? agent.created_at, idleDays) : null;
  return (
    <>
      <div>{agent.last_used_at ? new Date(agent.last_used_at).toLocaleString() : 'never'}</div>
      {left !== null && <span className="badge badge-accent">{left === 0 ? 'Expires today unless used' : `Expires in ${left} day${left === 1 ? '' : 's'} unless used`}</span>}
    </>
  );
}

function CopyBlock({ label, text, onCopied }: { label: string; text: string; onCopied: (m: string) => void }) {
  const copy = () => {
    navigator.clipboard?.writeText(text).then(() => onCopied(`${label} copied.`), () => onCopied(`Copy failed: select the ${label.toLowerCase()} and copy it.`));
  };
  return (
    <div className="dr-section">
      <div className="dr-row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
        <h4>{label}</h4>
        <button type="button" className="btn-secondary" onClick={copy} aria-label={`Copy ${label}`}><Copy size={14} /><span>Copy</span></button>
      </div>
      <pre className="dr-pre"><code>{text}</code></pre>
    </div>
  );
}

/** Shows a new token once; the parent drops it from state on close. */
const TokenDialog: React.FC<{ issued: AgentToken; onClose: () => void }> = ({ issued, onClose }) => {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const [copied, setCopied] = useState('');
  useLayoutEffect(() => {
    const dialog = dialogRef.current;
    dialog?.showModal();
    return () => dialog?.close();
  }, []);
  const { agent, token, configs } = issued;
  return (
    <dialog ref={dialogRef} className="modal-window" aria-labelledby="token-title" onCancel={onClose} style={{ maxWidth: 'min(760px, calc(100vw - 32px))' }}>
      <div className="dr-row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
        <h3 id="token-title" style={{ fontSize: '18px' }}>Token for {agent.mxid}</h3>
        <button type="button" className="btn-secondary" onClick={onClose} aria-label="Close token"><X size={18} /></button>
      </div>
      <div className="dr-alert dr-alert-warn" role="note">
        <span>This token is shown once. Copy it into the agent’s config now; nothing here can show it again. Lost it? Rotate the token.</span>
      </div>
      <CopyBlock label="Token" text={token} onCopied={setCopied} />
      {configs.openclaw && <CopyBlock label="OpenClaw config" text={configs.openclaw} onCopied={setCopied} />}
      {configs.hermes && <CopyBlock label="Hermes config" text={configs.hermes} onCopied={setCopied} />}
      {(!configs.openclaw || !configs.hermes) && <p className="dr-hint">A config block could not be filled in; set the values from the skill’s references by hand.</p>}
      {copied && <p role="status" className="dr-hint">{copied}</p>}
      <p><a href={SKILL} download><Download size={14} /> Download skill</a> (unzips to a <code>kymessages/</code> folder)</p>
      <h4>OpenClaw</h4>
      <ol>
        <li>Unzip the skill into <code>~/.openclaw/skills/</code>, giving <code>~/.openclaw/skills/kymessages/</code>.</li>
        <li>Merge the OpenClaw block into your OpenClaw config (it sets <code>channels.matrix</code>), then run <code>openclaw gateway restart</code>.</li>
        <li>Run <code>openclaw matrix encryption setup</code> once; keep its recovery key outside the config.</li>
      </ol>
      <h4>Hermes Agent</h4>
      <ol>
        <li>Unzip the skill into <code>~/.hermes/skills/</code>, giving <code>~/.hermes/skills/kymessages/</code>.</li>
        <li>Add the Hermes block to <code>~/.hermes/.env</code> and run <code>chmod 600 ~/.hermes/.env</code>: it holds the token in plain text.</li>
        <li>First start: set <code>MATRIX_RECOVERY_KEY_OUTPUT_FILE</code> to a path; Hermes writes a recovery key there once. Then set <code>MATRIX_RECOVERY_KEY</code> to that key and remove the output-file line.</li>
      </ol>
      <div className="dr-actions" style={{ justifyContent: 'flex-end' }}>
        <button type="button" onClick={onClose}>Done</button>
      </div>
    </dialog>
  );
};

export const Agents: React.FC = () => {
  const [list, setList] = useState<AgentList | null>(null);
  const [disabled, setDisabled] = useState(false);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const [reload, setReload] = useState(0);
  const [busy, setBusy] = useState(false);
  const [name, setName] = useState('agent-');
  const [owner, setOwner] = useState('');
  const [confirming, setConfirming] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<Agent | null>(null);
  const [issued, setIssued] = useState<AgentToken | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    fetch('/api/admin/agents', { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (await isMatrixDisabled(res)) return setDisabled(true);
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not list agents'));
        setList(parseAgents(await res.json()));
      })
      .catch((err: unknown) => { if (!controller.signal.aborted) { setList(null); setError(errorText(err, 'Could not list agents')); } });
    return () => controller.abort();
  }, [reload]);

  // done runs on success; a token reply opens the token dialog.
  const act = async (path: string, init: RequestInit, fallback: string, done: (body: unknown) => void) => {
    setBusy(true);
    setMessage('');
    setError('');
    try {
      const res = await adminFetch(path, init);
      if (!res.ok) throw new Error(await errorMessage(res, fallback));
      done(await res.json());
    } catch (err) {
      setError(errorText(err, fallback));
    } finally {
      setBusy(false);
      setReload((n) => n + 1);
    }
  };
  const create = () => void act('/api/admin/agents',
    { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, owner }) }, 'Could not create the agent',
    (b) => { setIssued(parseAgentToken(b)); setConfirming(false); setName('agent-'); setOwner(''); });
  const rotate = (a: Agent) => void act(`${agentPath(a.id)}/rotate`, { method: 'POST' }, 'Could not rotate the token', (b) => setIssued(parseAgentToken(b)));
  const revoke = (a: Agent) => void act(`${agentPath(a.id)}/revoke`, { method: 'POST' }, 'Could not revoke the token', (b) => {
    oneOf(obj(b).outcome, ['revoked'] as const);
    setMessage(`Revoked the token of ${a.mxid}.`);
  });
  const remove = (a: Agent) => void act(agentPath(a.id), { method: 'DELETE' }, 'Could not delete the agent', (b) => {
    oneOf(obj(b).outcome, ['deleted'] as const);
    setConfirmDelete(null);
    setMessage(`Deleted ${a.mxid}.`);
  });

  if (disabled) {
    return (
      <div className="dr-page">
        <div className="dr-header"><h1>Agents</h1></div>
        <p role="status">Chat (Matrix) is not set up on this server.</p>
      </div>
    );
  }
  const valid = NAME.test(name) && owner !== '';
  return (
    <div className="dr-page">
      <div className="dr-header">
        <h1>Agents</h1>
        <a href={SKILL} download className="btn btn-secondary"><Download size={14} /><span>Download skill</span></a>
      </div>
      <p className="dr-hint">
        Agents are OpenClaw or Hermes Agent bots that chat as their own Matrix account. Each belongs to a member; offboarding
        that member locks the agent. A token unused for {list?.idle_days ?? 90} days is revoked.
      </p>
      {message && <div className="dr-alert dr-alert-success" role="status"><span>{message}</span></div>}
      {error && <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>}
      {list && (
        <section className="panel dr-section" aria-label="Agents">
          {list.agents.length === 0 ? <p className="dr-hint">No agents yet.</p> : (
            <div className="console-table-wrap">
              <table className="console-table">
                <thead>
                  <tr>
                    <th scope="col">Agent</th><th scope="col">Owner</th><th scope="col">Device</th><th scope="col">Last used</th>
                    <th scope="col">Token</th><th scope="col">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {list.agents.map((a) => (
                    <tr key={a.id}>
                      <td><div>{a.username}</div><div className="dr-mono dr-hint">{a.mxid}</div></td>
                      <td>
                        <div>{ownerName(a.owner)}{a.owner.status !== 'active' && <span className="dr-hint"> ({a.owner.status})</span>}</div>
                        <div className="dr-mono dr-hint">{a.owner.mxid || '—'}</div>
                      </td>
                      <td className="dr-mono">{a.device_id}</td>
                      <td><LastUsed agent={a} idleDays={list.idle_days} /></td>
                      <td>
                        <span className={STATE_BADGE[a.token_state].badge}>{STATE_BADGE[a.token_state].label}</span>
                        {a.locked && <> <span className="badge badge-danger" title="The owner was offboarded">Locked</span></>}
                      </td>
                      <td>
                        <div className="dr-actions">
                          <button type="button" className="btn-secondary" disabled={busy} onClick={() => rotate(a)} aria-label={`Rotate token of ${a.username}`}>
                            <KeyRound size={14} /><span>Rotate</span>
                          </button>
                          {a.token_state === 'active' && (
                            <button type="button" className="btn-secondary" disabled={busy} onClick={() => revoke(a)} aria-label={`Revoke token of ${a.username}`}>Revoke</button>
                          )}
                          <button type="button" className="btn-danger" disabled={busy} onClick={() => setConfirmDelete(a)} aria-label={`Delete ${a.username}`}>
                            <Trash2 size={14} />
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {confirmDelete && (
            <div role="group" aria-label="Confirm delete" className="dr-alert dr-alert-warn">
              <span>
                Delete {confirmDelete.mxid}? Its token stops working and its Matrix account is deactivated for good. Its messages
                stay in the rooms. This cannot be undone.
              </span>
              <div className="dr-actions">
                <button type="button" className="btn-danger" disabled={busy} onClick={() => remove(confirmDelete)}>Delete agent</button>
                <button type="button" className="btn-secondary" disabled={busy} onClick={() => setConfirmDelete(null)}>Cancel</button>
              </div>
            </div>
          )}
        </section>
      )}
      {list && (
        <section className="panel dr-section" aria-labelledby="create-title">
          <div className="panel-header"><h3 id="create-title"><Bot size={16} /> Create agent</h3></div>
          {list.owners.length === 0 && <p className="dr-hint">No member can own an agent yet: an owner must be active and have signed in to chat once.</p>}
          <form className="dr-row" onSubmit={(e) => { e.preventDefault(); if (valid) setConfirming(true); }}>
            <label className="dr-field">
              <span>Name</span>
              <input value={name} maxLength={38} autoComplete="off" spellCheck={false} aria-invalid={name !== 'agent-' && !NAME.test(name)}
                aria-describedby="agent-name-hint" disabled={confirming} onChange={(e) => setName(e.target.value)} />
            </label>
            <label className="dr-field">
              <span>Owner</span>
              <select value={owner} disabled={confirming} onChange={(e) => setOwner(e.target.value)}>
                <option value="">Choose a member</option>
                {list.owners.map((o) => <option key={o.id} value={o.id}>{ownerName(o)} ({o.mxid})</option>)}
              </select>
            </label>
            <button type="submit" disabled={busy || confirming || !valid}>Create agent…</button>
          </form>
          <p id="agent-name-hint" className="dr-hint">
            agent- then 1–32 of a–z, 0–9 and -. The name becomes the agent’s Matrix ID and cannot be changed.
          </p>
          {confirming && (
            <div className="dr-alert dr-alert-warn" role="alert">
              <div>
                <strong>Before creating {name}:</strong>
                <ul>{AGENT_WARNINGS.map((w) => <li key={w}>{w}</li>)}</ul>
                <div className="dr-actions">
                  <button type="button" disabled={busy} onClick={create}>Create {name}</button>
                  <button type="button" className="btn-secondary" disabled={busy} onClick={() => setConfirming(false)}>Cancel</button>
                </div>
              </div>
            </div>
          )}
        </section>
      )}
      {issued && <TokenDialog issued={issued} onClose={() => setIssued(null)} />}
    </div>
  );
};
