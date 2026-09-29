import { useEffect, useState } from 'react';
import { secureFetch } from '../api';

type Device = {id:string; username:string; name:string; fingerprint:string; createdAt:number; generation:number};
type State = {kind:'loading'} | {kind:'ready'; devices:Device[]} | {kind:'error'; message:string};

function text(value: unknown): string {
  if (typeof value !== 'string' || !value || value.length > 255) throw new Error('Invalid suspended device list');
  return value;
}
function whole(value: unknown): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) throw new Error('Invalid suspended device list');
  return value;
}
function devicesResponse(value: unknown): Device[] {
  if (value === null || typeof value !== 'object' || !Array.isArray((value as {devices?: unknown}).devices)) throw new Error('Invalid suspended device list');
  return (value as {devices: unknown[]}).devices.map(raw => {
    const d = (raw ?? {}) as Record<string, unknown>;
    return {id:text(d.id), username:text(d.username), name:text(d.name), fingerprint:text(d.fingerprint), createdAt:whole(d.created_at), generation:whole(d.identity_generation)};
  });
}

// Restored devices stay suspended until their owner signs in again and re-proves the
// device key. An admin can revoke any of them first; live devices are not listed.
export function SuspendedDevices() {
  const [state, setState] = useState<State>({kind:'loading'});
  const [revision, setRevision] = useState(0);
  const [error, setError] = useState('');
  const [reauthUrl, setReauthUrl] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    void (async () => {
      try {
        const response = await fetch('/api/admin/messaging/devices?status=suspended',{signal:controller.signal,cache:'no-store'});
        if (!response.ok) throw new Error('Suspended devices unavailable. Refresh or sign in again.');
        const devices = devicesResponse(await response.json());
        if (!controller.signal.aborted) setState({kind:'ready',devices});
      } catch (err) {
        if (!controller.signal.aborted) setState({kind:'error',message:err instanceof Error ? err.message : 'Suspended devices unavailable.'});
      }
    })();
    return () => controller.abort();
  },[revision]);

  const revoke = async (device: Device) => {
    if (!window.confirm(`Revoke ${device.username}'s device "${device.name}"?\n\nThe owner can no longer resume it and must enroll a new device.`)) return;
    setError('');
    setReauthUrl('');
    const response = await secureFetch(`/api/admin/messaging/devices/${encodeURIComponent(device.id)}/revoke`,{method:'POST',credentials:'same-origin'}).catch(() => null);
    if (!response) {
      setError('Device revocation failed');
      return;
    }
    if (!response.ok) {
      const body: unknown = await response.json().catch(() => null);
      const url = (body as {reauth_url?: unknown} | null)?.reauth_url;
      if (typeof url === 'string' && url.startsWith('/api/sso/')) setReauthUrl(url);
      const message = (body as {error?: unknown} | null)?.error;
      setError(typeof message === 'string' ? message : 'Device revocation failed');
      return;
    }
    setRevision(value => value+1);
  };

  return <section className="panel" aria-labelledby="suspended-devices-title" style={{marginTop:24}}>
    <h2 id="suspended-devices-title" style={{fontSize:18}}>Suspended devices</h2>
    <p>Devices brought back by a message restore. Each stays unusable until its owner signs in again and proves the device key. Revoke any you do not recognise before users return.</p>
    {error && <p role="alert">{error}</p>}
    {reauthUrl && <p>Revoking needs a recent sign-in. <a href={reauthUrl}>Sign in to KySignOn again</a>, then return here.</p>}
    {state.kind === 'loading' && <p role="status">Loading suspended devices…</p>}
    {state.kind === 'error' && <p role="alert">{state.message}</p>}
    {state.kind === 'ready' && (state.devices.length === 0 ? <p>No suspended devices.</p> :
      <ul style={{listStyle:'none',padding:0}}>
        {state.devices.map(device => <li key={device.id} style={{borderTop:'1px solid var(--line)',padding:'12px 0',overflowWrap:'anywhere'}}>
          <strong>{device.username}</strong> · {device.name}
          <p>Enrolled {new Date(device.createdAt*1000).toLocaleString()} · identity generation {device.generation}</p>
          <small>Key fingerprint: {device.fingerprint}</small>
          <div><button type="button" className="btn-secondary" onClick={() => void revoke(device)}>Revoke {device.name}</button></div>
        </li>)}
      </ul>)}
  </section>;
}
