import React, { useEffect, useState } from 'react';
import { AppHeader } from './components/AppHeader';
import { Dashboard } from './pages/Dashboard';
import { Login } from './pages/Login';
import { ChangePassword } from './pages/ChangePassword';
import { Backup } from './pages/Backup';
import { SCIMAdmin } from './pages/SCIMAdmin';
import { Settings } from './pages/Settings';
import { Users } from './pages/Users';
import { Agents } from './pages/Agents';
import { Rooms } from './pages/Rooms';
import { Health } from './pages/Health';
import { Audit } from './pages/Audit';
import { ConfirmItsYou } from './components/ConfirmItsYou';
import { TwoFactorSetup } from './components/TwoFactorSetup';
import { MemberHome } from './pages/MemberHome';
import { Appearance } from './pages/Appearance';
import './styles/theme.css';
import './ky-ui/tokens.css';
import './ky-ui/navigation.css';
import './ky-ui/shell.css';
import './ky-ui/appearance.css';
import './ky-ui/appearance-themes.css';
import { errorMessage, secureFetch } from './api';
import { parseSettings, parseUser, type SessionUser, type SiteSettings } from './dto';
import { ErrorBoundary } from './components/ErrorBoundary';

export const App: React.FC = () => {
  const [user, setUser] = useState<SessionUser | null>(null);
  const [notice, setNotice] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState<boolean>(true);
  const [activeTab, setActiveTab] = useState<string>('dashboard');
  const [settings, setSettings] = useState<SiteSettings | null>(null);

  useEffect(() => {
    const checkAuth = async () => {
      try {
        const [authResp, setResp] = useResponses(
          await fetch('/api/auth/me'),
          await fetch('/api/settings')
        );

        if (setResp.ok) setSettings(parseSettings(await setResp.json()));

        if (authResp.ok) {
          const a: unknown = await authResp.json();
          if (typeof a === 'object' && a !== null && 'authenticated' in a && a.authenticated === true) {
            setUser(parseUser('user' in a ? a.user : undefined));
          }
        }
      } catch (err) {
        setError(`Could not load the console: ${err instanceof Error ? err.message : 'unknown error'}`);
      } finally {
        setLoading(false);
      }
    };

    checkAuth();
  }, []);

  // /api/settings returns more fields once authenticated, so re-read it after login.
  const loadSettings = async () => {
    try {
      const resp = await fetch('/api/settings');
      if (resp.ok) setSettings(parseSettings(await resp.json()));
    } catch (err) {
      setError(`Could not load settings: ${err instanceof Error ? err.message : 'unknown error'}`);
    }
  };

  // Only a confirmed sign-out leaves the console; otherwise the session may still be live.
  const handleLogout = async () => {
    setError('');
    try {
      const res = await secureFetch('/api/auth/logout', { method: 'POST' });
      if (!res.ok) throw new Error(await errorMessage(res, 'Sign-out failed'));
      setUser(null);
    } catch (err) {
      setError(`You are still signed in: ${err instanceof Error ? err.message : 'sign-out failed'}`);
    }
  };
  const errorAlert = error && <div className="dr-alert dr-alert-error" role="alert" style={{ margin: 16 }}><span>{error}</span></div>;

  if (loading) {
    return (
      <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'var(--bg)', color: 'var(--ink)' }}>
        Loading {settings?.app_name || 'KyMessages'}...
      </div>
    );
  }

  if (!user) {
    return (
      <>
      {notice && <p role="status" style={{ padding: 16 }}>{notice}</p>}
      {errorAlert}
      <Login
        appName={settings?.app_name || 'KyMessages'}
        ssoEnabled={settings?.sso_enabled ?? false}
        logoCustom={settings?.logo_custom ?? true}
        onSuccess={(u) => {
          setNotice('');
          setError('');
          setUser(u);
          void loadSettings();
        }}
      />
      </>
    );
  }

  if (user.must_change_password) {
    return <>{errorAlert}<ChangePassword onLogout={handleLogout} onComplete={() => {
      setUser(null);
      setNotice('Password changed. Sign in with your new password.');
    }} /></>;
  }

  const isAdmin = user.role === 'admin';

  return (
    <div className="app-shell">
      <AppHeader
        appName={settings?.app_name || 'KyMessages'}
        appVersion={settings?.app_version}
        logoCustom={settings?.logo_custom ?? true}
        activeTab={activeTab}
        onTabChange={(t) => setActiveTab(t)}
        user={user}
        onLogout={handleLogout}
      />

      {isAdmin && <ConfirmItsYou />}

      {errorAlert}

      <main className="app-main">
        <ErrorBoundary key={activeTab}>
        {activeTab === 'appearance' && <Appearance />}
        {!isAdmin && activeTab !== 'appearance' && <MemberHome user={user} chatUrl={settings?.chat_url ?? ''} onLogout={handleLogout} />}
        {isAdmin && activeTab === 'dashboard' && <Dashboard settings={settings} user={user} onNavigate={(t) => setActiveTab(t)} />}
        {isAdmin && activeTab === 'users' && <Users />}
        {isAdmin && activeTab === 'agents' && <Agents />}
        {isAdmin && activeTab === 'rooms' && <Rooms />}
        {isAdmin && activeTab === 'health' && <Health />}
        {isAdmin && activeTab === 'audit' && <Audit />}
        {isAdmin && activeTab === 'scim' && <SCIMAdmin />}
        {isAdmin && activeTab === 'backup' && <Backup />}
        {isAdmin && activeTab === 'settings' && (
          <Settings settings={settings} onBrandingChanged={() => void loadSettings()}><TwoFactorSetup /></Settings>
        )}
        </ErrorBoundary>
      </main>
    </div>
  );
};

function useResponses(r1: Response, r2: Response): [Response, Response] {
  return [r1, r2];
}
