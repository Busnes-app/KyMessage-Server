import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { NetworkCheck } from './NetworkCheck';

const ok = {peer_ip: '10.91.0.10', client_ip: '203.0.113.9', forwarded_trusted: true, forwarded_proto: 'https', app_url_https: true, host_matches: true, trusted_proxies_narrow: true};
const respond = (v: unknown) => new Response(JSON.stringify(v), {status: 200, headers: {'Content-Type': 'application/json'}});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('passes a correctly proxied request', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond(ok)));
  render(<NetworkCheck />);
  expect(await screen.findByText(/Your address as seen by the server: 203.0.113.9/)).toBeTruthy();
  expect(screen.queryAllByText(/^Warn:/)).toHaveLength(0);
});
it('warns on an untrusted proxy, plain HTTP, a host mismatch and a subnet trust list', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({...ok, forwarded_trusted: false, forwarded_proto: '', app_url_https: false, host_matches: false, trusted_proxies_narrow: false})));
  render(<NetworkCheck />);
  expect(await screen.findAllByText(/^Warn:/)).toHaveLength(5);
  expect(screen.getByText('Warn: KY_TRUSTED_PROXIES is empty or includes a subnet; any address in it can forge client IPs')).toBeTruthy();
});
it('passes a trust list of single addresses', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond(ok)));
  render(<NetworkCheck />);
  expect(await screen.findByText('Pass: KY_TRUSTED_PROXIES names single addresses')).toBeTruthy();
});
it('rejects a response without trusted_proxies_narrow', async () => {
  const {trusted_proxies_narrow: _, ...old} = ok;
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond(old)));
  render(<NetworkCheck />);
  expect(await screen.findByText('Network check unavailable.')).toBeTruthy();
});
it('rejects a malformed response', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({peer_ip: 1})));
  render(<NetworkCheck />);
  expect(await screen.findByText('Network check unavailable.')).toBeTruthy();
});
