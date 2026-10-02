import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { Users } from './Users';
import { ConfirmItsYou } from '../components/ConfirmItsYou';

const A = '01J9ZK8V6N3W4X5Y6Z7A8B9C0A';
const PAGE = {
  users: [
    { id: A, username: 'alice', mxid: '@alice:example.com', status: 'active', kyidentity: 'active' },
    { id: '01J9ZK8V6N3W4X5Y6Z7A8B9C0D', username: 'dave', mxid: '@dave:example.com', status: 'not_linked', kyidentity: '' },
  ],
  total: 2, offset: 0, limit: 50, directory_url: 'https://id.example.com',
};
const SESSIONS = { sessions: [
  { kind: 'browser', id: 'S1', device: '', client: 'Firefox', ip: '192.0.2.7', created_at: '2026-10-01T10:00:00Z', last_active_at: null },
  { kind: 'oauth2', id: 'S2', device: 'DEVA', client: 'Element', ip: '192.0.2.7', created_at: '2026-10-01T10:00:00Z', last_active_at: '2026-10-02T09:00:00Z' },
] };
const STEP_UP = { error: "Confirm it's you: this change needs a sign-in from the last 10 minutes", code: 'reauthentication_required', reauth_url: '/api/sso/kyidentity/login?fresh=1' };
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });

function serve(route: (url: string, init?: RequestInit) => Response | undefined) {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    const res = route(url, init);
    if (!res) throw new Error(`unexpected fetch ${url}`);
    return res;
  }));
  return calls;
}
const users = (url: string) => (url.startsWith('/api/admin/matrix/users?') ? json(PAGE) : undefined);
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

async function openSessions() {
  fireEvent.click(await screen.findByRole('button', { name: 'Sessions for alice' }));
  const panel = await screen.findByRole('region', { name: 'Sessions for @alice:example.com' });
  await within(panel).findByText('DEVA');
  return panel;
}

describe('Users', () => {
  it('lists Matrix users with status, KyIdentity link and the access notice', async () => {
    const calls = serve(users);
    render(<Users />);
    expect(await screen.findByText('@alice:example.com')).toBeTruthy();
    expect(screen.getAllByText('Not linked')).toHaveLength(2);
    expect(screen.getByRole('note').textContent).toContain('Access is controlled in KyIdentity');
    expect(screen.getByRole('link', { name: /Open KyIdentity/ }).getAttribute('href')).toBe('https://id.example.com');
    expect(screen.queryByRole('button', { name: /lock/i })).toBeNull();
    expect(calls[0]).toBe('GET /api/admin/matrix/users?search=&offset=0&limit=50');
  });

  it('searches and pages', async () => {
    const calls = serve((url) => (url.startsWith('/api/admin/matrix/users?') ? json({ ...PAGE, total: 120 }) : undefined));
    render(<Users />);
    await screen.findByText('@alice:example.com');
    fireEvent.change(screen.getByLabelText('Search by username'), { target: { value: ' ali ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Search' }));
    await waitFor(() => expect(calls).toContain('GET /api/admin/matrix/users?search=ali&offset=0&limit=50'));
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(calls).toContain('GET /api/admin/matrix/users?search=ali&offset=50&limit=50'));
  });

  it('says when chat is not set up', async () => {
    serve(() => json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404));
    render(<Users />);
    expect(await screen.findByText('Chat (Matrix) is not set up on this server.')).toBeTruthy();
  });

  it('shows why MAS is unreachable', async () => {
    serve(() => json({ error: 'Matrix admin API unavailable: MAS token: HTTP 401' }, 502));
    render(<Users />);
    expect((await screen.findByRole('alert')).textContent).toContain('MAS token: HTTP 401');
  });

  it('refuses a malformed page', async () => {
    serve(() => json({ ...PAGE, users: [{ ...PAGE.users[0], status: 'superuser' }] }));
    render(<Users />);
    expect((await screen.findByRole('alert')).textContent).toContain('Invalid response from the server');
    expect(screen.queryByText('@alice:example.com')).toBeNull();
  });

  it('ends one session', async () => {
    let finished = false;
    const calls = serve((url, init) => {
      if (url === `/api/admin/matrix/users/${A}/sessions`) return json(finished ? { sessions: SESSIONS.sessions.slice(0, 1) } : SESSIONS);
      if (url === '/api/admin/matrix/sessions/oauth2/S2/finish' && init?.method === 'POST') {
        finished = true;
        return json({ outcome: 'ended', mxid: '@alice:example.com' });
      }
      return users(url);
    });
    render(<><ConfirmItsYou /><Users /></>);
    const panel = await openSessions();
    expect(within(panel).getAllByText('192.0.2.7', { selector: 'td' })).toHaveLength(2);
    fireEvent.click(within(panel).getByRole('button', { name: 'End Matrix app session DEVA' }));
    expect(await within(panel).findByText('Ended 1 session.')).toBeTruthy();
    await waitFor(() => expect(within(panel).queryByText('DEVA')).toBeNull());
    expect(calls).toContain('POST /api/admin/matrix/sessions/oauth2/S2/finish');
  });

  it('ends all sessions, browser first, after the admin confirms it is them', async () => {
    const posts: string[] = [];
    serve((url, init) => {
      if (url.endsWith('/sessions')) return json(SESSIONS);
      if (init?.method === 'POST') {
        posts.push(url);
        return posts.length === 1 ? json(STEP_UP, 403) : json({ outcome: 'ended', mxid: '@alice:example.com' });
      }
      return users(url);
    });
    render(<><ConfirmItsYou /><Users /></>);
    const panel = await openSessions();
    fireEvent.click(within(panel).getByRole('button', { name: 'End all sessions' }));
    fireEvent.click(within(await screen.findByRole('dialog', { name: "Confirm it's you" })).getByRole('button', { name: 'Retry' }));
    expect(await within(panel).findByText('Ended 2 sessions.')).toBeTruthy();
    expect(posts).toEqual([
      '/api/admin/matrix/sessions/browser/S1/finish',
      '/api/admin/matrix/sessions/browser/S1/finish',
      '/api/admin/matrix/sessions/oauth2/S2/finish',
    ]);
  });

  it('stops when the admin cancels the confirmation', async () => {
    const posts: string[] = [];
    serve((url, init) => {
      if (url.endsWith('/sessions')) return json(SESSIONS);
      if (init?.method === 'POST') { posts.push(url); return json(STEP_UP, 403); }
      return users(url);
    });
    render(<><ConfirmItsYou /><Users /></>);
    const panel = await openSessions();
    fireEvent.click(within(panel).getByRole('button', { name: 'End all sessions' }));
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }));
    expect((await within(panel).findByRole('alert')).textContent).toContain("Confirm it's you");
    expect(posts).toHaveLength(1);
  });

  it('keeps the End failure when the reload after it fails too', async () => {
    let lists = 0;
    serve((url, init) => {
      if (url.endsWith('/sessions')) return ++lists === 1 ? json(SESSIONS) : json({ error: 'MAS unreachable' }, 502);
      if (init?.method === 'POST') return json({ error: 'Could not end: MAS said no' }, 502);
      return users(url);
    });
    render(<Users />);
    const panel = await openSessions();
    fireEvent.click(within(panel).getByRole('button', { name: 'End Matrix app session DEVA' }));
    await waitFor(() => expect(lists).toBe(2));
    const alert = await within(panel).findByRole('alert');
    await waitFor(() => expect(alert.textContent).toContain('MAS unreachable'));
    expect(alert.textContent).toContain('Could not end: MAS said no');
  });

  it('lists a session whose device id is as long as the server allows', async () => {
    const device = 'D'.repeat(200);
    serve((url) => (url.endsWith('/sessions') ? json({ sessions: [{ ...SESSIONS.sessions[1], device }] }) : users(url)));
    render(<Users />);
    fireEvent.click(await screen.findByRole('button', { name: 'Sessions for alice' }));
    expect(await screen.findByText(device)).toBeTruthy();
  });

  it('keeps Close disabled while ending, and stops the loop once the panel is gone', async () => {
    const posts: string[] = [];
    let release: (r: Response) => void = () => {};
    serve((url, init) => {
      if (url.endsWith('/sessions')) return json(SESSIONS);
      if (init?.method === 'POST') { posts.push(url); return undefined; }
      return users(url);
    });
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.endsWith('/sessions')) return json(SESSIONS);
      if (init?.method === 'POST') { posts.push(url); return new Promise<Response>((r) => { release = r; }); }
      return users(url)!;
    });
    const { unmount } = render(<Users />);
    const panel = await openSessions();
    fireEvent.click(within(panel).getByRole('button', { name: 'End all sessions' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect((within(panel).getByRole('button', { name: 'Close sessions' }) as HTMLButtonElement).disabled).toBe(true);
    unmount();
    release(json({ outcome: 'ended', mxid: '@alice:example.com' }));
    await new Promise((r) => setTimeout(r, 20));
    expect(posts).toHaveLength(1);
  });
});
