import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { Agents, expiresInDays } from './Agents';
import { ConfirmItsYou } from '../components/ConfirmItsYou';

const DAY = 24 * 60 * 60 * 1000;
const ago = (days: number) => new Date(Date.now() - days * DAY - 60_000).toISOString();
const ALICE = { id: 'u-alice', username: 'alice', display_name: 'Alice Doe', mxid: '@alice:example.com', status: 'active' };
const BOB = { id: 'u-bob', username: 'bob', display_name: 'Bob Roe', mxid: '@bob:example.com', status: 'inactive' };
const HELPER = {
  id: '6f1c2a3b-0000-4000-8000-000000000001', username: 'agent-helper', mxid: '@agent-helper:example.com', device_id: 'DEVHELPER0000001',
  owner: ALICE, token_state: 'active', locked: false, last_used_at: ago(80), created_at: ago(200),
};
const OLD = { ...HELPER, id: '6f1c2a3b-0000-4000-8000-000000000002', username: 'agent-old', mxid: '@agent-old:example.com', device_id: 'DEVOLD',
  owner: BOB, token_state: 'expired', locked: true, last_used_at: null };
const ODD = { ...HELPER, id: '6f1c2a3b-0000-4000-8000-000000000003', username: 'agent-odd', mxid: '@agent-odd:example.com', device_id: 'DEVODD',
  last_used_at: null, last_used_error: 'MAS: HTTP 502' };
const LIST = { agents: [HELPER, OLD, ODD], owners: [ALICE], idle_days: 90 };
const TOKEN = 'mct_SECRETTOKEN123';
const ISSUED = { agent: { ...HELPER, last_used_at: null, created_at: ago(0) }, token: TOKEN, configs: { openclaw: `accessToken: "${TOKEN}"`, hermes: `MATRIX_ACCESS_TOKEN=${TOKEN}` } };
const STEP_UP = { error: "Confirm it's you: this change needs a sign-in from the last 10 minutes", code: 'reauthentication_required' };
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });

type Call = { method: string; url: string; body?: unknown };
function serve(route: (url: string, init?: RequestInit) => Response | undefined) {
  const calls: Call[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ method: init?.method ?? 'GET', url, body: init?.body });
    const res = route(url, init);
    if (!res) throw new Error(`unexpected fetch ${url}`);
    return res;
  }));
  return calls;
}
const list = (url: string, init?: RequestInit) => (url === '/api/admin/agents' && !init?.method ? json(LIST) : undefined);
const posts = (calls: Call[]) => calls.filter((c) => c.method !== 'GET');
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe('expiresInDays', () => {
  const now = Date.parse('2026-10-14T12:00:00Z');
  const since = (days: number) => new Date(now - days * DAY).toISOString();
  it('warns from 76 idle days and counts down to 0', () => {
    expect(expiresInDays(since(75), 90, now)).toBeNull();
    expect(expiresInDays(since(76), 90, now)).toBe(14);
    expect(expiresInDays(since(89), 90, now)).toBe(1);
    expect(expiresInDays(since(90), 90, now)).toBe(0);
  });
});

describe('Agents', () => {
  it('lists agents with owner, device, last use, token state, lock and the idle warning', async () => {
    serve(list);
    render(<Agents />);
    const table = await screen.findByRole('table');
    expect(within(table).getByText('@agent-helper:example.com')).toBeTruthy();
    expect(within(table).getAllByText('Alice Doe')).toHaveLength(2);
    expect(within(table).getByText('DEVHELPER0000001')).toBeTruthy();
    expect(within(table).getByText('Expires in 10 days unless used')).toBeTruthy();
    expect(within(table).getByText('Expired')).toBeTruthy();
    expect(within(table).getByText('Locked')).toBeTruthy();
    expect(within(table).getByText('(inactive)')).toBeTruthy();
    expect(within(table).getByText('never')).toBeTruthy();
    expect(within(table).getByText('unknown')).toBeTruthy();
    expect(within(table).getByText('MAS: HTTP 502')).toBeTruthy();
    // Revoke only for a live token; the expired agent has none.
    expect(within(table).getAllByRole('button', { name: /^Revoke/ })).toHaveLength(2);
    expect(screen.getByRole('link', { name: 'Download skill' }).getAttribute('href')).toBe('/api/admin/agents/skill');
  });

  it('says when chat is not set up', async () => {
    serve(() => json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404));
    render(<Agents />);
    expect(await screen.findByText('Chat (Matrix) is not set up on this server.')).toBeTruthy();
  });

  it('refuses a malformed list', async () => {
    serve(() => json({ ...LIST, agents: [{ ...HELPER, token_state: 'forever' }] }));
    render(<Agents />);
    expect((await screen.findByRole('alert')).textContent).toContain('Invalid response from the server');
    expect(screen.queryByText('@agent-helper:example.com')).toBeNull();
  });

  it('shows the warnings before creating; Cancel sends nothing', async () => {
    const calls = serve(list);
    render(<Agents />);
    const name = await screen.findByRole('textbox', { name: 'Name' });
    const submit = screen.getByRole('button', { name: 'Create agent…' }) as HTMLButtonElement;
    fireEvent.change(name, { target: { value: 'agent-Bad!' } });
    fireEvent.change(screen.getByRole('combobox', { name: 'Owner' }), { target: { value: 'u-alice' } });
    expect(submit.disabled).toBe(true);
    fireEvent.change(name, { target: { value: 'agent-notes' } });
    fireEvent.click(submit);
    expect(screen.getByRole('alert').textContent).toContain('room content leaves your infrastructure');
    expect(screen.getByText(/join and answer only for its owner/)).toBeTruthy();
    expect(screen.getByText(/always starts with @agent-/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('button', { name: 'Create agent-notes' })).toBeNull();
    expect(posts(calls)).toEqual([]);
  });

  it('creates after step-up, shows the token once with both configs, and forgets it on close', async () => {
    let refused = false;
    const calls = serve((url, init) => {
      if (url === '/api/admin/agents' && init?.method === 'POST') {
        if (!refused) { refused = true; return json(STEP_UP, 403); }
        return json(ISSUED);
      }
      return list(url, init);
    });
    render(<><ConfirmItsYou /><Agents /></>);
    fireEvent.change(await screen.findByRole('textbox', { name: 'Name' }), { target: { value: 'agent-notes' } });
    fireEvent.change(screen.getByRole('combobox', { name: 'Owner' }), { target: { value: 'u-alice' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create agent…' }));
    fireEvent.click(screen.getByRole('button', { name: 'Create agent-notes' }));
    fireEvent.click(within(await screen.findByRole('dialog', { name: "Confirm it's you" })).getByRole('button', { name: 'Retry' }));
    const dialog = await screen.findByRole('dialog', { name: 'Token for @agent-helper:example.com' });
    expect(within(dialog).getByText('This token is shown once.', { exact: false })).toBeTruthy();
    expect(within(dialog).getByText(TOKEN)).toBeTruthy();
    expect(within(dialog).getByText(`accessToken: "${TOKEN}"`)).toBeTruthy();
    expect(within(dialog).getByText(`MATRIX_ACCESS_TOKEN=${TOKEN}`)).toBeTruthy();
    expect(within(dialog).getByRole('link', { name: 'Download skill' }).getAttribute('href')).toBe('/api/admin/agents/skill');
    expect(within(dialog).getByText('openclaw matrix encryption setup')).toBeTruthy();
    const creates = calls.filter((c) => c.method === 'POST');
    expect(creates).toHaveLength(2);
    expect(creates[1].body).toBe(JSON.stringify({ name: 'agent-notes', owner: 'u-alice' }));
    expect(calls.every((c) => !c.url.includes(TOKEN))).toBe(true);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Done' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(document.body.textContent).not.toContain(TOKEN);
  });

  it('shows a create refusal', async () => {
    serve((url, init) => (init?.method === 'POST' ? json({ error: 'That Matrix name is taken' }, 409) : list(url, init)));
    render(<Agents />);
    fireEvent.change(await screen.findByRole('textbox', { name: 'Name' }), { target: { value: 'agent-notes' } });
    fireEvent.change(screen.getByRole('combobox', { name: 'Owner' }), { target: { value: 'u-alice' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create agent…' }));
    fireEvent.click(screen.getByRole('button', { name: 'Create agent-notes' }));
    expect(await screen.findByText('That Matrix name is taken')).toBeTruthy();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('rotates into the token dialog', async () => {
    const calls = serve((url, init) => (url === `/api/admin/agents/${HELPER.id}/rotate` && init?.method === 'POST' ? json(ISSUED) : list(url, init)));
    render(<Agents />);
    fireEvent.click(await screen.findByRole('button', { name: 'Rotate token of agent-helper' }));
    const dialog = await screen.findByRole('dialog', { name: 'Token for @agent-helper:example.com' });
    expect(within(dialog).getByText(TOKEN)).toBeTruthy();
    expect(posts(calls).map((c) => c.url)).toEqual([`/api/admin/agents/${HELPER.id}/rotate`]);
  });

  it('refuses a malformed token reply without showing anything of it', async () => {
    serve((url, init) => (init?.method === 'POST' ? json({ ...ISSUED, configs: 'x' }) : list(url, init)));
    render(<Agents />);
    fireEvent.click(await screen.findByRole('button', { name: 'Rotate token of agent-helper' }));
    expect((await screen.findByRole('alert')).textContent).toBe('Invalid response from the server');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(document.body.textContent).not.toContain(TOKEN);
  });

  it('revokes in one click and reloads', async () => {
    const calls = serve((url, init) => (url === `/api/admin/agents/${HELPER.id}/revoke` && init?.method === 'POST' ? json({ outcome: 'revoked' }) : list(url, init)));
    render(<Agents />);
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke token of agent-helper' }));
    expect(await screen.findByText('Revoked the token of @agent-helper:example.com.')).toBeTruthy();
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET')).toHaveLength(2));
    expect(posts(calls).map((c) => c.url)).toEqual([`/api/admin/agents/${HELPER.id}/revoke`]);
  });

  it('deletes only after the inline confirm', async () => {
    const calls = serve((url, init) => (url === `/api/admin/agents/${HELPER.id}` && init?.method === 'DELETE' ? json({ outcome: 'deleted' }) : list(url, init)));
    render(<Agents />);
    fireEvent.click(await screen.findByRole('button', { name: 'Delete agent-helper' }));
    const confirm = screen.getByRole('group', { name: 'Confirm delete' });
    expect(confirm.textContent).toContain('messages stay');
    fireEvent.click(within(confirm).getByRole('button', { name: 'Cancel' }));
    expect(posts(calls)).toEqual([]);
    fireEvent.click(screen.getByRole('button', { name: 'Delete agent-helper' }));
    fireEvent.click(screen.getByRole('button', { name: 'Delete agent' }));
    expect(await screen.findByText('Deleted @agent-helper:example.com.')).toBeTruthy();
    expect(posts(calls).map((c) => `${c.method} ${c.url}`)).toEqual([`DELETE /api/admin/agents/${HELPER.id}`]);
  });
});
