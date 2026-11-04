import React, { useEffect, useState } from 'react';
import { AppHeader } from './components/AppHeader';
import { Dashboard } from './pages/Dashboard';
import { Login, ssoErrorFromHash } from './pages/Login';
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
import { MemberHome } from './pages/MemberHome';
import { hashFor, tabFromHash, type SettingsSection } from './route';
import { LicenseFooter } from './components/LicenseFooter';
import { AppRail } from './components/AppRail';
import './styles/theme.css';
import './ky-ui/tokens.css';
import './ky-ui/navigation.css';
import './ky-ui/shell.css';
import './ky-ui/appearance.css';
import './ky-ui/appearance-themes.css';
import { errorMessage, secureFetch } from './api';
import { parseSettings, parseSuiteApps, parseUser, type SessionUser, type SiteSettings, type SuiteApp } from './dto';
import { ErrorBoundary } from './components/ErrorBoundary';

export const App: React.FC = () => {
  const [user, setUser] = useState<SessionUser | null>(null);
  const [notice, setNotice] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState<boolean>(true);
  const [hash, setHash] = useState(() => window.location.hash);
  const [settings, setSettings] = useState<SiteSettings | null>(null);
  const [suiteApps, setSuiteApps] = useState<SuiteApp[]>([]);
  // A refused single sign-on returns to #/login?sso_error=; a signed-in user (a failed
  // Confirm it's you) sees it in the shell, since the hash is rewritten to a page.
  const [ssoError, setSSOError] = useState(() => ssoErrorFromHash(window.location.hash));

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

  // The view lives in the URL hash, so links, reload and the back button work.
  useEffect(() => {
    const sync = () => { setHash(window.location.hash); setSSOError(''); };
    window.addEventListener('hashchange', sync);
    return () => window.removeEventListener('hashchange', sync);
  }, []);
  const activeTab = tabFromHash(hash, user?.role ?? '');
  // A moved or unknown hash is rewritten in place, not added to history.
  useEffect(() => {
    if (user && window.location.hash && window.location.hash !== hashFor(activeTab)) history.replaceState(null, '', hashFor(activeTab));
  }, [user, activeTab, hash]);
  const setActiveTab = (tab: string) => { window.location.hash = hashFor(tab); };

  // The app rail: fetched once per sign-in; any failure leaves it out.
  const signedIn = user !== null && !user.must_change_password;
  useEffect(() => {
    setSuiteApps([]);
    if (!signedIn) return;
    let live = true;
    fetch('/api/suite/apps')
      .then(async (r) => (r.ok ? parseSuiteApps(await r.json()) : []))
      .then((apps) => { if (live) setSuiteApps(apps); }, () => {});
    return () => { live = false; };
  }, [signedIn, user?.username]);

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
          setSSOError('');
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
    <div className="ky-app">
      <AppHeader
        appName={settings?.app_name || 'KyMessages'}
        appVersion={settings?.app_version}
        logoCustom={settings?.logo_custom ?? true}
        activeTab={activeTab}
        onTabChange={(t) => setActiveTab(t)}
        user={user}
        onLogout={handleLogout}
        suiteApps={suiteApps}
      />

      {isAdmin && <ConfirmItsYou />}

      {errorAlert}
      {ssoError && <div className="dr-alert dr-alert-error" role="alert" style={{ margin: 16 }}><span>{ssoError} <a href="/api/sso/kyidentity/login?fresh=1">Try again</a></span></div>}

      <main className="ky-content">
        <ErrorBoundary key={activeTab}>
        {activeTab.startsWith('settings/') && (
          <Settings section={activeTab.slice(9) as SettingsSection} user={user} settings={settings} onBrandingChanged={() => void loadSettings()} />
        )}
        {!isAdmin && activeTab === 'dashboard' && <MemberHome user={user} chatUrl={settings?.chat_url ?? ''} onLogout={handleLogout} />}
        {isAdmin && activeTab === 'dashboard' && <Dashboard settings={settings} user={user} onNavigate={(t) => setActiveTab(t)} />}
        {isAdmin && activeTab === 'users' && <Users />}
        {isAdmin && activeTab === 'agents' && <Agents />}
        {isAdmin && activeTab === 'rooms' && <Rooms />}
        {isAdmin && activeTab === 'health' && <Health />}
        {isAdmin && activeTab === 'audit' && <Audit />}
        {isAdmin && activeTab === 'scim' && <SCIMAdmin />}
        {isAdmin && activeTab === 'backup' && <Backup />}
        </ErrorBoundary>
        <LicenseFooter className="ky-content-foot" appVersion={settings?.app_version}><AppRail apps={suiteApps} /></LicenseFooter>
      </main>
    </div>
  );
};

function useResponses(r1: Response, r2: Response): [Response, Response] {
  return [r1, r2];
}
