import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { GUESTS_WARNINGS, GuestsPanel } from './GuestsPanel';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function stub(states: unknown[]) {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url === '/api/admin/meeting-guests') return json(states.length > 1 ? states.shift() : states[0]);
    return json({ outcome: 'ok' });
  }));
  return calls;
}

describe('GuestsPanel', () => {
  it('renders nothing without chat', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ code: 'matrix_disabled', error: 'off' }, 404)));
    const { container } = render(<GuestsPanel />);
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    expect(container.innerHTML).toBe('');
  });

  it('is off until the warnings are accepted, and Cancel sends nothing', async () => {
    const calls = stub([{ enabled: false, live: 0 }, { enabled: true, live: 0 }]);
    render(<GuestsPanel />);
    expect(await screen.findByText('Off')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Turn on…' }));
    for (const w of GUESTS_WARNINGS) expect(screen.getByText(w)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(calls).toEqual(['GET /api/admin/meeting-guests']);
    fireEvent.click(screen.getByRole('button', { name: 'Turn on…' }));
    fireEvent.click(screen.getByRole('button', { name: 'Turn on' }));
    expect(await screen.findByText('On')).toBeTruthy();
    expect(calls).toContain('POST /api/admin/meeting-guests/enable');
    expect(screen.getByText('0 guests are in meetings now.')).toBeTruthy();
  });

  it('turns off in one click', async () => {
    const calls = stub([{ enabled: true, live: 1 }, { enabled: false, live: 0 }]);
    render(<GuestsPanel />);
    expect(await screen.findByText('1 guest is in a meeting now.')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Turn off' }));
    expect(await screen.findByText('Off')).toBeTruthy();
    expect(calls).toContain('POST /api/admin/meeting-guests/disable');
  });
});
