import React, { useEffect, useState } from 'react';
import QRCode from 'qrcode';
import { KeyRound } from 'lucide-react';
import { adminFetch, errorMessage } from '../api';
import { arr, bool, obj, str } from '../dto';

type Stage =
  | { kind: 'idle' }
  | { kind: 'enrolling'; secret: string; qr: string }
  | { kind: 'codes'; codes: string[] };

const post = (path: string, body?: object) => adminFetch(path, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: body ? JSON.stringify(body) : undefined,
});

/** "Account security": TOTP for the signed-in local admin. Suite accounts get nothing here;
 * their second factor lives in KyIdentity. Recovery codes are shown once, right after setup. */
export const TwoFactorSetup: React.FC = () => {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [stage, setStage] = useState<Stage>({ kind: 'idle' });
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    void (async () => {
      const res = await fetch('/api/auth/me');
      const me = res.ok ? await res.json().catch(() => null) : null;
      if (me?.user?.sso_provider === 'local') setEnabled(me.user.totp_enabled === true);
    })();
  }, []);

  const run = async (step: () => Promise<void>) => {
    setBusy(true);
    setError('');
    try {
      await step();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'The request failed');
    } finally {
      setBusy(false);
    }
  };

  const begin = () => run(async () => {
    const res = await post('/api/auth/totp/begin');
    if (!res.ok) throw new Error(await errorMessage(res, 'Could not start setup'));
    const d = obj(await res.json());
    const secret = str(d.secret, 128);
    const qr = await QRCode.toDataURL(str(d.otpauth_uri, 2048), { margin: 1, width: 200 });
    setCode('');
    setStage({ kind: 'enrolling', secret, qr });
  });

  const confirm = () => run(async () => {
    const res = await post('/api/auth/totp/confirm', { code: code.trim() });
    if (!res.ok) throw new Error(await errorMessage(res, 'That code was not accepted'));
    const codes = arr(obj(await res.json()).recovery_codes).map((c) => str(c, 64));
    setEnabled(true);
    setCode('');
    setStage({ kind: 'codes', codes });
  });

  const disable = () => run(async () => {
    const res = await post('/api/auth/totp/disable', { code: code.trim() });
    if (!res.ok) throw new Error(await errorMessage(res, 'That code was not accepted'));
    setEnabled(bool(obj(await res.json()).totp_enabled));
    setCode('');
  });

  if (enabled === null) return null;
  const codeField = (label: string) => (
    <label className="dr-field">
      <span>{label}</span>
      <input value={code} inputMode="numeric" autoComplete="one-time-code" maxLength={64}
        onChange={(e) => setCode(e.target.value)} />
    </label>
  );
  return (
    <section className="dr-settings dr-settings-own" aria-labelledby="account-security">
      <div className="panel dr-section">
        <h3 id="account-security"><KeyRound size={16} aria-hidden="true" /> Account security</h3>
        <p className="dr-hint">
          Two-factor sign-in asks for a code from an authenticator app after your password, at every sign-in and every confirmation. Changes need a sign-in from the last 10 minutes.
        </p>

      {stage.kind === 'codes' && (
        <div className="dr-alert dr-alert-warn" role="status">
          <p>Two-factor sign-in is on. Save these recovery codes somewhere safe: each signs in once without your phone, and they are not shown again.</p>
          <ul aria-label="Recovery codes" className="font-mono">
            {stage.codes.map((c) => <li key={c}>{c}</li>)}
          </ul>
          <button type="button" onClick={() => setStage({ kind: 'idle' })}>I have saved them</button>
        </div>
      )}

      {stage.kind === 'enrolling' && (
        <form className="dr-row" onSubmit={(e) => { e.preventDefault(); void confirm(); }}>
          <img src={stage.qr} width={200} height={200} alt="QR code for your authenticator app" />
          <div>
            <p className="dr-hint">Scan the code, or enter this key: <code>{stage.secret}</code></p>
            {codeField('Code from the app')}
            <div className="dr-actions">
              <button type="submit" disabled={busy || !code.trim()}>Turn on</button>
              <button type="button" className="btn-secondary" disabled={busy} onClick={() => setStage({ kind: 'idle' })}>Cancel</button>
            </div>
          </div>
        </form>
      )}

      {stage.kind === 'idle' && !enabled && (
        <div className="dr-state-row">
          <span className="dr-state-label">Two-factor sign-in</span>
          <span className="badge badge-warning" role="status">Off</span>
          <button type="button" disabled={busy} onClick={() => void begin()}>Set up two-factor sign-in</button>
        </div>
      )}

      {stage.kind === 'idle' && enabled && (
        <>
          <div className="dr-state-row">
            <span className="dr-state-label">Two-factor sign-in</span>
            <span className="badge badge-success" role="status">On</span>
          </div>
          <form className="dr-row dr-form" onSubmit={(e) => { e.preventDefault(); void disable(); }}>
            {codeField('Code or recovery code')}
            <button type="submit" className="btn-secondary" disabled={busy || !code.trim()}>Turn off</button>
          </form>
        </>
      )}

      {error && <div className="dr-alert dr-alert-error" role="alert">{error}</div>}
      </div>
    </section>
  );
};
