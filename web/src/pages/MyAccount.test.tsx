import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MyAccount } from './MyAccount';

vi.mock('../browserSupport', () => ({
  supportedBrowsers: 'current desktop Chrome, Edge and Firefox',
  browserSupport: async () => ({state: 'supported', missing: []}),
}));
const user = {display_name: 'Alice', username: 'alice', sso_provider: 'kysignon', sso_subject: 'sub-1'};
const device = {id: 'dev-1', name: '<img src=x onerror=alert(1)>', fingerprint: 'ab'.repeat(32), status: 'approved', created_at: 1790000000};
function respond(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), {status, headers: {'Content-Type': 'application/json'}});
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it('shows the account, the chat notice and browser support', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({devices: []})));
  render(<MyAccount user={user} onLogout={() => {}} />);
  expect(screen.getByText('Alice')).toBeTruthy();
  expect(screen.getByText('Encrypted chat is not available on this server yet. It ships after an independent security review.')).toBeTruthy();
  expect(await screen.findByText(/This browser can run KyMessages chat/)).toBeTruthy();
  expect(await screen.findByText('No messaging devices.')).toBeTruthy();
});

it('lists devices safely and revokes one after confirmation', async () => {
  const fetch = vi.fn()
    .mockResolvedValueOnce(respond({devices: [device]}))
    .mockResolvedValueOnce(respond({revoked: true}))
    .mockResolvedValueOnce(respond({devices: [{...device, status: 'revoked'}]}));
  vi.stubGlobal('fetch', fetch);
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
  const {container} = render(<MyAccount user={user} onLogout={() => {}} />);
  expect(await screen.findByText(new RegExp(device.fingerprint))).toBeTruthy();
  expect(container.querySelector('img')).toBeNull();
  fireEvent.click(screen.getByRole('button', {name: /Revoke/}));
  expect(await screen.findByText(/revoked/i)).toBeTruthy();
  expect(confirm).toHaveBeenCalledOnce();
  expect(String(fetch.mock.calls[1][0])).toBe('/api/messaging/devices/dev-1');
  expect(fetch.mock.calls[1][1].method).toBe('DELETE');
  expect(screen.queryByRole('button', {name: /Revoke/})).toBeNull();
});

it('shows a suspended device with its automatic revocation date', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({devices: [{...device, status: 'suspended', expires_at: 1792592000}]})));
  render(<MyAccount user={user} onLogout={() => {}} />);
  expect(await screen.findByText(/Revoked automatically on/)).toBeTruthy();
});

it('non-suite account sees no device actions', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({error: 'Suite OIDC sign-in required'}, 403)));
  render(<MyAccount user={{username: 'admin', sso_provider: 'local'}} onLogout={() => {}} />);
  expect(await screen.findByText('Messaging needs a KySignOn account.')).toBeTruthy();
  expect(screen.queryByRole('button', {name: /Revoke/})).toBeNull();
});

it('rejects a malformed device list', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({devices: [{id: 7}]})));
  render(<MyAccount user={user} onLogout={() => {}} />);
  expect(await screen.findByText('Devices unavailable. Refresh or sign in again.')).toBeTruthy();
});
