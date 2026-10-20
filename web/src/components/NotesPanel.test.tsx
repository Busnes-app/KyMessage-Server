import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { NOTES_WARNINGS, NotesPanel } from './NotesPanel';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const OFF = { available: true, enabled: false, user: '@kymessages-notes:example.com', checkin: '2026-10-20T10:00:00Z' };
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function stub(states: unknown[]) {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url === '/api/admin/notes') return json(states.length > 1 ? states.shift() : states[0]);
    return json({ outcome: 'ok' });
  }));
  return calls;
}

describe('NotesPanel', () => {
  it('renders nothing without chat', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ code: 'matrix_disabled', error: 'off' }, 404)));
    const { container } = render(<NotesPanel />);
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    expect(container.innerHTML).toBe('');
  });

  it('says how to set it up when the secret is missing', async () => {
    stub([{ ...OFF, available: false }]);
    render(<NotesPanel />);
    expect(await screen.findByText(/Meeting notes are not set up/)).toBeTruthy();
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('turns on only after the warnings, and Cancel sends nothing', async () => {
    const calls = stub([OFF, { ...OFF, enabled: true }]);
    render(<NotesPanel />);
    fireEvent.click(await screen.findByRole('button', { name: 'Turn on…' }));
    for (const w of NOTES_WARNINGS) expect(screen.getByText(w)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(calls).toEqual(['GET /api/admin/notes']);
    fireEvent.click(screen.getByRole('button', { name: 'Turn on…' }));
    fireEvent.click(screen.getByRole('button', { name: 'Turn on' }));
    expect(await screen.findByText('On')).toBeTruthy();
    expect(calls).toContain('POST /api/admin/notes/enable');
  });

  it('turns off in one click and shows a bot that never checked in', async () => {
    const calls = stub([{ ...OFF, enabled: true, checkin: '' }, OFF]);
    render(<NotesPanel />);
    expect(await screen.findByText(/Has not checked in/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Turn off' }));
    expect(await screen.findByText('Off')).toBeTruthy();
    expect(calls).toContain('POST /api/admin/notes/disable');
  });

  it('shows a refused change', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => (url === '/api/admin/notes' ? json(OFF) : json({ error: 'Could not prepare the notes account' }, 502))));
    render(<NotesPanel />);
    fireEvent.click(await screen.findByRole('button', { name: 'Turn on…' }));
    fireEvent.click(screen.getByRole('button', { name: 'Turn on' }));
    expect(await screen.findByText('Could not prepare the notes account')).toBeTruthy();
  });
});
