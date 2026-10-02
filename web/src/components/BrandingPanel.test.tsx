import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { BrandingPanel, elementNotice, parseBranding, staleServed } from './BrandingPanel';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const SHA = 'a'.repeat(64);
const STATE = {
  name: 'KyMessages', stored_name: '', default_name: 'KyMessages',
  logo: { custom: false, sha256: '', size: 0 }, element: { brand: 'KyMessages', served: 'KyMessages' },
};

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

describe('parseBranding and elementNotice', () => {
  it('reads Element only when present and refuses a malformed body', () => {
    expect(parseBranding({ ...STATE, element: undefined }).element).toBeNull();
    expect(parseBranding({ ...STATE, element: { error: 'open: no such file' } }).element).toEqual({ brand: null, error: 'open: no such file', served: null, served_error: '', restart_hint: '' });
    expect(() => parseBranding({ ...STATE, element: { restart_hint: 7 } })).toThrow();
    expect(() => parseBranding({ ...STATE, logo: { custom: 'yes', sha256: '', size: 0 } })).toThrow();
    expect(() => parseBranding({ ...STATE, element: { brand: 7 } })).toThrow();
  });
  it('clips an over-long brand from a hand-edited Element file instead of refusing the body', () => {
    const e = parseBranding({ ...STATE, element: { brand: 'x'.repeat(5000), served: 'y'.repeat(5000), error: 'e'.repeat(5000) } }).element;
    expect(e?.brand).toBe(`${'x'.repeat(256)}\u2026`);
    expect(e?.served).toBe(`${'y'.repeat(256)}\u2026`);
    expect(e?.error).toHaveLength(1025);
  });
  it('names what Element shows and why, and is quiet when it matches', () => {
    expect(elementNotice(parseBranding({ ...STATE, name: 'Acme', element: { brand: 'KyMessages', error: 'permission denied' } }), true))
      .toBe('Saved, but Element shows "KyMessages": permission denied. It is retried every minute.');
    expect(elementNotice(parseBranding({ ...STATE, name: 'Acme', element: { error: 'open: no such file' } }), true))
      .toBe("Saved, but Element's config could not be read: open: no such file");
    expect(elementNotice(parseBranding({ ...STATE, name: 'Acme', element: { brand: 'KyMessages' } }), true))
      .toBe('Saved, but Element shows "KyMessages". It is retried every minute.');
    expect(elementNotice(parseBranding(STATE), true)).toBeNull();
    expect(elementNotice(parseBranding({ ...STATE, element: undefined }), true)).toBeNull();
  });
  it('claims no save on a plain load', () => {
    expect(elementNotice(parseBranding({ ...STATE, name: 'Acme', element: { brand: 'KyMessages', error: 'permission denied' } }), false))
      .toBe('Element shows "KyMessages": permission denied. It is retried every minute.');
    expect(elementNotice(parseBranding({ ...STATE, name: 'Acme', element: { error: 'open: no such file' } }), false))
      .toBe("Element's config could not be read: open: no such file");
  });
  it('names the brand a running Element serves until it restarts', () => {
    expect(staleServed(parseBranding({ ...STATE, name: 'Acme', element: { brand: 'Acme', served: 'KyMessages' } }))).toBe('KyMessages');
    // The file write failed: elementNotice speaks, not the restart hint.
    expect(staleServed(parseBranding({ ...STATE, name: 'Acme', element: { brand: 'KyMessages', served: 'KyMessages', error: 'permission denied' } }))).toBeNull();
    expect(staleServed(parseBranding({ ...STATE, name: 'Acme', element: { brand: 'Acme', served: 'Acme' } }))).toBeNull();
    expect(staleServed(parseBranding({ ...STATE, name: 'Acme', element: { brand: 'Acme', served_error: 'connection refused' } }))).toBeNull();
    expect(staleServed(parseBranding({ ...STATE, element: undefined }))).toBeNull();
  });
});

describe('BrandingPanel', () => {
  it('saves a name and tells the shell to re-read the settings', async () => {
    const onChanged = vi.fn();
    const calls = serve((url, init) => {
      if (url === '/api/admin/branding') return json(STATE);
      if (url === '/api/admin/branding/name' && init?.method === 'PUT') return json({ ...STATE, name: 'Acme', stored_name: 'Acme', element: { brand: 'Acme', served: 'KyMessages', restart_hint: 'kubectl -n ky-stack rollout restart deployment/element' } });
      return undefined;
    });
    render(<BrandingPanel onChanged={onChanged} />);
    expect(screen.queryByText(/until it restarts/)).toBeNull();
    fireEvent.change(await screen.findByLabelText('Product name'), { target: { value: 'Acme' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save name' }));
    expect(await screen.findByText(/^Name saved/)).toBeTruthy();
    expect(onChanged).toHaveBeenCalledTimes(1);
    expect(calls.find((c) => c.init?.method === 'PUT')?.init?.body).toBe(JSON.stringify({ name: 'Acme' }));
    expect(screen.getByRole('button', { name: 'Use default (KyMessages)' })).toBeTruthy();
    const hint = screen.getByText(/until it restarts/);
    expect(hint.textContent).toBe('Element shows \u201cKyMessages\u201d until it restarts: kubectl -n ky-stack rollout restart deployment/element');
    expect(hint.querySelector('code')?.textContent).toBe('kubectl -n ky-stack rollout restart deployment/element');
  });

  it('says what Element still shows when its config could not be written', async () => {
    serve((url, init) => {
      if (url === '/api/admin/branding') return json(STATE);
      if (init?.method === 'PUT') return json({ ...STATE, name: 'Acme', stored_name: 'Acme', element: { brand: 'KyMessages', error: 'open /matrix/element/config.json: permission denied' } });
      return undefined;
    });
    render(<BrandingPanel onChanged={() => {}} />);
    fireEvent.change(await screen.findByLabelText('Product name'), { target: { value: 'Acme' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save name' }));
    expect(await screen.findByText('Saved, but Element shows "KyMessages": open /matrix/element/config.json: permission denied. It is retried every minute.')).toBeTruthy();
    expect(screen.queryByText(/until it restarts/)).toBeNull();
  });

  it('on load, says what Element shows without claiming a save', async () => {
    serve((url) => (url === '/api/admin/branding' ? json({ ...STATE, name: 'Acme', stored_name: 'Acme', element: { brand: 'KyMessages' } }) : undefined));
    render(<BrandingPanel onChanged={() => {}} />);
    expect((await screen.findByRole('alert')).textContent).toBe('Element shows "KyMessages". It is retried every minute.');
  });

  it('keeps the panel when Element\'s file holds an over-long hand-edited brand', async () => {
    const long = `<b>${'x'.repeat(5000)}`;
    serve((url) => (url === '/api/admin/branding' ? json({ ...STATE, element: { brand: long, served: long } }) : undefined));
    render(<BrandingPanel onChanged={() => {}} />);
    expect(await screen.findByLabelText('Product name')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Save name' })).toBeTruthy();
    const alert = screen.getByRole('alert');
    expect(alert.textContent).toBe(`Element shows "${long.slice(0, 256)}\u2026". It is retried every minute.`);
    expect(alert.querySelector('b')).toBeNull();
    expect(screen.queryByText(/Invalid response/)).toBeNull();
  });

  it('resets the name to the default', async () => {
    const calls = serve((url, init) => {
      if (url === '/api/admin/branding') return json({ ...STATE, name: 'Acme', stored_name: 'Acme', element: { brand: 'Acme' } });
      if (init?.method === 'PUT') return json(STATE);
      return undefined;
    });
    render(<BrandingPanel onChanged={() => {}} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Use default (KyMessages)' }));
    await waitFor(() => expect(calls.find((c) => c.init?.method === 'PUT')?.init?.body).toBe(JSON.stringify({ name: '' })));
    expect(await screen.findByText('Name reset to KyMessages.')).toBeTruthy();
  });

  it('uploads a PNG and previews it by its digest, never through a blob URL', async () => {
    const calls = serve((url, init) => {
      if (url === '/api/admin/branding') return json(STATE);
      if (url === '/api/admin/branding/logo' && init?.method === 'PUT') return json({ ...STATE, logo: { custom: true, sha256: SHA, size: 1234 } });
      if (url === '/api/admin/branding/logo' && init?.method === 'DELETE') return json(STATE);
      return undefined;
    });
    render(<BrandingPanel onChanged={() => {}} />);
    expect((await screen.findByAltText('Current logo')).getAttribute('src')).toBe('/app-icon.png?v=default');
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], 'logo.png', { type: 'image/png' });
    fireEvent.change(screen.getByLabelText(/^Logo/), { target: { files: [file] } });
    fireEvent.click(screen.getByRole('button', { name: 'Upload logo' }));
    await waitFor(() => expect(screen.getByAltText('Current logo').getAttribute('src')).toBe(`/app-icon.png?v=${SHA}`));
    const put = calls.find((c) => c.init?.method === 'PUT');
    expect(new Headers(put?.init?.headers).get('Content-Type')).toBe('image/png');
    expect(put?.init?.body).toBe(file);
    fireEvent.click(screen.getByRole('button', { name: 'Reset logo' }));
    await waitFor(() => expect(screen.getByAltText('Current logo').getAttribute('src')).toBe('/app-icon.png?v=default'));
  });

  it('refuses a file over 1 MiB without sending it', async () => {
    const calls = serve((url) => (url === '/api/admin/branding' ? json(STATE) : undefined));
    render(<BrandingPanel onChanged={() => {}} />);
    const big = new File([new Uint8Array((1 << 20) + 1)], 'big.png', { type: 'image/png' });
    fireEvent.change(await screen.findByLabelText(/^Logo/), { target: { files: [big] } });
    fireEvent.click(screen.getByRole('button', { name: 'Upload logo' }));
    expect(await screen.findByText('The logo must be at most 1 MiB.')).toBeTruthy();
    expect(calls.filter((c) => c.init?.method === 'PUT')).toHaveLength(0);
  });
});
