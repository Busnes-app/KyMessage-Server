import React, { useEffect, useState } from 'react';
import { Users } from 'lucide-react';
import { errorMessage } from '../api';
import { arr, bool, obj, opt, str } from '../dto';

interface SCIMUser { id: string; username: string; display_name: string; email: string; status: string }
export interface SCIMDirectory { enabled: boolean; token_set: boolean; users: SCIMUser[] }

export function parseSCIMDirectory(v: unknown): SCIMDirectory {
  const o = obj(v);
  return {
    enabled: bool(o.enabled),
    token_set: bool(o.token_set),
    users: arr(o.users).map((u) => {
      const r = obj(u);
      return {
        id: str(r.id, 64), username: str(r.username, 255), display_name: opt(r.display_name, str) ?? '',
        email: opt(r.email, str) ?? '', status: str(r.status, 32),
      };
    }),
  };
}

const cell = { padding: '10px 12px' };

/** SCIM-provisioned accounts, from the admin route; the bearer token is never shown here. */
export const SCIMAdmin: React.FC = () => {
  const [dir, setDir] = useState<SCIMDirectory | null>(null);
  const [error, setError] = useState<string>('');

  useEffect(() => {
    const ctrl = new AbortController();
    (async () => {
      try {
        const resp = await fetch('/api/admin/scim/users', { cache: 'no-store', signal: ctrl.signal });
        if (!resp.ok) throw new Error(await errorMessage(resp, 'Could not load the SCIM directory'));
        setDir(parseSCIMDirectory(await resp.json()));
      } catch (err) {
        if (!ctrl.signal.aborted) setError(err instanceof Error && err.message ? err.message : 'Could not load the SCIM directory');
      }
    })();
    return () => ctrl.abort();
  }, []);

  return (
    <div style={{ maxWidth: '1080px', margin: '0 auto', padding: '32px 20px' }}>
      <div style={{ marginBottom: '24px' }}>
        <h1 style={{ fontSize: '24px', fontWeight: 'bold', display: 'flex', alignItems: 'center', gap: '10px' }}>
          <Users size={24} style={{ color: 'var(--accent)' }} />
          <span>SCIM 2.0 Directory & Inbound Provisioning</span>
        </h1>
        <p style={{ color: 'var(--ink)', fontSize: '14px', marginTop: '4px' }}>
          RFC 7643 and RFC 7644 inbound provisioning from an identity provider.
        </p>
      </div>

      {error && <div className="dr-alert dr-alert-error" role="alert" style={{ marginBottom: '24px' }}><span>{error}</span></div>}

      <div className="panel" style={{ marginBottom: '24px' }}>
        <h3 style={{ fontSize: '16px', marginBottom: '8px' }}>SCIM 2.0 Connection Details</h3>
        <label htmlFor="scim-base-url" style={{ display: 'block', fontSize: '12px', color: 'var(--ink)', marginBottom: '4px' }}>
          SCIM Base URL
        </label>
        <input
          id="scim-base-url"
          type="text"
          readOnly
          value={`${window.location.origin}/scim/v2`}
          style={{ fontFamily: 'var(--font-mono)', fontSize: '13px', marginBottom: '12px' }}
        />
        <p style={{ color: 'var(--ink)', fontSize: '13px' }}>
          The provider authenticates with the bearer token set in <code>KY_SCIM_TOKEN</code>. This console never shows it:
          copy it from where you set it.
        </p>
        {dir && !dir.enabled && (
          <div className="dr-alert dr-alert-warn" role="status" style={{ marginTop: '12px' }}>
            <span>SCIM is off on this server (<code>KY_SCIM_ENABLED=false</code>).</span>
          </div>
        )}
        {dir?.enabled && !dir.token_set && (
          <div className="dr-alert dr-alert-warn" role="status" style={{ marginTop: '12px' }}>
            <span>
              <code>KY_SCIM_TOKEN</code> is not set, so no provider can authenticate. Set it to a long random value and restart.
            </span>
          </div>
        )}
        {dir?.enabled && dir.token_set && (
          <p style={{ color: 'var(--success)', fontSize: '13px', marginTop: '12px' }}>KY_SCIM_TOKEN is set.</p>
        )}
      </div>

      <div className="panel">
        <h3 style={{ fontSize: '16px', marginBottom: '16px' }}>Provisioned Accounts{dir ? ` (${dir.users.length})` : ''}</h3>

        {!dir ? (
          !error && <p style={{ color: 'var(--ink)', fontSize: '13px' }}>Loading directory...</p>
        ) : dir.users.length === 0 ? (
          <p style={{ color: 'var(--ink)', fontSize: '13px' }}>No accounts have been provisioned through SCIM.</p>
        ) : (
          <div style={{ overflowX: 'auto' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '13px' }}>
              <thead>
                <tr style={{ borderBottom: '1px solid var(--line)', textAlign: 'left', color: 'var(--ink)' }}>
                  <th style={{ padding: '8px 12px' }}>Username</th>
                  <th style={{ padding: '8px 12px' }}>Display Name</th>
                  <th style={{ padding: '8px 12px' }}>Email</th>
                  <th style={{ padding: '8px 12px' }}>Status</th>
                </tr>
              </thead>
              <tbody>
                {dir.users.map((u) => (
                  <tr key={u.id} style={{ borderBottom: '1px solid var(--line)' }}>
                    <td style={{ ...cell, fontFamily: 'var(--font-mono)', fontWeight: 600 }}>{u.username}</td>
                    <td style={cell}>{u.display_name || '—'}</td>
                    <td style={{ ...cell, color: 'var(--ink)' }}>{u.email || '—'}</td>
                    <td style={cell}>
                      <span className={`badge badge-${u.status === 'active' ? 'success' : 'danger'}`}>
                        {u.status === 'active' ? 'Active' : u.status}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
};
