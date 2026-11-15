import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { KyCalendarPanel, KyPostPanel, parsePair } from './SuitePairPanel';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe('KyCalendarPanel', () => {
  it('shows a connected pair with its source and address', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ connected: true, source: 'kyidentity', url: 'https://cal.example.com', checked_at: '2026-11-14T10:00:00Z' })));
    render(<KyCalendarPanel />);
    expect(await screen.findByText('Connected')).toBeTruthy();
    expect(screen.getByText('KyIdentity')).toBeTruthy();
    expect(screen.getByText('https://cal.example.com')).toBeTruthy();
    expect(screen.getByText('Last checked')).toBeTruthy();
  });
  it('says why it is not connected', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ connected: false, reason: 'no_peer' })));
    render(<KyCalendarPanel />);
    expect(await screen.findByText('Not connected')).toBeTruthy();
    expect(screen.getByText('KyIdentity has no KyCalendar paired with KyMessages.')).toBeTruthy();
    expect(screen.queryByText('Address')).toBeNull();
  });
  it('renders nothing without chat', async () => {
    const f = vi.fn(async () => json({ connected: false, reason: 'matrix_off' }));
    vi.stubGlobal('fetch', f);
    const { container } = render(<KyCalendarPanel />);
    await vi.waitFor(() => expect(f).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(container.innerHTML).toBe('');
  });
  it('shows a load failure', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ error: 'boom' }, 500)));
    render(<KyCalendarPanel />);
    expect((await screen.findByRole('alert')).textContent).toContain('boom');
  });
  it('refuses a malformed reply', () => {
    expect(() => parsePair({ connected: 'yes' })).toThrow();
    expect(() => parsePair({ connected: true, url: 7 })).toThrow();
    expect(parsePair({ connected: false, reason: 'no_peer' })).toEqual({ connected: false, source: '', url: '', reason: 'no_peer', checked_at: '' });
  });
});

describe('KyPostPanel', () => {
  it('reads its own route and names its environment variable', async () => {
    const f = vi.fn(async (_url: string) => json({ connected: true, source: 'environment', url: 'https://mail.example.com' }));
    vi.stubGlobal('fetch', f);
    render(<KyPostPanel />);
    expect(await screen.findByText('KY_KYPOST_URL')).toBeTruthy();
    expect(f.mock.calls[0][0]).toBe('/api/admin/kypost');
    expect(screen.getByRole('region', { name: 'KyPost' })).toBeTruthy();
  });
  it('names KyPost in its reasons', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ connected: false, reason: 'invalid_peer' })));
    render(<KyPostPanel />);
    expect(await screen.findByText("KyIdentity's KyPost address is not an https origin.")).toBeTruthy();
  });
});
