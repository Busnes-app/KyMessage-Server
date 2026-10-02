import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen, within } from '@testing-library/react';
import { Health } from './Health';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const NET = { peer_ip: '10.0.0.1', client_ip: '203.0.113.9', forwarded_trusted: true, forwarded_proto: 'https', app_url_https: true, host_matches: true, trusted_proxies_narrow: true };
const c = (name: string, extra: Record<string, unknown> = {}) => ({ name, status: 'up', version: '', pinned: '', mismatch: false, source: '', error: '', ...extra });
const REPORT = { matrix: true, components: [
  c('kymessages', { version: '0.1.0-dev' }),
  c('database'),
  c('synapse', { version: '1.162.0', pinned: '1.162.0', source: 'https://github.com/element-hq/synapse/tree/v1.162.0' }),
  c('element', { version: '1.12.29', pinned: '1.12.30', mismatch: true, source: 'javascript:alert(1)' }),
  c('postgres', { status: 'down', pinned: '17.6', error: 'dial tcp: lookup postgres: no such host' }),
] };
function serve(health: Response) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => (String(input) === '/api/admin/health' ? health.clone() : json(NET))));
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('shows each component, its pin, its source and the network check', async () => {
  serve(json(REPORT));
  render(<Health />);
  const list = await screen.findByRole('region', { name: 'Components' });
  expect(within(list).getAllByText('Up')).toHaveLength(4);
  expect(within(list).getByText('Down')).toBeTruthy();
  expect(within(list).getByRole('link', { name: /Source for 1\.162\.0/ }).getAttribute('href')).toBe('https://github.com/element-hq/synapse/tree/v1.162.0');
  expect(within(list).getByText(/Compose pins 1\.12\.30/)).toBeTruthy();
  expect(within(list).queryByRole('link', { name: /Source for 1\.12\.29/ })).toBeNull();
  expect(within(list).getByText('dial tcp: lookup postgres: no such host')).toBeTruthy();
  expect(await screen.findByRole('heading', { name: 'Network path' })).toBeTruthy();
});

it('says when only KyMessages is checked', async () => {
  serve(json({ matrix: false, components: [c('kymessages', { version: '0.1.0-dev' }), c('database')] }));
  render(<Health />);
  expect(await screen.findByText(/only KyMessages and its database are checked/)).toBeTruthy();
});

it('refuses a malformed report', async () => {
  serve(json({ matrix: true, components: [c('synapse', { status: 'sideways' })] }));
  render(<Health />);
  expect((await screen.findByRole('alert')).textContent).toContain('Invalid response from the server');
});

it('drops an https source outside the upstream repos and shows odd versions as text', async () => {
  serve(json({ matrix: true, components: [c('synapse', { version: '<b>x</b>', source: 'https://evil.example/element-hq/synapse/tree/v1' })] }));
  render(<Health />);
  expect(await screen.findByText('<b>x</b>')).toBeTruthy();
  expect(screen.queryByRole('link')).toBeNull();
});

it('names Synapse admin access and shows why it is down, without a version', async () => {
  serve(json({ matrix: true, components: [c('synapse-admin', { status: 'down', error: 'console account is locked in MAS' })] }));
  render(<Health />);
  const list = await screen.findByRole('region', { name: 'Components' });
  expect(within(list).getByText('Synapse admin access (console)')).toBeTruthy();
  expect(within(list).getByText('console account is locked in MAS')).toBeTruthy();
  expect(within(list).queryByText('Version unknown')).toBeNull();
});
