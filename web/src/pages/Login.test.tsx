import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createHash } from 'node:crypto';
import { Login } from './Login';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });

// A challenge whose answer is 1, so each solve is one hash.
function challenge(salt: string) {
  return {
    algorithm: 'SHA-256', salt, challenge: createHash('sha256').update(`${salt}1`).digest('hex'),
    maxnumber: 10, expires_at: Math.floor(Date.now() / 1000) + 300, signature: 'sig',
  };
}

function stubServer() {
  let challenges = 0;
  const logins: Record<string, unknown>[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === '/api/auth/pow-challenge') return json(challenge(`salt${++challenges}`));
    if (url === '/api/auth/login') {
      logins.push(JSON.parse(String(init?.body)));
      return json({ error: 'Invalid credentials' }, 401);
    }
    throw new Error(`unexpected fetch ${url}`);
  }));
  return { challenges: () => challenges, logins };
}

const settle = () => act(() => new Promise((r) => setTimeout(r, 50)));

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('solves one challenge per mount, not one per render', async () => {
  const server = stubServer();
  render(<Login onSuccess={vi.fn()} appName="KyMessages" />);
  await screen.findByText('Browser check passed');
  await settle();
  fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'admin' } });
  await settle();
  expect(server.challenges()).toBe(1);
});

it('fetches exactly one new challenge after a failed sign-in, and sends each token once', async () => {
  const server = stubServer();
  render(<Login onSuccess={vi.fn()} appName="KyMessages" />);
  await screen.findByText('Browser check passed');
  fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'admin' } });
  fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'WrongPassword1!' } });
  fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
  expect((await screen.findByRole('alert')).textContent).toContain('Invalid credentials');
  await waitFor(() => expect(server.challenges()).toBe(2));
  await screen.findByText('Browser check passed');
  await settle();
  expect(server.challenges()).toBe(2);

  fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
  await waitFor(() => expect(server.logins).toHaveLength(2));
  const [first, second] = server.logins.map((l) => JSON.parse(atob(String(l.captcha_token))).salt);
  expect([first, second]).toEqual(['salt1', 'salt2']);
});

it('labels its fields for assistive technology and password managers', () => {
  stubServer();
  render(<Login onSuccess={vi.fn()} appName="KyMessages" />);
  expect(screen.getByLabelText('Username').getAttribute('autocomplete')).toBe('username');
  expect(screen.getByLabelText('Password').getAttribute('autocomplete')).toBe('current-password');
  expect(screen.queryByText('Cloud Mobile First Base Platform')).toBeNull();
});

it('offers no single sign-on when it is not configured', () => {
  stubServer();
  render(<Login onSuccess={vi.fn()} appName="KyMessages" />);
  expect(screen.queryByText(/single sign-on/i)).toBeNull();
  expect(screen.queryByRole('separator')).toBeNull();
});

it('offers single sign-on once, as the KyIdentity link', () => {
  stubServer();
  render(<Login onSuccess={vi.fn()} appName="KyMessages" ssoEnabled />);
  expect(screen.getByRole('link', { name: 'Continue with single sign-on' }).getAttribute('href')).toBe('/api/sso/kyidentity/login');
  expect(screen.getAllByText(/single sign-on/i)).toHaveLength(1);
});

it('explains a refused single sign-on from its code and offers to try again', () => {
  stubServer();
  window.location.hash = '#/login?sso_error=not_provisioned';
  render(<Login onSuccess={vi.fn()} appName="KyMessages" ssoEnabled />);
  const alert = screen.getByRole('alert');
  expect(alert.textContent).toContain('This server has no account for you yet.');
  expect(screen.getByRole('link', { name: 'Try again' }).getAttribute('href')).toBe('/api/sso/kyidentity/login');
  cleanup();
  // Only the fixed codes are shown; anything else in the URL is ignored.
  window.location.hash = '#/login?sso_error=%3Cb%3Ehi';
  render(<Login onSuccess={vi.fn()} appName="KyMessages" ssoEnabled />);
  expect(screen.queryByRole('alert')).toBeNull();
  window.location.hash = '';
});
