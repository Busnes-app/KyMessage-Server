import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { Rooms } from './Rooms';
import { ConfirmItsYou } from '../components/ConfirmItsYou';

const GRP = '!grp:example.com';
const ENC = encodeURIComponent(GRP);
const ROOM = { id: GRP, name: 'Team chat', alias: '', creator: '@alice:example.com', members: 2, encrypted: true, public: false, join_rule: 'invite', state_events: 12, closed: false };
const DM = { ...ROOM, id: '!dm:example.com', name: '', closed: true };
const PAGE = { rooms: [ROOM, DM], total: 2, offset: 0, limit: 50 };
const DETAIL = { room: ROOM, members: ['@alice:example.com', '@bob:example.com'], members_total: 2, confirm_text: 'Team chat', jobs: [] };
const STEP_UP = { error: "Confirm it's you: this change needs a sign-in from the last 10 minutes", code: 'reauthentication_required' };
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });

function serve(route: (url: string, init?: RequestInit) => Response | undefined) {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    const res = route(url, init);
    if (!res) throw new Error(`unexpected fetch ${url}`);
    return res;
  }));
  return calls;
}
const list = (url: string) => (url.startsWith('/api/admin/matrix/rooms?') ? json(PAGE) : undefined);
const detail = (url: string) => (url === `/api/admin/matrix/rooms/${ENC}` ? json(DETAIL) : undefined);
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

async function openRoom() {
  fireEvent.click(await screen.findByRole('button', { name: 'Details for Team chat' }));
  const panel = await screen.findByRole('region', { name: 'Team chat' });
  await within(panel).findByRole('list', { name: 'Members' });
  return panel;
}

describe('Rooms', () => {
  it('lists rooms with encryption, access, size and status', async () => {
    const calls = serve(list);
    render(<Rooms />);
    const table = await screen.findByRole('table');
    expect(within(table).getByText('Team chat')).toBeTruthy();
    expect(within(table).getByText('Unnamed room')).toBeTruthy();
    expect(within(table).getAllByText('Encryption on')).toHaveLength(2);
    expect(within(table).getAllByText('Invite only')).toHaveLength(2);
    expect(within(table).getByText('Closed')).toBeTruthy();
    expect(within(table).getByRole('columnheader', { name: 'State events' })).toBeTruthy();
    expect(screen.getByText(/Messages are never shown here/)).toBeTruthy();
    expect(calls[0]).toBe('GET /api/admin/matrix/rooms?search=&offset=0&limit=50');
  });

  it('searches and pages', async () => {
    const calls = serve((url) => (url.startsWith('/api/admin/matrix/rooms?') ? json({ ...PAGE, total: 120 }) : undefined));
    render(<Rooms />);
    await screen.findByText('Team chat');
    fireEvent.change(screen.getByLabelText('Search by name or room ID'), { target: { value: ' team ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Search' }));
    await waitFor(() => expect(calls).toContain('GET /api/admin/matrix/rooms?search=team&offset=0&limit=50'));
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(calls).toContain('GET /api/admin/matrix/rooms?search=team&offset=50&limit=50'));
  });

  it('says when chat is not set up', async () => {
    serve(() => json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404));
    render(<Rooms />);
    expect(await screen.findByText('Chat (Matrix) is not set up on this server.')).toBeTruthy();
  });

  it('says why Synapse admin access is not working', async () => {
    serve(() => json({ error: 'Synapse admin access is not working: console account is locked in MAS' }, 502));
    render(<Rooms />);
    expect((await screen.findByRole('alert')).textContent).toContain('console account is locked in MAS');
  });

  it('refuses a malformed page', async () => {
    serve(() => json({ ...PAGE, rooms: [{ ...ROOM, closed: 'yes' }] }));
    render(<Rooms />);
    expect((await screen.findByRole('alert')).textContent).toContain('Invalid response from the server');
    expect(screen.queryByText('Team chat')).toBeNull();
  });

  it('shows a hostile room name as text', async () => {
    const name = '<img src=x onerror=alert(1)>';
    serve(() => json({ ...PAGE, rooms: [{ ...ROOM, name }] }));
    render(<Rooms />);
    expect(await screen.findByText(name)).toBeTruthy();
    expect(document.querySelector('img')).toBeNull();
  });

  it('closes a room after confirming, then polls until Synapse finishes', async () => {
    let polls = 0;
    const calls = serve((url, init) => {
      if (url === `/api/admin/matrix/rooms/${ENC}/close` && init?.method === 'POST') return json({ outcome: 'started', delete_id: 'D1' });
      if (url === `/api/admin/matrix/rooms/${ENC}/delete-status`) {
        polls++;
        return json({ jobs: [{ delete_id: 'D1', status: polls < 5 ? 'active' : 'complete' }] });
      }
      return detail(url) ?? list(url);
    });
    render(<><ConfirmItsYou /><Rooms pollMs={20} /></>);
    const panel = await openRoom();
    expect(within(panel).getByRole('list', { name: 'Members' }).textContent).toContain('@bob:example.com');
    fireEvent.click(within(panel).getByRole('button', { name: 'Close room…' }));
    const confirm = within(panel).getByRole('group', { name: 'Confirm close' });
    expect(confirm.textContent).toContain('cannot be reopened');
    fireEvent.click(within(confirm).getByRole('button', { name: 'Close room' }));
    expect(await within(panel).findByText('Closing…')).toBeTruthy();
    expect(await screen.findByText(/Room closed: everyone was removed/)).toBeTruthy();
    expect(screen.queryByRole('region', { name: 'Team chat' })).toBeNull();
    expect(polls).toBe(5);
    expect(calls.filter((c) => c.startsWith('GET /api/admin/matrix/rooms?'))).toHaveLength(2);
  });

  it('retries a close after the admin confirms it is them', async () => {
    const posts: string[] = [];
    serve((url, init) => {
      if (init?.method === 'POST') { posts.push(url); return posts.length === 1 ? json(STEP_UP, 403) : json({ outcome: 'already_closed', delete_id: '' }); }
      return detail(url) ?? list(url);
    });
    render(<><ConfirmItsYou /><Rooms pollMs={20} /></>);
    const panel = await openRoom();
    fireEvent.click(within(panel).getByRole('button', { name: 'Close room…' }));
    fireEvent.click(within(within(panel).getByRole('group', { name: 'Confirm close' })).getByRole('button', { name: 'Close room' }));
    fireEvent.click(within(await screen.findByRole('dialog', { name: "Confirm it's you" })).getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('The room was already closed.')).toBeTruthy();
    expect(posts).toHaveLength(2);
  });

  it('deletes only after the exact room name is typed, and sends it for the server to check', async () => {
    const bodies: string[] = [];
    serve((url, init) => {
      if (url === `/api/admin/matrix/rooms/${ENC}/delete` && init?.method === 'POST') { bodies.push(String(init.body)); return json({ outcome: 'started', delete_id: 'D2' }); }
      if (url === `/api/admin/matrix/rooms/${ENC}/delete-status`) return json({ jobs: [{ delete_id: 'D2', status: 'complete' }] });
      return detail(url) ?? list(url);
    });
    render(<Rooms pollMs={20} />);
    const panel = await openRoom();
    const button = within(panel).getByRole('button', { name: 'Delete permanently' }) as HTMLButtonElement;
    const input = within(panel).getByLabelText(/to delete this room permanently/);
    expect(within(panel).getByText(/encrypted rooms cannot be found by the server/)).toBeTruthy();
    expect(button.disabled).toBe(true);
    fireEvent.change(input, { target: { value: 'team chat' } });
    expect(button.disabled).toBe(true);
    fireEvent.change(input, { target: { value: 'Team chat' } });
    expect(button.disabled).toBe(false);
    fireEvent.click(button);
    expect(await screen.findByText('Room deleted permanently.')).toBeTruthy();
    expect(bodies).toEqual(['{"confirm":"Team chat"}']);
  });

  it('shows the server refusing a delete', async () => {
    serve((url, init) => {
      if (init?.method === 'POST') return json({ error: "Type the room's name exactly as shown to delete it" }, 400);
      return detail(url) ?? list(url);
    });
    render(<Rooms pollMs={20} />);
    const panel = await openRoom();
    fireEvent.change(within(panel).getByLabelText(/to delete this room permanently/), { target: { value: 'Team chat' } });
    fireEvent.click(within(panel).getByRole('button', { name: 'Delete permanently' }));
    expect((await within(panel).findByRole('alert')).textContent).toContain('exactly as shown');
  });

  it('disables changes while a job already runs and follows it', async () => {
    serve((url) => {
      if (url === `/api/admin/matrix/rooms/${ENC}`) return json({ ...DETAIL, jobs: [{ delete_id: 'D0', status: 'active' }] });
      if (url.endsWith('/delete-status')) return json({ jobs: [{ delete_id: 'D0', status: 'active' }] });
      return list(url);
    });
    render(<Rooms pollMs={10000} />);
    const panel = await openRoom();
    expect(within(panel).getByText('A close or delete of this room is running…')).toBeTruthy();
    expect((within(panel).getByRole('button', { name: 'Close room…' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(panel).getByLabelText(/to delete this room permanently/), { target: { value: 'Team chat' } });
    expect((within(panel).getByRole('button', { name: 'Delete permanently' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('reports a failed job', async () => {
    serve((url, init) => {
      if (init?.method === 'POST') return json({ outcome: 'started', delete_id: 'D3' });
      if (url.endsWith('/delete-status')) return json({ jobs: [{ delete_id: 'D3', status: 'failed' }] });
      return detail(url) ?? list(url);
    });
    render(<Rooms pollMs={20} />);
    const panel = await openRoom();
    fireEvent.click(within(panel).getByRole('button', { name: 'Close room…' }));
    fireEvent.click(within(within(panel).getByRole('group', { name: 'Confirm close' })).getByRole('button', { name: 'Close room' }));
    expect((await within(panel).findByRole('alert')).textContent).toContain('Synapse reports the job as failed');
    expect(within(panel).queryByText('Closing…')).toBeNull();
  });

  it('stops polling a job Synapse never lists', async () => {
    let polls = 0;
    serve((url, init) => {
      if (init?.method === 'POST') return json({ outcome: 'started', delete_id: 'D4' });
      if (url.endsWith('/delete-status')) { polls++; return json({ jobs: [] }); }
      return detail(url) ?? list(url);
    });
    render(<Rooms pollMs={1} />);
    const panel = await openRoom();
    fireEvent.click(within(panel).getByRole('button', { name: 'Close room…' }));
    fireEvent.click(within(within(panel).getByRole('group', { name: 'Confirm close' })).getByRole('button', { name: 'Close room' }));
    expect((await within(panel).findByRole('alert', {}, { timeout: 3000 })).textContent).toContain('has not finished yet');
    const stopped = polls;
    await new Promise((r) => setTimeout(r, 30));
    expect(polls).toBe(stopped);
    expect(stopped).toBe(90);
  });
});
