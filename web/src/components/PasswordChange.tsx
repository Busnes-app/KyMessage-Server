import React, { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import { LockKeyhole } from 'lucide-react';
import { adminFetch, errorMessage } from '../api';

/** "Password": a local account changes its own password after confirming the current one.
 * Every session ends with it, this one too, so success goes back to sign-in. Suite accounts
 * get nothing here; their password lives in KyIdentity. */
export const PasswordChange: React.FC<{ onChanged?: () => void }> = ({ onChanged = () => window.location.assign('/') }) => {
  const [local, setLocal] = useState(false);
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirmation, setConfirmation] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void (async () => {
      const res = await fetch('/api/auth/me');
      const me = res.ok ? await res.json().catch(() => null) : null;
      setLocal(me?.user?.sso_provider === 'local');
    })();
  }, []);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError('');
    if (next !== confirmation) { setError('The new passwords do not match.'); return; }
    if (current === next) { setError('Choose a different password.'); return; }
    setBusy(true);
    try {
      // adminFetch: a sign-in older than 10 minutes gets Confirm it's you, then the retry.
      const res = await adminFetch('/api/auth/change-password', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ current_password: current, new_password: next }),
      });
      if (!res.ok) throw new Error(await errorMessage(res, 'Password change failed'));
      onChanged();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Password change failed');
      setBusy(false);
    }
  }

  if (!local) return null;
  return (
    <section className="dr-settings dr-settings-own" aria-labelledby="account-password">
      <form className="panel dr-section" onSubmit={submit}>
        <h3 id="account-password"><LockKeyhole size={16} aria-hidden="true" /> Password</h3>
        <p className="dr-hint">At least 12 characters. It needs a sign-in from the last 10 minutes, and changing it signs you out everywhere, here too.</p>
        <label className="dr-field"><span>Current password</span>
          <input type="password" autoComplete="current-password" required maxLength={1024} value={current} onChange={(e) => setCurrent(e.target.value)} />
        </label>
        <label className="dr-field"><span>New password</span>
          <input type="password" autoComplete="new-password" required minLength={12} maxLength={1024} value={next} onChange={(e) => setNext(e.target.value)} />
        </label>
        <label className="dr-field"><span>Confirm new password</span>
          <input type="password" autoComplete="new-password" required minLength={12} maxLength={1024} value={confirmation} onChange={(e) => setConfirmation(e.target.value)} />
        </label>
        {error && <div className="dr-alert dr-alert-error" role="alert">{error}</div>}
        <div className="dr-actions"><button type="submit" disabled={busy}>{busy ? 'Changing password…' : 'Change password'}</button></div>
      </form>
    </section>
  );
};
