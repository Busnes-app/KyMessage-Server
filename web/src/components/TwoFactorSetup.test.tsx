import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { TwoFactorSetup } from './TwoFactorSetup';
import { secureFetch } from '../api';

vi.mock('qrcode', () => ({ default: { toDataURL: vi.fn(async () => 'data:image/png;base64,AAAA') } }));

afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

function server(routes: Record<string, (body: unknown) => Response>, me: object) {
  const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === '/api/auth/me') return json({ authenticated: true, user: me });
    return routes[path](init?.body ? JSON.parse(String(init.body)) : undefined);
  });
  vi.stubGlobal('fetch', fetcher);
  return fetcher;
}

it('refuses a wrong code, then shows recovery codes once on a right one', async () => {
  const fetcher = server({
    '/api/auth/totp/begin': () => json({ secret: 'JBSWY3DPEHPK3PXP', otpauth_uri: 'otpauth://totp/KyMessages:admin?secret=JBSWY3DPEHPK3PXP' }),
    '/api/auth/totp/confirm': (b) => (b as { code: string }).code === '123456'
      ? json({ totp_enabled: true, recovery_codes: ['aaaa-bbbb', 'cccc-dddd'] })
      : json({ error: 'That code is not valid' }, 400),
  }, { sso_provider: 'local', totp_enabled: false });
  render(<TwoFactorSetup />);
  fireEvent.click(await screen.findByRole('button', { name: 'Set up two-factor sign-in' }));
  expect(await screen.findByAltText('QR code for your authenticator app')).toBeTruthy();
  expect(screen.getByText('JBSWY3DPEHPK3PXP')).toBeTruthy();

  fireEvent.change(screen.getByLabelText('Code from the app'), { target: { value: '000000' } });
  fireEvent.click(screen.getByRole('button', { name: 'Turn on' }));
  expect((await screen.findByRole('alert')).textContent).toContain('That code is not valid');
  expect(screen.queryByLabelText('Recovery codes')).toBeNull();

  fireEvent.change(screen.getByLabelText('Code from the app'), { target: { value: '123456' } });
  fireEvent.click(screen.getByRole('button', { name: 'Turn on' }));
  expect((await screen.findByLabelText('Recovery codes')).textContent).toContain('aaaa-bbbb');
  fireEvent.click(screen.getByRole('button', { name: 'I have saved them' }));
  expect(screen.queryByLabelText('Recovery codes')).toBeNull();
  expect(screen.getByText('Two-factor sign-in is on.')).toBeTruthy();
  expect(fetcher).toHaveBeenCalledWith('/api/auth/totp/confirm', expect.objectContaining({ method: 'POST' }));
});

it('keeps two-factor on when the disable code is refused', async () => {
  server({ '/api/auth/totp/disable': () => json({ error: 'That code is not valid' }, 400) }, { sso_provider: 'local', totp_enabled: true });
  render(<TwoFactorSetup />);
  fireEvent.change(await screen.findByLabelText('Code or recovery code'), { target: { value: '999999' } });
  fireEvent.click(screen.getByRole('button', { name: 'Turn off' }));
  expect((await screen.findByRole('alert')).textContent).toContain('That code is not valid');
  expect(screen.getByText('Two-factor sign-in is on.')).toBeTruthy();
});

it('shows nothing for a suite account', async () => {
  const fetcher = server({}, { sso_provider: 'kyidentity', totp_enabled: false });
  const { container } = render(<TwoFactorSetup />);
  await waitFor(() => expect(fetcher).toHaveBeenCalled());
  expect(container.textContent).toBe('');
});

it('mirrors the __Host- CSRF cookie of a secure deployment', async () => {
  const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => json({}));
  vi.stubGlobal('fetch', fetcher);
  // jsdom's http origin refuses to store a __Host- cookie, so present it directly.
  vi.spyOn(document, 'cookie', 'get').mockReturnValue('ky_theme=x; __Host-ky_csrf=host-token');
  await secureFetch('/api/x', { method: 'POST' });
  expect(new Headers(fetcher.mock.calls[0][1]?.headers).get('X-CSRF-Token')).toBe('host-token');
});
