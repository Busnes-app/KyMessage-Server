import React, { useEffect, useState } from 'react';
import { errorMessage } from '../api';
import { arr, bool, obj, opt, str } from '../dto';
import { PageHead } from '../components/PageHead';

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
    <div>
      <PageHead title="Directory & SCIM" lede="SCIM 2.0 (RFC 7643 and RFC 7644) inbound provisioning from an identity provider." />

      {error && <div className="dr-alert dr-alert-error" role="alert" style={{ marginBottom: '24px' }}><span>{error}</span></div>}

      <section className="panel dr-section">
        <h3>SCIM 2.0 Connection Details</h3>
        <label className="dr-field" htmlFor="scim-base-url">SCIM Base URL</label>
        <input id="scim-base-url" className="dr-mono" type="text" readOnly value={`${window.location.origin}/scim/v2`} />
        <p className="dr-hint">
          The provider authenticates with the bearer token set in <code>KY_SCIM_TOKEN</code>. This console never shows it:
          copy it from where you set it.
        </p>
        {dir && !dir.enabled && (
          <div className="dr-alert dr-alert-warn" role="status">
            <span>SCIM is off on this server. Set <code>KY_SCIM_ENABLED=true</code> and restart to turn it on.</span>
          </div>
        )}
        {dir?.enabled && !dir.token_set && (
          <div className="dr-alert dr-alert-warn" role="status">
            <span>
              <code>KY_SCIM_TOKEN</code> is not set, so no provider can authenticate. Set it to a long random value and restart.
            </span>
          </div>
        )}
        {dir?.enabled && dir.token_set && (
          <p className="dr-hint dr-ok">KY_SCIM_TOKEN is set.</p>
        )}
      </section>

      <section className="panel dr-section">
        <h3>Provisioned Accounts{dir ? ` (${dir.users.length})` : ''}</h3>

        {!dir ? (
          !error && <p className="dr-hint">Loading directory...</p>
        ) : dir.users.length === 0 ? (
          <p className="dr-hint">No accounts have been provisioned through SCIM.</p>
        ) : (
          <div className="console-table-wrap">
            <table className="console-table">
              <thead>
                <tr>
                  <th scope="col">Username</th>
                  <th scope="col">Display Name</th>
                  <th scope="col">Email</th>
                  <th scope="col">Status</th>
                </tr>
              </thead>
              <tbody>
                {dir.users.map((u) => (
                  <tr key={u.id}>
                    <td className="dr-mono">{u.username}</td>
                    <td>{u.display_name || '—'}</td>
                    <td className="dr-muted">{u.email || '—'}</td>
                    <td>
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
      </section>
    </div>
  );
};
