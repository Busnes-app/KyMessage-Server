import React, { useState } from 'react';
import { Key, AlertCircle, LogIn } from 'lucide-react';
import { CaptchaWidget } from '../components/CaptchaWidget';
import { ThemeSwitcher } from '../components/ThemeSwitcher';
import { parseUser, type SessionUser } from '../dto';

interface LoginProps {
  onSuccess: (user: SessionUser) => void;
  appName: string;
}

interface MFAChallenge {
  mfa_token: string;
}

export const Login: React.FC<LoginProps> = ({ onSuccess, appName }) => {
  const [username, setUsername] = useState<string>('');
  const [password, setPassword] = useState<string>('');
  const [captchaToken, setCaptchaToken] = useState<string>('');
  const [captchaRound, setCaptchaRound] = useState<number>(0);
  const [mfaChallenge, setMfaChallenge] = useState<MFAChallenge | null>(null);
  const [mfaCode, setMfaCode] = useState<string>('');
  const [isRecoveryCode, setIsRecoveryCode] = useState<boolean>(false);
  const [loading, setLoading] = useState<boolean>(false);
  const [error, setError] = useState<string>('');

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
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

      if (data.mfa_required) {
        setMfaChallenge(data);
        return;
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

  const handleMFASubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!mfaChallenge) return;
    setError('');
    setLoading(true);

    try {
      const endpoint = isRecoveryCode ? '/api/auth/mfa/recovery-code' : '/api/auth/mfa/totp';
      const resp = await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          mfa_token: mfaChallenge.mfa_token,
          code: mfaCode.trim(),
        }),
      });

      const data = await resp.json();
      if (!resp.ok) {
        throw new Error(data.error || 'MFA verification failed');
      }

      if (data.authenticated) {
        onSuccess(parseUser(data.user));
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Invalid verification code');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="auth-page">
      <div className="auth-theme">
        <ThemeSwitcher />
      </div>

      <div className="auth-column">
        <div className="auth-brand">
          <img src="/app-icon.png" width={56} height={56} alt="" />
          <h1>{appName || 'KyMessages'}</h1>
        </div>

        <div className="panel auth-card">
          {error && (
            <div role="alert" className="dr-alert dr-alert-error">
              <AlertCircle size={16} aria-hidden="true" />
              <span>{error}</span>
            </div>
          )}

          {mfaChallenge ? (
            <form onSubmit={handleMFASubmit}>
              <h2>Two-Factor Verification</h2>
              <p className="dr-hint">
                {isRecoveryCode ? 'Enter one of your single-use recovery codes.' : 'Enter the 6-digit code from your authenticator app.'}
              </p>

              <div className="auth-field">
                <label htmlFor="login-mfa-code">
                  {isRecoveryCode ? 'Recovery Code' : 'Authenticator Code'}
                </label>
                <input
                  id="login-mfa-code"
                  className="auth-code"
                  type="text"
                  autoComplete="one-time-code"
                  autoFocus
                  value={mfaCode}
                  onChange={(e) => setMfaCode(e.target.value)}
                  placeholder={isRecoveryCode ? 'XXXX-XXXX' : '123456'}
                  required
                />
              </div>

              <button type="submit" className="btn-block" disabled={loading}>
                {loading ? 'Verifying...' : 'Verify & Sign In'}
              </button>

              <div className="auth-switch">
                <button type="button" className="btn-ghost" onClick={() => setIsRecoveryCode(!isRecoveryCode)}>
                  {isRecoveryCode ? 'Use Authenticator App' : 'Use a Recovery Code'}
                </button>
              </div>
            </form>
          ) : (
            <form onSubmit={handleLogin}>
              <div className="auth-field">
                <label htmlFor="login-username">Username</label>
                <input
                  id="login-username"
                  type="text"
                  autoComplete="username"
                  autoFocus
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder="admin"
                  required
                />
              </div>

              <div className="auth-field">
                <label htmlFor="login-password">Password</label>
                <input
                  id="login-password"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="••••••••••••"
                  required
                />
              </div>

              <CaptchaWidget onToken={setCaptchaToken} round={captchaRound} />

              <button type="submit" className="btn-block auth-submit" disabled={loading}>
                <LogIn size={16} aria-hidden="true" />
                <span>{loading ? 'Signing in...' : 'Sign In'}</span>
              </button>

              <div className="auth-sso">
                <p>Or continue with Single Sign-On</p>
                <a href="/api/sso/kyidentity/login" className="btn btn-secondary btn-block">
                  <Key size={16} aria-hidden="true" />
                  <span>Sign in with KyIdentity</span>
                </a>
              </div>
            </form>
          )}
        </div>
      </div>
    </div>
  );
};
