import { useEffect, useState } from 'react';
import { secureFetch } from '../api';
import { browserSupport, supportedBrowsers, type BrowserSupport } from '../browserSupport';

type Device = {id: string; name: string; fingerprint: string; status: 'unverified' | 'pending' | 'approved' | 'suspended' | 'revoked'; createdAt: number; expiresAt?: number};
type Devices = {kind: 'loading'} | {kind: 'ready'; devices: Device[]} | {kind: 'not-suite'} | {kind: 'error'};
const statuses = new Set(['unverified', 'pending', 'approved', 'suspended', 'revoked']);
const label = (s: Device['status']) => s === 'unverified' ? 'enrollment not finished' : s;
const invalid = () => { throw new Error('Invalid device list'); };
const text = (v: unknown) => typeof v === 'string' && v && v.length <= 255 ? v : invalid();
const whole = (v: unknown) => typeof v === 'number' && Number.isSafeInteger(v) && v >= 0 ? v : invalid();
const date = (v: unknown) => { const n = whole(v); return Number.isNaN(new Date(n * 1000).getTime()) ? invalid() : n; };

function devicesResponse(value: unknown): Device[] {
  const body = value as {devices?: unknown} | null;
  if (body === null || typeof body !== 'object' || !Array.isArray(body.devices)) invalid();
  return (body as {devices: unknown[]}).devices.map(raw => {
    const d = (raw ?? {}) as Record<string, unknown>;
    if (typeof d.status !== 'string' || !statuses.has(d.status)) invalid();
    return {id: text(d.id), name: text(d.name), fingerprint: text(d.fingerprint), status: d.status as Device['status'],
      createdAt: whole(d.created_at), expiresAt: d.expires_at === undefined ? undefined : date(d.expires_at)};
  });
}
const day = (s: number) => new Date(s * 1000).toLocaleDateString();

// Members' landing page: account, their own messaging devices and browser support.
// Chat itself ships only after the independent review.
export function MyAccount({user, onLogout}: {user: {display_name?: string; username: string; sso_provider: string; sso_subject?: string}; onLogout: () => void}) {
  const [devices, setDevices] = useState<Devices>({kind: 'loading'});
  const [support, setSupport] = useState<BrowserSupport | null>(null);
  const [refresh, setRefresh] = useState(0);
  const [revokeError, setRevokeError] = useState('');

  useEffect(() => { let live = true; void browserSupport().then(s => { if (live) setSupport(s); }); return () => { live = false; }; }, []);
  useEffect(() => {
    const controller = new AbortController();
    void (async () => {
      try {
        const response = await fetch('/api/messaging/devices', {signal: controller.signal, cache: 'no-store'});
        if (response.status === 403) {
          const body: unknown = await response.json().catch(() => null);
          const suite = (body as {error?: unknown} | null)?.error === 'Suite OIDC sign-in required' || user.sso_provider !== 'kysignon';
          if (!controller.signal.aborted) setDevices({kind: suite ? 'not-suite' : 'error'});
          return;
        }
        if (!response.ok) throw new Error('unavailable');
        const list = devicesResponse(await response.json());
        if (!controller.signal.aborted) setDevices({kind: 'ready', devices: list});
      } catch { if (!controller.signal.aborted) setDevices({kind: 'error'}); }
    })();
    return () => controller.abort();
  }, [refresh]);

  const revoke = async (device: Device) => {
    if (!window.confirm(`Revoke ${device.name}? It can no longer read or send messages.`)) return;
    const response = await secureFetch('/api/messaging/devices/' + encodeURIComponent(device.id), {method: 'DELETE'}).catch(() => null);
    setRevokeError(response?.ok ? '' : `Could not revoke ${device.name}. Try again.`);
    setRefresh(n => n + 1);
  };

  return <section className="card" aria-labelledby="my-account">
    <h2 id="my-account">My account</h2>
    <p>{user.display_name || user.username}</p>
    <p>Signed in as {user.username}{user.sso_provider === 'kysignon' ? ' with KySignOn' : ''}.</p>
    <button type="button" className="btn-secondary" onClick={onLogout}>Sign out</button>
    <p>Encrypted chat is not available on this server yet.</p>
    <h3>Browser</h3>
    {support === null ? <p role="status">Checking this browser…</p>
      : support.state === 'supported' ? <p>This browser can run KyMessages chat.</p>
      : support.state === 'unverified' ? <p>This browser has every feature chat needs but has not been verified. Supported: {supportedBrowsers}.</p>
      : <p>This browser isn't supported for chat yet. Missing: {support.missing.join(', ')}. Supported: {supportedBrowsers}.</p>}
    <h3>My messaging devices</h3>
    {devices.kind === 'loading' && <p role="status">Loading devices…</p>}
    {devices.kind === 'not-suite' && <p>Messaging needs a KySignOn account.</p>}
    {revokeError && <p role="alert">{revokeError}</p>}
    {devices.kind === 'error' && <><p role="alert">Devices unavailable. Refresh or sign in again.</p>
      <button type="button" className="btn-secondary" onClick={() => setRefresh(n => n + 1)}>Refresh</button></>}
    {devices.kind === 'ready' && (devices.devices.length === 0 ? <p>No messaging devices.</p> :
      <ul style={{listStyle: 'none', padding: 0}}>
        {devices.devices.map(d => <li key={d.id} style={{borderTop: '1px solid var(--line)', padding: '12px 0', overflowWrap: 'anywhere'}}>
          <strong>{d.name}</strong> — {label(d.status)}{d.status === 'suspended' && d.expiresAt ? `. Revoked automatically on ${day(d.expiresAt)}` : ''}
          <div>Fingerprint {d.fingerprint}</div>
          <div>Added {day(d.createdAt)}</div>
          {d.status !== 'revoked' && <button type="button" className="btn-secondary" onClick={() => void revoke(d)}>Revoke {d.name}</button>}
        </li>)}
      </ul>)}
  </section>;
}
