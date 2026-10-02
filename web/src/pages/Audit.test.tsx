import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Audit } from './Audit';

const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } });
const rec = (id: number, action: string, extra: Record<string, unknown> = {}) =>
  ({ id, at: '2026-10-02T10:00:00Z', actor: 'root', action, target: '', outcome: '', details: '', ip: '192.0.2.9', ...extra });
const PAGE = { records: [
  rec(3, 'matrix.session_end', { target: '@alice:example.com', outcome: 'ended', details: 'session="S2" kind="oauth2" outcome="ended"' }),
  rec(2, 'admin.backup_run', { outcome: 'failure' }),
  rec(1, 'auth.login'),
], total: 120, offset: 0, limit: 50 };
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('lists rows newest first and filters by kind', async () => {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => { calls.push(String(input)); return json(PAGE); }));
  render(<Audit />);
  const cells = await screen.findAllByRole('cell', { name: /^(matrix\.session_end|admin\.backup_run|auth\.login)$/ });
  expect(cells.map((c) => c.textContent)).toEqual(['matrix.session_end', 'admin.backup_run', 'auth.login']);
  expect(screen.getByRole('cell', { name: 'failure' }).className).toContain('dr-danger');
  expect(calls[0]).toBe('/api/admin/audit?offset=0&limit=50');
  expect(calls[0]).not.toContain('kind');
  fireEvent.click(screen.getByRole('button', { name: 'Older' }));
  await waitFor(() => expect(calls).toContain('/api/admin/audit?offset=50&limit=50'));
  fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'backup' } });
  await waitFor(() => expect(calls).toContain('/api/admin/audit?kind=backup&offset=0&limit=50'));
  fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'branding' } });
  await waitFor(() => expect(calls).toContain('/api/admin/audit?kind=branding&offset=0&limit=50'));
});

it('refuses a malformed page', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => json({ ...PAGE, records: [{ ...PAGE.records[0], id: 'x' }] })));
  render(<Audit />);
  expect((await screen.findByRole('alert')).textContent).toContain('Invalid response from the server');
});
