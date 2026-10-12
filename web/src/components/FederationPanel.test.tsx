import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { FederationPanel, parseFederation, parseStatuses, wideningWarnings } from './FederationPanel';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const CLOSED = { level: 'closed', server_name: 'example.com', servers: [], presets: [], restart_hint: 'docker compose restart synapse' };
const PRESET = { name: 'public.example', operator: 'Public Org', open_signup: true, source: 'https://public.example/', checked: '2026-10-11', ticked: false };

function serve(route: (url: string, init?: RequestInit) => Response | undefined) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init });
    const res = route(url, init);
    if (!res) throw new Error(`unexpected fetch ${init?.method ?? 'GET'} ${url}`);
    return res;
  }));
  return calls;
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe('parsing', () => {
  it('reads the view and refuses unknown levels and statuses', () => {
    const f = parseFederation({ ...CLOSED, servers: [{ name: 'partner.example' }], presets: [PRESET] });
    expect(f.servers).toEqual(['partner.example']);
    expect(f.presets[0].open_signup).toBe(true);
    expect(f.file_error).toBe('');
    expect(() => parseFederation({ ...CLOSED, level: 'open' })).toThrow();
    expect(() => parseFederation({ ...CLOSED, servers: ['partner.example'] })).toThrow();
    expect(parseStatuses({ statuses: { 'a.example': 'connected' } })).toEqual({ 'a.example': 'connected' });
    expect(() => parseStatuses({ statuses: { 'a.example': 'great' } })).toThrow();
  });
  it('warns about public servers only at the curated level', () => {
    expect(wideningWarnings('closed')).toEqual([]);
    expect(wideningWarnings('manual').some((w) => w.includes('public servers'))).toBe(false);
    expect(wideningWarnings('curated').some((w) => w.includes('public servers'))).toBe(true);
    expect(wideningWarnings('manual').some((w) => w.includes('not been independently tested'))).toBe(true);
  });
});

describe('FederationPanel', () => {
  it('renders nothing when chat is not set up', async () => {
    serve(() => json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404));
    const { container } = render(<FederationPanel />);
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    expect(container.innerHTML).toBe('');
  });

  it('shows the warnings before widening and sends nothing on Cancel', async () => {
    const calls = serve((url, init) => {
      if (url === '/api/admin/federation' && !init?.method) return json(CLOSED);
      if (url === '/api/admin/federation/level' && init?.method === 'PUT') return json({ ...CLOSED, level: 'manual' });
      if (url === '/api/admin/federation/status') return json({ statuses: {} });
      return undefined;
    });
    render(<FederationPanel />);
    fireEvent.click(await screen.findByRole('radio', { name: /Manual approval/ }));
    expect(screen.getByRole('alert').textContent).toContain('Offboarding someone here does not remove');
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(calls.some((c) => c.init?.method === 'PUT')).toBe(false);
    expect((screen.getByRole('radio', { name: /Closed/ }) as HTMLInputElement).checked).toBe(true);

    fireEvent.click(screen.getByRole('radio', { name: /Manual approval/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Allow Manual approval' }));
    await screen.findByText('Federation set to Manual approval.');
    const put = calls.find((c) => c.init?.method === 'PUT');
    expect(put?.init?.body).toBe(JSON.stringify({ level: 'manual' }));
    expect(screen.getByText('docker compose restart synapse')).toBeTruthy();
  });

  it('closes at once, without the warnings', async () => {
    const calls = serve((url, init) => {
      if (url === '/api/admin/federation' && !init?.method) return json({ ...CLOSED, level: 'manual', servers: [{ name: 'partner.example' }] });
      if (url === '/api/admin/federation/status') return json({ statuses: { 'partner.example': 'connected' } });
      if (url === '/api/admin/federation/level') return json({ ...CLOSED, servers: [{ name: 'partner.example' }] });
      return undefined;
    });
    render(<FederationPanel />);
    await screen.findByText('Connected');
    fireEvent.click(screen.getByRole('radio', { name: /Closed/ }));
    await screen.findByText('Federation set to Closed.');
    expect(calls.filter((c) => c.init?.method === 'PUT')).toHaveLength(1);
  });

  it('adds and removes servers and shows each status with its hint', async () => {
    let view = { ...CLOSED, level: 'manual', servers: [{ name: 'down.example' }] };
    const calls = serve((url, init) => {
      if (url === '/api/admin/federation' && !init?.method) return json(view);
      if (url === '/api/admin/federation/status') return json({ statuses: { 'down.example': 'unreachable', 'partner.example': 'not_allowed' } });
      if (url === '/api/admin/federation/servers' && init?.method === 'POST') {
        view = { ...view, servers: [...view.servers, { name: 'partner.example' }] };
        return json(view);
      }
      if (url === '/api/admin/federation/servers/down.example' && init?.method === 'DELETE') {
        view = { ...view, servers: [{ name: 'partner.example' }] };
        return json(view);
      }
      return undefined;
    });
    render(<FederationPanel />);
    expect(await screen.findByText('Unreachable')).toBeTruthy();
    fireEvent.change(screen.getByRole('textbox', { name: 'Add a server' }), { target: { value: ' Partner.Example ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add server' }));
    await screen.findByText('Added partner.example.');
    expect(calls.find((c) => c.init?.method === 'POST')?.init?.body).toBe(JSON.stringify({ name: ' Partner.Example ' }));
    fireEvent.click(screen.getByRole('button', { name: 'Check connections' }));
    expect(await screen.findByText('Not allowed yet')).toBeTruthy();
    expect(screen.getByText(/Both servers must list each other/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Remove down.example' }));
    await screen.findByText('Removed down.example.');
  });

  it('shows public servers only at the curated level, with an open sign-up badge', async () => {
    serve((url, init) => {
      if (url === '/api/admin/federation' && !init?.method) return json({ ...CLOSED, level: 'curated', presets: [PRESET] });
      if (url === '/api/admin/federation/status') return json({ statuses: {} });
      if (url === '/api/admin/federation/presets/public.example' && init?.method === 'PUT') return json({ ...CLOSED, level: 'curated', presets: [{ ...PRESET, ticked: true }] });
      return undefined;
    });
    render(<FederationPanel />);
    expect(await screen.findByText('Open sign-up')).toBeTruthy();
    fireEvent.click(screen.getByRole('checkbox'));
    await screen.findByText('Allowed public.example.');
    expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(true);
  });

  it('says so when no public servers are listed', async () => {
    serve((url, init) => {
      if (url === '/api/admin/federation' && !init?.method) return json({ ...CLOSED, level: 'curated' });
      if (url === '/api/admin/federation/status') return json({ statuses: {} });
      return undefined;
    });
    render(<FederationPanel />);
    expect(await screen.findByText('No public servers are listed in this release yet.')).toBeTruthy();
  });

  it('shows a file error and a refused change', async () => {
    serve((url, init) => {
      if (url === '/api/admin/federation' && !init?.method) return json({ ...CLOSED, level: 'manual', file_error: 'permission denied' });
      if (url === '/api/admin/federation/status') return json({ statuses: {} });
      if (url === '/api/admin/federation/servers') return json({ error: 'that is this server\'s own name' }, 400);
      return undefined;
    });
    render(<FederationPanel />);
    expect((await screen.findByText(/federation file was not updated/)).textContent).toContain('permission denied');
    fireEvent.change(screen.getByRole('textbox', { name: 'Add a server' }), { target: { value: 'example.com' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add server' }));
    expect(await screen.findByText("that is this server's own name")).toBeTruthy();
  });
});
