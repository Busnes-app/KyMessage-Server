import React, { useEffect, useState } from 'react';
import { AppHeader } from './components/AppHeader';
import { Dashboard } from './pages/Dashboard';
import { Login } from './pages/Login';
import { ChangePassword } from './pages/ChangePassword';
import { Backup } from './pages/Backup';
import { SCIMAdmin } from './pages/SCIMAdmin';
import { Settings } from './pages/Settings';
import { Users } from './pages/Users';
import { ConfirmItsYou } from './components/ConfirmItsYou';
import { MemberHome } from './pages/MemberHome';
import './styles/theme.css';
import './ky-ui/tokens.css';
import './ky-ui/navigation.css';
import { secureFetch } from './api';

export const App: React.FC = () => {
  const [user, setUser] = useState<any>(null);
  const [notice, setNotice] = useState('');
  const [loading, setLoading] = useState<boolean>(true);
  const [activeTab, setActiveTab] = useState<string>('dashboard');
  const [settings, setSettings] = useState<any>(null);

  useEffect(() => {
    const checkAuth = async () => {
      try {
        const [authResp, setResp] = useResponses(
          await fetch('/api/auth/me'),
          await fetch('/api/settings')
        );

        if (setResp.ok) {
          const s = await setResp.json();
          setSettings(s);
        }

        if (authResp.ok) {
          const a = await authResp.json();
          if (a.authenticated) {
            setUser(a.user);
          }
        }
      } catch (err) {
        console.error('Initialization error:', err);
      } finally {
        setLoading(false);
      }
    };

    checkAuth();
  }, []);

  // /api/settings returns more fields once authenticated, so re-read it after login.
  const loadSettings = async () => {
    const resp = await fetch('/api/settings');
    if (resp.ok) {
      const s = await resp.json();
      setSettings(s);
    }
  };

  const handleLogout = async () => {
    await secureFetch('/api/auth/logout', { method: 'POST' });
    setUser(null);
  };

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
      <Login
        appName={settings?.app_name || 'KyMessages'}
        onSuccess={(u) => {
          setNotice('');
          setUser(u);
          void loadSettings();
        }}
      />
      </>
    );
  }

  if (user.must_change_password) {
    return <ChangePassword onLogout={handleLogout} onComplete={() => {
      setUser(null);
      setNotice('Password changed. Sign in with your new password.');
    }} />;
  }

  const isAdmin = user.role === 'admin';

  return (
    <div className="app-shell">
      <AppHeader
        appName={settings?.app_name || 'KyMessages'}
        activeTab={activeTab}
        onTabChange={(t) => setActiveTab(t)}
        user={user}
        onLogout={handleLogout}
      />

      {isAdmin && <ConfirmItsYou />}

      <main className="app-main">
        {!isAdmin && <MemberHome user={user} chatUrl={typeof settings?.chat_url === 'string' ? settings.chat_url : ''} onLogout={handleLogout} />}
        {isAdmin && activeTab === 'dashboard' && <Dashboard settings={settings} user={user} onNavigate={(t) => setActiveTab(t)} />}
        {isAdmin && activeTab === 'users' && <Users />}
        {isAdmin && activeTab === 'scim' && <SCIMAdmin />}
        {isAdmin && activeTab === 'backup' && <Backup />}
        {isAdmin && activeTab === 'settings' && <Settings settings={settings} />}
      </main>
    </div>
  );
};

function useResponses(r1: Response, r2: Response): [Response, Response] {
  return [r1, r2];
}
