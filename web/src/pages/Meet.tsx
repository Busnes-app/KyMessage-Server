import React, { useEffect, useState } from 'react';
import { AlertCircle, DoorOpen } from 'lucide-react';
import { CaptchaWidget } from '../components/CaptchaWidget';
import { secureFetch } from '../api';
import { obj, str } from '../dto';

/** The guest's page, /meet#<token>: the token stays in the fragment, so no server log sees it. */
type View =
  | { kind: 'loading' }
  | { kind: 'invalid' }
  | { kind: 'ended' }
  | { kind: 'waiting'; opens: string }
  | { kind: 'open' }
  | { kind: 'error'; message: string };

const UNREACHABLE = 'The meeting could not be reached. Try again in a moment.';

/** Every field is untrusted: anything unexpected reads as an error. */
export function parseMeetStatus(status: number, v: unknown): View {
  if (status === 404) return { kind: 'invalid' };
  try {
    const o = obj(v);
    if (status !== 200) return { kind: 'error', message: str(o.error ?? '', 200) || UNREACHABLE };
    switch (o.state) {
      case 'open': return { kind: 'open' };
      case 'waiting': return { kind: 'waiting', opens: str(o.opens_at, 64) };
      case 'ended': return { kind: 'ended' };
    }
  } catch { /* falls through */ }
  return { kind: 'error', message: UNREACHABLE };
}

/** Only an https address may be followed: the answer names Element's bootstrap page. */
export function nextURL(v: unknown): string | null {
  try {
    const next = str(obj(v).next, 512);
    return new URL(next).protocol === 'https:' ? next : null;
  } catch {
    return null;
  }
}

const post = (path: string, body: object) => secureFetch(`/api/meet/${path}`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), cache: 'no-store',
});

export const Meet: React.FC = () => {
  const [token] = useState(() => window.location.hash.slice(1));
  const [view, setView] = useState<View>({ kind: 'loading' });
  const [name, setName] = useState('');
  const [captcha, setCaptcha] = useState('');
  const [round, setRound] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    post('status', { token })
      .then(async (res) => { if (!cancelled) setView(parseMeetStatus(res.status, await res.json().catch(() => null))); })
      .catch(() => { if (!cancelled) setView({ kind: 'error', message: 'The server could not be reached. Check the connection and try again.' }); });
    return () => { cancelled = true; };
  }, [token]);

  const join = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      const res = await post('join', { token, name, captcha });
      const body: unknown = await res.json().catch(() => null);
      const next = res.ok ? nextURL(body) : null;
      if (!next) {
        const refused = parseMeetStatus(res.ok ? 500 : res.status, body);
        throw new Error(refused.kind === 'error' ? refused.message : "This link isn't valid. Ask the organizer for a new invitation.");
      }
      window.location.assign(next);
    } catch (err) {
      setError(err instanceof TypeError ? 'The server could not be reached. Check the connection and try again.' : err instanceof Error ? err.message : 'Could not join the meeting');
      setBusy(false);
    } finally {
      setCaptcha('');
      setRound((r) => r + 1);
    }
  };

  const say = (text: string) => <p className="ky-auth-note">{text}</p>;
  return (
    <main className="ky-auth">
      <div className="ky-auth-body">
        <div className="ky-auth-brand">
          <img className="ky-mark" src="/app-icon.png" width={56} height={56} alt="" />
          <h1>Join the meeting</h1>
          <p>You join as a guest. Someone in the meeting lets you in.</p>
        </div>
        <div className="ky-auth-card">
          {view.kind === 'loading' && say('Checking your link…')}
          {view.kind === 'invalid' && say("This link isn't valid. Ask the organizer for a new invitation.")}
          {view.kind === 'ended' && say('This meeting has ended.')}
          {view.kind === 'waiting' && say(`This meeting opens ${view.opens ? `at ${new Date(view.opens).toLocaleString()}` : 'later'}, 10 minutes before it starts. Come back then.`)}
          {view.kind === 'error' && <p role="alert" className="ky-auth-error"><AlertCircle size={16} aria-hidden="true" /><span>{view.message}</span></p>}
          {view.kind === 'open' && (
            <form onSubmit={join}>
              {error && <p role="alert" className="ky-auth-error"><AlertCircle size={16} aria-hidden="true" /><span>{error}</span></p>}
              <div className="ky-auth-field">
                <label htmlFor="meet-name">Your name</label>
                <input id="meet-name" type="text" autoComplete="name" maxLength={64} autoFocus required
                  value={name} onChange={(e) => setName(e.target.value)} />
              </div>
              <p className="ky-auth-note">Others see it with “(guest)” after it.</p>
              <CaptchaWidget onToken={setCaptcha} round={round} />
              <button type="submit" className="ky-auth-submit" disabled={busy || !captcha || !name.trim()}>
                <DoorOpen aria-hidden="true" />
                <span>{busy ? 'Joining…' : 'Ask to join'}</span>
              </button>
            </form>
          )}
        </div>
      </div>
    </main>
  );
};
