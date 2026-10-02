import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { Dashboard } from './Dashboard';

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

it('shows chat health, the user count and the last backup, each linking to its page', async () => {
  serve({
    '/api/admin/health': json({ matrix: true, components: [up('kymessages'), up('database'), { ...up('element'), mismatch: true }] }),
    '/api/admin/matrix/users': json({ users: [], total: 7, offset: 0, limit: 1, directory_url: '' }),
    '/api/backup/status': json({ last_run: { outcome: 'success', trigger: 'scheduled', recorded_at: '2026-10-02T03:00:00Z', capsule_id: 'c1' } }),
    '/api/admin/matrix/sync-status': json(SYNC),
  });
  const onNavigate = vi.fn();
  render(<Dashboard settings={{ app_name: 'KyMessages' }} user={{ username: 'root' }} onNavigate={onNavigate} />);
  expect(await screen.findByText('3 of 3 components up, 1 off their pinned version')).toBeTruthy();
  expect(await screen.findByText('7 Matrix users')).toBeTruthy();
  expect(await screen.findByText(/^Last backup: Succeeded/)).toBeTruthy();
  expect(screen.queryByText(/not set up yet/)).toBeNull();
  expect(await screen.findByText(/^Last webhook .*; last sweep ok$/)).toBeTruthy();
  fireEvent.click(within(screen.getByRole('region', { name: 'KyIdentity sync' })).getByRole('button'));
  expect(onNavigate).toHaveBeenCalledWith('settings');
  fireEvent.click(within(screen.getByRole('region', { name: 'Chat health' })).getByRole('button'));
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
  expect(await screen.findByText('Chat (Matrix) is not set up')).toBeTruthy();
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
