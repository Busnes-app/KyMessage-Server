import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, render, screen } from '@testing-library/react';
import { QRPairingModal } from './QRPairingModal';

vi.mock('qrcode', () => ({ default: { toCanvas: vi.fn(async () => undefined) } }));

const SECRET = 'a'.repeat(48);
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
type Call = { url: string; init?: RequestInit };

function stub(init: () => Promise<Response>, poll: () => Response = () => json({ status: 'pending', expires_at: 0, device_name: '' })) {
  const calls: Call[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, req?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init: req });
    if (url === '/api/devices/pair/init') return init();
    if (url === '/api/devices/pair/poll') return poll();
    throw new Error(`unexpected fetch ${url}`);
  }));
  return calls;
}
const polls = (calls: Call[]) => calls.filter((c) => c.url.startsWith('/api/devices/pair/poll'));
const initOK = async () => json({ secret: SECRET, expires_at: 0, qr_payload: 'kymessages://pair?s=' + SECRET });
const tick = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });

beforeEach(() => { vi.useFakeTimers(); });
afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals(); });

it('polls with a POST body: the secret never appears in a URL', async () => {
  const calls = stub(initOK);
  render(<QRPairingModal onClose={vi.fn()} />);
  await tick(1600);
  const p = polls(calls);
  expect(p.length).toBeGreaterThan(0);
  expect(p[0].url).toBe('/api/devices/pair/poll');
  expect(p[0].init?.method).toBe('POST');
  expect(JSON.parse(String(p[0].init?.body))).toEqual({ secret: SECRET });
  expect(calls.some((c) => c.url.includes(SECRET))).toBe(false);
});

it('starts no timers when closed before init resolves', async () => {
  let release!: (r: Response) => void;
  const calls = stub(() => new Promise((r) => { release = r; }));
  const { unmount } = render(<QRPairingModal onClose={vi.fn()} />);
  unmount();
  release(await initOK());
  await tick(10_000);
  expect(polls(calls)).toHaveLength(0);
});

it('shows the refusal instead of a blank code when init is refused', async () => {
  const calls = stub(async () => json({ error: 'Too many pairing requests' }, 429));
  render(<QRPairingModal onClose={vi.fn()} />);
  await tick(0);
  expect(screen.getByRole('alert').textContent).toContain('Too many pairing requests');
  await tick(10_000);
  expect(polls(calls)).toHaveLength(0);
});

it('stops polling at expiry and says so', async () => {
  const calls = stub(initOK);
  render(<QRPairingModal onClose={vi.fn()} />);
  await tick(91_000);
  expect(screen.getByRole('alert').textContent).toContain('expired');
  const n = polls(calls).length;
  await tick(10_000);
  expect(polls(calls)).toHaveLength(n);
});

it('stops polling on a non-OK poll answer and shows it', async () => {
  const calls = stub(initOK, () => json({ error: 'Pairing not found or expired' }, 404));
  render(<QRPairingModal onClose={vi.fn()} />);
  await tick(1600);
  expect(screen.getByRole('alert').textContent).toContain('Pairing not found or expired');
  await tick(10_000);
  expect(polls(calls)).toHaveLength(1);
});
