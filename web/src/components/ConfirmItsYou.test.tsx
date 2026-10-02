import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { ConfirmItsYou } from './ConfirmItsYou';
import { adminFetch } from '../api';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const STEP_UP = { error: "Confirm it's you: this change needs a sign-in from the last 10 minutes", code: 'reauthentication_required', reauth_url: '/api/sso/kyidentity/login?fresh=1' };
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

/** fetch answers each path from its queue, repeating the last answer. */
function answers(byPath: Record<string, Response[]>) {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    const q = byPath[url];
    return (q.length > 1 ? q.shift() : q[0])!.clone();
  }));
  return calls;
}

it('without a mounted prompt, returns the refusal', async () => {
  answers({ '/x': [json(STEP_UP, 403)] });
  expect((await adminFetch('/x', { method: 'POST' })).status).toBe(403);
});

it('passes through a 403 that is not a step-up', async () => {
  answers({ '/x': [json({ error: 'Administrator role required' }, 403)] });
  render(<ConfirmItsYou />);
  expect((await adminFetch('/x', { method: 'POST' })).status).toBe(403);
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('retries the request once the admin has signed in again', async () => {
  const calls = answers({ '/x': [json(STEP_UP, 403), json({ ok: true })] });
  render(<ConfirmItsYou />);
  const pending = adminFetch('/x', { method: 'POST' });
  const dialog = await screen.findByRole('dialog', { name: "Confirm it's you" });
  const link = within(dialog).getByRole('link', { name: 'Sign in to KyIdentity again' });
  expect(link.getAttribute('href')).toBe('/api/sso/kyidentity/login?fresh=1');
  expect(link.getAttribute('target')).toBe('_blank');
  expect(link.getAttribute('rel')).toBe('noopener noreferrer');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Retry' }));
  expect((await pending).status).toBe(200);
  expect(calls).toEqual(['/x', '/x']);
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('returns the refusal when the admin cancels', async () => {
  answers({ '/x': [json(STEP_UP, 403)] });
  render(<ConfirmItsYou />);
  const pending = adminFetch('/x', { method: 'POST' });
  fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }));
  expect((await pending).status).toBe(403);
});

it('offers no link for a reauth_url off the suite sign-in route', async () => {
  answers({ '/x': [json({ ...STEP_UP, reauth_url: 'https://evil.example/' }, 403)] });
  render(<ConfirmItsYou />);
  void adminFetch('/x', { method: 'POST' });
  const dialog = await screen.findByRole('dialog');
  expect(within(dialog).queryByRole('link')).toBeNull();
  expect(dialog.textContent).toContain('sign out and sign in again');
});

it('answers concurrent refusals with one prompt', async () => {
  answers({ '/a': [json(STEP_UP, 403), json({ ok: 1 })], '/b': [json(STEP_UP, 403), json({ ok: 2 })] });
  render(<ConfirmItsYou />);
  const a = adminFetch('/a', { method: 'POST' });
  const b = adminFetch('/b', { method: 'POST' });
  await screen.findByRole('dialog');
  // Both refusals are mocked and parse within a few ticks; 50 ms lets the second join the open prompt.
  await new Promise((r) => setTimeout(r, 50));
  expect(screen.getAllByRole('dialog')).toHaveLength(1);
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
  expect((await a).status).toBe(200);
  expect((await b).status).toBe(200);
});

it('never leaves a refusal hanging when the answer lands while it is joining', async () => {
  answers({ '/a': [json(STEP_UP, 403)], '/b': [json(STEP_UP, 403)] });
  render(<ConfirmItsYou />);
  const a = adminFetch('/a', { method: 'POST' });
  await screen.findByRole('dialog');
  const b = adminFetch('/b', { method: 'POST' });
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
  expect((await a).status).toBe(403);
  // b either joined the answer or opened its own prompt; cancelling that settles it too.
  const settled = await Promise.race([b, screen.findByRole('dialog').then((d) => { fireEvent.click(within(d).getByRole('button', { name: 'Cancel' })); return b; })]);
  expect(settled.status).toBe(403);
});
