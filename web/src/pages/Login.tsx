import React, { useState } from 'react';
import { Key, AlertCircle, LogIn } from 'lucide-react';
import { CaptchaWidget } from '../components/CaptchaWidget';
import { ThemeSwitcher } from '../components/ThemeSwitcher';
import { parseUser, type SessionUser } from '../dto';

interface LoginProps {
  onSuccess: (user: SessionUser) => void;
  appName: string;
  /** KyIdentity SSO is configured: offer it under a divider. */
  ssoEnabled?: boolean;
  /** An admin-uploaded logo is never inverted in dark schemes. */
  logoCustom?: boolean;
}

/** What a refused single sign-on came back with (`#/login?sso_error=<code>`); unknown codes are ignored. */
export const SSO_ERRORS: Record<string, string> = {
  not_assigned: "Your KyIdentity account can't sign in to this server. Ask an administrator to give you access.",
  not_provisioned: 'This server has no account for you yet. Ask an administrator to add you.',
  username_conflict: 'Another account on this server already has your KyIdentity username. Ask an administrator.',
  adoption_conflict: 'More than one account on this server is linked to your KyIdentity account. Ask an administrator.',
  disabled: 'Your account on this server is turned off. Ask an administrator.',
  failed: "Single sign-on didn't finish.",
};

export function ssoErrorFromHash(hash: string): string {
  const code = new URLSearchParams(hash.split('?')[1] ?? '').get('sso_error') ?? '';
  return Object.hasOwn(SSO_ERRORS, code) ? SSO_ERRORS[code] : '';
}

export const Login: React.FC<LoginProps> = ({ onSuccess, appName, ssoEnabled = false, logoCustom = true }) => {
  const [username, setUsername] = useState<string>('');
  const [password, setPassword] = useState<string>('');
  const [captchaToken, setCaptchaToken] = useState<string>('');
  const [captchaRound, setCaptchaRound] = useState<number>(0);
  const [loading, setLoading] = useState<boolean>(false);
  const [error, setError] = useState<string>('');
  const [ssoError, setSSOError] = useState<string>(() => ssoErrorFromHash(window.location.hash));

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSSOError('');
    setLoading(true);

    try {
      const resp = await fetch('/api/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          username,
          password,
          captcha_token: captchaToken || undefined,
        }),
      });

      const data = await resp.json();
      if (!resp.ok) {
        throw new Error(data.error || 'Login failed');
      }

      if (data.authenticated) {
        onSuccess(parseUser(data.user));
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Login failed');
    } finally {
      setLoading(false);
      // The attempt spent the token, whatever the answer: solve a fresh challenge.
      setCaptchaToken('');
      setCaptchaRound((r) => r + 1);
    }
  };

  return (
    <main className="ky-auth">
      <div className="ky-auth-theme"><ThemeSwitcher /></div>
      <div className="ky-auth-body">
        <div className="ky-auth-brand">
          <img className={logoCustom ? 'ky-mark' : 'ky-mark ky-mark-stamp'} src="/app-icon.png" width={56} height={56} alt="" />
          <h1>{appName || 'KyMessages'}</h1>
          <p>Team chat on your own server.</p>
        </div>

        <div className="ky-auth-card">
          {ssoError && <p role="alert" className="ky-auth-error"><AlertCircle size={16} aria-hidden="true" /><span>{ssoError} <a href="/api/sso/kyidentity/login">Try again</a></span></p>}
          {error && <p role="alert" className="ky-auth-error"><AlertCircle size={16} aria-hidden="true" /><span>{error}</span></p>}

          <form onSubmit={handleLogin}>
            <div className="ky-auth-field">
              <label htmlFor="login-username">Username</label>
              <input id="login-username" type="text" autoComplete="username" autoCapitalize="none" spellCheck={false} autoFocus
                value={username} onChange={(e) => setUsername(e.target.value)} required />
            </div>
            <div className="ky-auth-field">
              <label htmlFor="login-password">Password</label>
              <input id="login-password" type="password" autoComplete="current-password"
                value={password} onChange={(e) => setPassword(e.target.value)} required />
            </div>
            <CaptchaWidget onToken={setCaptchaToken} round={captchaRound} />
            <button type="submit" className="ky-auth-submit" disabled={loading}>
              <LogIn aria-hidden="true" />
              <span>{loading ? 'Signing in…' : 'Sign in'}</span>
            </button>
            {ssoEnabled && <>
              <hr className="ky-auth-divider" />
              <a href="/api/sso/kyidentity/login" className="ky-auth-sso">
                <Key aria-hidden="true" />
                <span>Continue with single sign-on</span>
              </a>
            </>}
          </form>
        </div>
      </div>
    </main>
  );
};
