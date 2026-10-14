import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { SCIMAdmin } from './SCIMAdmin';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const stub = (res: () => Response) => {
  const fetcher = vi.fn(async () => res());
  vi.stubGlobal('fetch', fetcher);
  return fetcher;
};

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('shows a load failure instead of an empty directory', async () => {
  stub(() => json({ error: 'Administrator role required' }, 403));
  render(<SCIMAdmin />);
  expect((await screen.findByRole('alert')).textContent).toContain('Administrator role required');
  expect(screen.queryByText(/No accounts have been provisioned/)).toBeNull();
});

it('refuses a malformed directory', async () => {
  stub(() => json({ enabled: true, token_set: true, users: [{ id: 'u1' }] }));
  render(<SCIMAdmin />);
  expect((await screen.findByRole('alert')).textContent).toContain('Invalid response from the server');
});

it('lists provisioned users from the admin route and never offers a token to copy', async () => {
  const fetcher = stub(() => json({ enabled: true, token_set: true, users: [
    { id: 'u1', username: 'carol', display_name: 'Carol', email: 'carol@example.test', status: 'active' },
    { id: 'u2', username: 'dave', display_name: '', email: '', status: 'inactive' },
  ] }));
  const { container } = render(<SCIMAdmin />);
  expect(await screen.findByText('carol')).toBeTruthy();
  expect(screen.getByText('inactive')).toBeTruthy();
  expect(screen.getByText('KY_SCIM_TOKEN is set.')).toBeTruthy();
  expect(fetcher).toHaveBeenCalledWith('/api/admin/scim/users', expect.anything());
  expect(container.querySelector('input[type=password]')).toBeNull();
  expect(screen.queryByRole('button')).toBeNull();
  expect(container.textContent).not.toContain('Bearer token configured');
});

it('says when KY_SCIM_TOKEN is not set', async () => {
  stub(() => json({ enabled: true, token_set: false, users: [] }));
  render(<SCIMAdmin />);
  expect(await screen.findByText(/is not set, so no provider can authenticate/)).toBeTruthy();
  expect(screen.getByText('No accounts have been provisioned through SCIM.')).toBeTruthy();
});
