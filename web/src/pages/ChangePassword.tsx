import { useState } from 'react';
import type { FormEvent } from 'react';
import { secureFetch } from '../api';

interface ChangePasswordProps {
  onComplete: () => void;
  onLogout: () => void;
}

export function ChangePassword({ onComplete, onLogout }: ChangePasswordProps) {
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmation, setConfirmation] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError('');
    if (newPassword !== confirmation) { setError('The new passwords do not match.'); return; }
    if (currentPassword === newPassword) { setError('Choose a different password.'); return; }
    setBusy(true);
    try {
      const response = await secureFetch('/api/auth/change-password', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
      });
      if (!response.ok) {
        const body: unknown = await response.json();
        throw new Error(typeof body === 'object' && body !== null && 'error' in body && typeof body.error === 'string'
          ? body.error : 'Password change failed. Please try again.');
      }
      setCurrentPassword(''); setNewPassword(''); setConfirmation('');
      onComplete();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Password change failed. Please try again.');
    } finally { setBusy(false); }
  }

  return (
    <main className="ky-auth">
      <div className="ky-auth-body">
        <div className="ky-auth-brand">
          <h1>Change your password</h1>
          <p>Replace your temporary password before continuing.</p>
        </div>
        <form onSubmit={submit} className="ky-auth-card">
          <p className="ky-auth-note">Use at least 12 characters. You will sign in again afterward.</p>
          {error && <p role="alert" className="ky-auth-error">{error}</p>}
          <div className="ky-auth-field">
            <label htmlFor="current-password">Current password</label>
            <input id="current-password" type="password" autoComplete="current-password" required maxLength={1024}
              value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} />
          </div>
          <div className="ky-auth-field">
            <label htmlFor="new-password">New password</label>
            <input id="new-password" type="password" autoComplete="new-password" required minLength={12} maxLength={1024}
              value={newPassword} onChange={(event) => setNewPassword(event.target.value)} />
          </div>
          <div className="ky-auth-field">
            <label htmlFor="confirm-password">Confirm new password</label>
            <input id="confirm-password" type="password" autoComplete="new-password" required minLength={12} maxLength={1024}
              value={confirmation} onChange={(event) => setConfirmation(event.target.value)} />
          </div>
          <button type="submit" className="ky-auth-submit" disabled={busy}>{busy ? 'Changing password…' : 'Change password'}</button>
          <button type="button" className="ky-auth-link" disabled={busy} onClick={onLogout}>Sign out</button>
        </form>
      </div>
    </main>
  );
}
