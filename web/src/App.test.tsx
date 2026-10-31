import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { App } from './App';
import { ErrorBoundary } from './components/ErrorBoundary';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const member = { username: 'bob', display_name: 'Bob', role: 'user', must_change_password: false };

function stubServer(logout: () => Promise<Response>, me: unknown = { authenticated: true, user: member }, apps: () => Promise<Response> = async () => json({ apps: [] })) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/suite/apps') return apps();
    if (url === '/api/auth/me') return json(me);
    if (url === '/api/settings') return json({ app_name: 'KyMessages', chat_url: '' });
    if (url === '/api/auth/logout') return logout();
    return json({ error: 'not stubbed' }, 404);
  }));
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('stays signed in and says so when the server refuses sign-out', async () => {
  stubServer(async () => json({ error: 'Invalid CSRF token' }, 403));
  render(<App />);
  fireEvent.click((await screen.findAllByRole('button', { name: 'Sign out' }))[0]);
  expect((await screen.findByRole('alert')).textContent).toContain('You are still signed in: Invalid CSRF token');
  expect(screen.getByRole('heading', { name: 'Bob' })).toBeTruthy();
});

it('stays signed in when sign-out cannot reach the server', async () => {
  stubServer(async () => { throw new TypeError('Failed to fetch'); });
  render(<App />);
  fireEvent.click((await screen.findAllByRole('button', { name: 'Sign out' }))[0]);
  expect((await screen.findByRole('alert')).textContent).toContain('You are still signed in');
  expect(screen.getByRole('heading', { name: 'Bob' })).toBeTruthy();
});

it('returns to sign-in only after the server confirms sign-out', async () => {
  stubServer(async () => json({ logged_out: true }));
  render(<App />);
  fireEvent.click((await screen.findAllByRole('button', { name: 'Sign out' }))[0]);
  expect(await screen.findByLabelText('Username')).toBeTruthy();
  expect(screen.queryByRole('alert')).toBeNull();
});

it('shows an error for a malformed session instead of a blank page', async () => {
  stubServer(async () => json({}), { authenticated: true, user: { username: 42 } });
  render(<App />);
  expect((await screen.findByRole('alert')).textContent).toContain('Could not load the console: Invalid response from the server');
  await waitFor(() => expect(screen.getByLabelText('Username')).toBeTruthy());
});

it('catches a render failure in a page and keeps a message on screen', () => {
  const Broken = () => { throw new Error('boom'); };
  vi.spyOn(console, 'error').mockImplementation(() => {});
  render(<ErrorBoundary><Broken /></ErrorBoundary>);
  expect(screen.getByRole('alert').textContent).toContain('This page failed to load: boom');
  vi.restoreAllMocks();
});

it('fetches the app rail once after sign-in and shows it in both feet', async () => {
  stubServer(async () => json({ logged_out: true }), undefined, async () => json({ apps: [{ id: 'kypost', name: 'KyPost', url: 'https://mail.example.com/login' }] }));
  render(<App />);
  expect(await screen.findAllByRole('link', { name: 'KyPost' })).toHaveLength(2);
  expect(document.querySelector('.ky-sidebar-foot > .ky-app-rail:first-child')).toBeTruthy();
  expect(document.querySelector('.ky-content-foot > .ky-app-rail:first-child')).toBeTruthy();
  const calls = vi.mocked(fetch).mock.calls.filter(([u]) => String(u) === '/api/suite/apps');
  expect(calls).toHaveLength(1);
});

it('leaves the rail out when the list fails', async () => {
  stubServer(async () => json({ logged_out: true }), undefined, async () => json({ error: 'nope' }, 500));
  render(<App />);
  await screen.findByRole('heading', { name: 'Bob' });
  await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([u]) => String(u) === '/api/suite/apps')).toBe(true));
  expect(screen.queryByRole('navigation', { name: 'Apps' })).toBeNull();
});
