import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { EXEMPT_ROUTE, ExemptNetworksPanel, parseExemptStatus } from './ExemptNetworksPanel';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

it('refuses a malformed status', () => {
  expect(() => parseExemptStatus({ networks: 1, environment: '', exempted: 0 })).toThrow();
  expect(() => parseExemptStatus({ networks: '', environment: '', exempted: -1 })).toThrow();
});

it('shows the stored networks one per line and saves an edited list', async () => {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    expect(String(input)).toBe(EXEMPT_ROUTE);
    if (init?.method === 'PUT') {
      expect(JSON.parse(String(init.body))).toEqual({ networks: '203.0.113.0/24,198.51.100.7' });
      return json({ networks: '203.0.113.0/24,198.51.100.7/32', environment: '192.0.2.0/24', exempted: 0 });
    }
    return json({ networks: '203.0.113.0/24', environment: '192.0.2.0/24', exempted: 41 });
  });
  vi.stubGlobal('fetch', fetchMock);
  const { container } = render(<ExemptNetworksPanel />);
  const box = (await screen.findByDisplayValue('203.0.113.0/24')) as HTMLTextAreaElement;
  expect(container.textContent).toMatch(/41 since the last change or restart/);
  expect(container.textContent).toMatch(/Also set by the server environment192\.0\.2\.0\/24/);
  expect(container.textContent).toMatch(/never add guest Wi-Fi, a VPN provider or\s+your reverse proxy/);

  fireEvent.change(box, { target: { value: '203.0.113.0/24\n 198.51.100.7 \n' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save networks' }));
  expect((await screen.findByRole('status')).textContent).toMatch(/Saved/);
  expect(box.value).toBe('203.0.113.0/24\n198.51.100.7/32');
});

it('shows why the server refused a list, verbatim, and keeps what was typed', async () => {
  const refusal = '"10.0.0.0/8" is too broad; the widest IPv4 range allowed is /16';
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) =>
    init?.method === 'PUT' ? json({ error: refusal }, 400) : json({ networks: '', environment: '', exempted: 0 })));
  const { container } = render(<ExemptNetworksPanel />);
  const box = (await screen.findByPlaceholderText('203.0.113.0/24')) as HTMLTextAreaElement;
  await screen.findByText(/0 since/);
  fireEvent.change(box, { target: { value: '10.0.0.0/8' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save networks' }));
  expect((await screen.findByRole('alert')).textContent).toBe(refusal);
  expect(box.value).toBe('10.0.0.0/8');
  expect(container.textContent).not.toMatch(/Also set by the server environment/);
});

it('keeps the form disabled when the networks cannot be loaded', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => json({ error: 'Admin access required' }, 403)));
  render(<ExemptNetworksPanel />);
  expect((await screen.findByRole('alert')).textContent).toBe('Admin access required');
  expect((screen.getByRole('textbox') as HTMLTextAreaElement).disabled).toBe(true);
  expect((screen.getByRole('button', { name: 'Save networks' }) as HTMLButtonElement).disabled).toBe(true);
});
