import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { Meet, nextURL, parseMeetStatus } from './Meet';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.location.hash = ''; });

describe('Meet', () => {
  it('reads every answer of the status route', () => {
    expect(parseMeetStatus(404, { code: 'not_found' })).toEqual({ kind: 'invalid' });
    expect(parseMeetStatus(200, { state: 'open' })).toEqual({ kind: 'open' });
    expect(parseMeetStatus(200, { state: 'waiting', opens_at: '2026-11-15T09:50:00Z' })).toEqual({ kind: 'waiting', opens: '2026-11-15T09:50:00Z' });
    expect(parseMeetStatus(200, { state: 'ended' })).toEqual({ kind: 'ended' });
    expect(parseMeetStatus(200, { state: '<img src=x>' }).kind).toBe('error');
    expect(parseMeetStatus(429, { error: 'Too many attempts; wait a few minutes' })).toEqual({ kind: 'error', message: 'Too many attempts; wait a few minutes' });
  });

  it('follows only an https next address', () => {
    expect(nextURL({ next: 'https://chat.example.com/kymessages-guest.html#abc' })).toBe('https://chat.example.com/kymessages-guest.html#abc');
    for (const next of ['javascript:alert(1)', 'http://chat.example.com/x', '/relative', '', 42]) expect(nextURL({ next })).toBeNull();
  });

  it('sends the token from the fragment, never the URL path or query', async () => {
    window.location.hash = '#' + 'A'.repeat(43);
    const bodies: string[] = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      bodies.push(`${url} ${String(init?.body)}`);
      return json({ state: 'ended' });
    }));
    render(<Meet />);
    expect(await screen.findByText('This meeting has ended.')).toBeTruthy();
    expect(bodies).toEqual([`/api/meet/status {"token":"${'A'.repeat(43)}"}`]);
  });

  it('says a link is not valid', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ code: 'not_found', error: "This link isn't valid" }, 404)));
    render(<Meet />);
    expect(await screen.findByText(/This link isn't valid. Ask the organizer/)).toBeTruthy();
    expect(screen.queryByRole('textbox')).toBeNull();
  });

  it('asks for a name inside a window', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => url === '/api/meet/status' ? json({ state: 'open' }) : json({}, 500)));
    render(<Meet />);
    expect(await screen.findByLabelText('Your name')).toBeTruthy();
    expect((screen.getByRole('button', { name: /Ask to join/ }) as HTMLButtonElement).disabled).toBe(true);
  });
});
