import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { App } from './App';
import { ErrorBoundary } from './components/ErrorBoundary';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const member = { username: 'bob', display_name: 'Bob', role: 'user', must_change_password: false };

function stubServer(logout: () => Promise<Response>, me: unknown = { authenticated: true, user: member }) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
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
