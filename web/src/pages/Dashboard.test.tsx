import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { CHAT_SETUP_GUIDE, Dashboard } from './Dashboard';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const up = (name: string) => ({ name, status: 'up', version: '', pinned: '', mismatch: false, source: '', error: '' });
const SYNC = {
  webhook: { at: '2026-10-02T03:00:00Z', kind: 'user.updated' }, rejected: { count: 0, last_at: null, last_reason: '' },
  sweep: { finished_at: '2026-10-02T03:01:00Z', ok: true, error: '', applied: 0, failed: 0, failing_since: null },
  kyidentity_url: 'https://id.example.com',
};
function serve(routes: Record<string, Response>) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const key = Object.keys(routes).find((k) => String(input).startsWith(k));
    if (!key) throw new Error(`unexpected ${String(input)}`);
    return routes[key].clone();
  }));
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('shows the chat connection, the user count and the last backup, each with its next action', async () => {
  serve({
    '/api/admin/health': json({ matrix: true, components: [up('kymessages'), up('database'), { ...up('element'), mismatch: true }] }),
    '/api/admin/matrix/users': json({ users: [], total: 7, offset: 0, limit: 1, directory_url: '' }),
    '/api/backup/status': json({ last_run: { outcome: 'success', trigger: 'scheduled', recorded_at: '2026-10-02T03:00:00Z', capsule_id: 'c1' } }),
    '/api/admin/matrix/sync-status': json(SYNC),
  });
  const onNavigate = vi.fn();
  render(<Dashboard settings={{ app_name: 'KyMessages' }} user={{ username: 'root' }} onNavigate={onNavigate} />);
  expect(await screen.findByText('1 of 1 components up, 1 off their pinned version')).toBeTruthy();
  expect(screen.getByText('2 of 2 components up')).toBeTruthy(); // console health, apart from chat
  expect(await screen.findByText('7 Matrix users')).toBeTruthy();
  expect(await screen.findByText(/^Last backup: Succeeded/)).toBeTruthy();
  expect(screen.queryByText(/not set up yet/)).toBeNull();
  expect(await screen.findByText(/^Last webhook .*; last sweep ok$/)).toBeTruthy();
  fireEvent.click(within(screen.getByRole('region', { name: 'KyIdentity sync' })).getByRole('button'));
  expect(onNavigate).toHaveBeenCalledWith('settings');
  const chat = within(screen.getByRole('region', { name: 'Chat connection' })).getByRole('button', { name: 'Open Health' });
  expect(chat.className).toBe(''); // the most severe row carries the one filled button
  expect(screen.getByRole('button', { name: 'Open Users' }).className).toBe('btn-secondary');
  fireEvent.click(chat);
  expect(onNavigate).toHaveBeenCalledWith('health');
});

it('says when chat is not set up and when a source fails', async () => {
  serve({
    '/api/admin/health': json({ error: 'boom' }, 500),
    '/api/admin/matrix/users': json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404),
    '/api/backup/status': json({}),
    '/api/admin/matrix/sync-status': json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404),
  });
  render(<Dashboard settings={null} user={null} onNavigate={() => {}} />);
  expect(await screen.findByText('Unavailable: HTTP 500')).toBeTruthy();
  expect(await screen.findByText('Available once chat is set up')).toBeTruthy();
  expect(await screen.findByText('Last backup: none recorded')).toBeTruthy();
  await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([u]) => String(u).startsWith('/api/admin/matrix/sync-status'))).toBe(true));
  expect(screen.queryByRole('region', { name: 'KyIdentity sync' })).toBeNull();
});

const syncRoutes = (sync: unknown) => ({
  '/api/admin/health': json({ matrix: true, components: [up('kymessages')] }),
  '/api/admin/matrix/users': json({ users: [], total: 0, offset: 0, limit: 1, directory_url: '' }),
  '/api/admin/matrix/sync-status': json(sync),
  '/api/backup/status': json({}),
});

it('shows a failing sweep on the sync card', async () => {
  serve(syncRoutes({ ...SYNC, sweep: { ...SYNC.sweep, ok: false, error: 'MAS token: HTTP 401', failing_since: '2026-10-02T02:00:00Z' } }));
  render(<Dashboard settings={null} user={null} onNavigate={() => {}} />);
  const text = await within(await screen.findByRole('region', { name: 'KyIdentity sync' })).findByText(/^Offboarding sweep failing since /);
  expect(text.className).toBe('dr-danger');
});

it('shows a sync warning in the warning tone', async () => {
  serve(syncRoutes({ ...SYNC, webhook: null }));
  render(<Dashboard settings={null} user={null} onNavigate={() => {}} />);
  const text = await within(await screen.findByRole('region', { name: 'KyIdentity sync' })).findByText(/^Needs attention: /);
  expect(text.className).toBe('dr-warn');
});

it('invents no sync row when the server cannot be reached', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch'); }));
  render(<Dashboard settings={null} user={null} onNavigate={() => {}} />);
  expect(await screen.findAllByText('Unavailable: the server could not be reached')).toHaveLength(3);
  await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([u]) => String(u).startsWith('/api/admin/matrix/sync-status'))).toBe(true));
  expect(screen.queryByRole('region', { name: 'KyIdentity sync' })).toBeNull();
});

it('leads with chat not configured and sends the operator to the setup guide', async () => {
  serve({
    '/api/admin/health': json({ matrix: false, components: [up('kymessages'), up('database')] }),
    '/api/admin/matrix/users': json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404),
    '/api/backup/status': json({}),
    '/api/admin/matrix/sync-status': json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404),
  });
  render(<Dashboard settings={null} user={null} onNavigate={() => {}} />);
  expect(await screen.findByRole('heading', { level: 2, name: 'Chat is not configured' })).toBeTruthy();
  const setup = within(screen.getByRole('region', { name: 'Chat connection' })).getByRole('link', { name: 'Set up chat' });
  expect(setup.getAttribute('href')).toBe(CHAT_SETUP_GUIDE);
  expect(setup.getAttribute('rel')).toBe('noopener noreferrer');
  expect(setup.className).toBe('btn');
  expect(screen.getByText('Not configured').className).toBe('dr-warn');
  expect(await screen.findByText('2 of 2 components up')).toBeTruthy();
  expect(screen.getAllByRole('button').every((b) => b.className === 'btn-secondary')).toBe(true);
});
