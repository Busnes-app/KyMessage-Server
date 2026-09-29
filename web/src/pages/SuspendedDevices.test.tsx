import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { SuspendedDevices } from './SuspendedDevices';

const device = {id:'dev-1',user_id:'u1',username:'alice',name:'<img src=x onerror=alert(1)>',fingerprint:'ab'.repeat(32),created_at:1790000000,identity_generation:1};
function respond(value: unknown, status = 200) {
  return new Response(JSON.stringify(value),{status,headers:{'Content-Type':'application/json'}});
}
afterEach(() => {cleanup();vi.unstubAllGlobals();vi.restoreAllMocks();});

it('lists suspended devices and revokes one after confirmation',async () => {
  const fetch = vi.fn()
    .mockResolvedValueOnce(respond({devices:[device]}))
    .mockResolvedValueOnce(respond({revoked:true}))
    .mockResolvedValueOnce(respond({devices:[]}));
  vi.stubGlobal('fetch',fetch);
  const confirm = vi.spyOn(window,'confirm').mockReturnValue(true);
  const {container} = render(<SuspendedDevices />);
  expect(await screen.findByText(/alice/)).toBeTruthy();
  expect(screen.getByText(new RegExp(device.fingerprint))).toBeTruthy();
  expect(container.querySelector('img')).toBeNull();
  expect(String(fetch.mock.calls[0][0])).toBe('/api/admin/messaging/devices?status=suspended');
  fireEvent.click(screen.getByRole('button',{name:/Revoke/}));
  expect(await screen.findByText('No suspended devices.')).toBeTruthy();
  expect(confirm).toHaveBeenCalledOnce();
  expect(String(fetch.mock.calls[1][0])).toBe('/api/admin/messaging/devices/dev-1/revoke');
  expect(fetch.mock.calls[1][1].method).toBe('POST');
});

it('does nothing when the confirmation is declined',async () => {
  const fetch = vi.fn().mockResolvedValueOnce(respond({devices:[device]}));
  vi.stubGlobal('fetch',fetch);
  vi.spyOn(window,'confirm').mockReturnValue(false);
  render(<SuspendedDevices />);
  fireEvent.click(await screen.findByRole('button',{name:/Revoke/}));
  expect(fetch).toHaveBeenCalledOnce();
});

it('offers a fresh sign-in when revocation needs step-up',async () => {
  vi.stubGlobal('fetch',vi.fn()
    .mockResolvedValueOnce(respond({devices:[device]}))
    .mockResolvedValueOnce(respond({error:'Sign in again',code:'reauthentication_required',reauth_url:'/api/sso/kysignon/login?fresh=1'},403)));
  vi.spyOn(window,'confirm').mockReturnValue(true);
  render(<SuspendedDevices />);
  fireEvent.click(await screen.findByRole('button',{name:/Revoke/}));
  const link = await screen.findByRole('link',{name:/Sign in to KySignOn again/});
  expect(link.getAttribute('href')).toBe('/api/sso/kysignon/login?fresh=1');
});

it('rejects a malformed list at the response boundary',async () => {
  vi.stubGlobal('fetch',vi.fn().mockResolvedValueOnce(respond({devices:[{...device,created_at:'soon'}]})));
  render(<SuspendedDevices />);
  expect((await screen.findByRole('alert')).textContent).toContain('Invalid');
  expect(screen.queryByText('No suspended devices.')).toBeNull();
});
