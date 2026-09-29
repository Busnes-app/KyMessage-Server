import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { Backup } from './Backup';

function mockStatus(body: Record<string, unknown>) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (!url.endsWith('/api/backup/status')) throw new Error(`unexpected fetch ${url}`);
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }),
  );
}

// PAIRED is a healthy SQLite instance: the driver decides whether the screen warns that no
// capsule can carry the database.
const PAIRED = {
  key_pinned: true,
  paired: true,
  interval_sec: 86400,
  recovery_url: 'https://recovery.example',
  recovery_key_id: 'k1',
  threshold: 2,
  total_shares: 3,
  database_driver: 'sqlite',
  members: ['data/ky_server.db'],
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Backup', () => {
  it('shows a failed scheduled attempt even when an older remote receipt exists', async () => {
    mockStatus({ ...PAIRED,
      last_deposit: { capsule_id: 'old', digest: 'abc', size_bytes: 100, deposited_at: '2026-09-26T12:00:00Z' },
      last_run: { outcome: 'failure', trigger: 'scheduled', recorded_at: '2026-09-27T12:00:00Z', capsule_id: '' },
    });
    render(<Backup />);
    expect((await screen.findByRole('alert')).textContent).toContain('Last recorded backup attempt: Failed');
    expect(screen.getByRole('alert').textContent).toContain('scheduled');
    expect(screen.getByText(/Last deposit/)).toBeTruthy();
  });

  it('rejects a malformed attempt instead of showing success', async () => {
    mockStatus({ ...PAIRED, last_run: { outcome: 'success', trigger: 'scheduled', recorded_at: 'bad', capsule_id: '' } });
    render(<Backup />);
    expect((await screen.findByRole('alert')).textContent).toContain('Invalid backup attempt status');
    expect(screen.queryByText(/Last recorded backup attempt: Succeeded/)).toBeNull();
  });

  it('warns when a key is pinned but there is no destination', async () => {
    mockStatus({ key_pinned: true, paired: false, interval_sec: 0, recovery_key_id: 'k1', threshold: 2, total_shares: 3, database_driver: 'sqlite', members: [] });
    render(<Backup />);
    expect(await screen.findByText(/nowhere to go/i)).toBeTruthy();
    expect(screen.getByText(/automatic backups are off/i)).toBeTruthy();
    expect(screen.getByText(/KY_BACKUP_DIR not set/)).toBeTruthy();
  });

  it('never renders the token', async () => {
    mockStatus({
      key_pinned: true,
      paired: true,
      interval_sec: 86400,
      recovery_url: 'https://recovery.example',
      recovery_key_id: 'k1',
      threshold: 2,
      total_shares: 3,
      database_driver: 'sqlite',
      members: ['data/ky_server.db'],
    });
    const { container } = render(<Backup />);
    expect(await screen.findByText('https://recovery.example')).toBeTruthy();
    expect(container.textContent).not.toMatch(/token/i);
    expect(screen.getByText('data/ky_server.db')).toBeTruthy();
    expect(screen.getByRole('button', { name: /unpair/i })).toBeTruthy();
  });

  it('says so on Postgres, where no capsule carries the database', async () => {
    mockStatus({ ...PAIRED, database_driver: 'postgres' });
    render(<Backup />);
    expect(await screen.findByText(/Backups do not include the PostgreSQL database/)).toBeTruthy();
  });

  it('does not warn about the database on SQLite', async () => {
    mockStatus(PAIRED);
    render(<Backup />);
    await screen.findByText('https://recovery.example');
    expect(screen.queryByText(/Backups do not include the PostgreSQL database/)).toBeNull();
  });

  it('offers a fresh suite sign-in when a backup change needs step-up', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith('/api/backup/status')) return new Response(JSON.stringify(PAIRED), { status: 200 });
        return new Response(JSON.stringify({
          error: 'Sign in to KySignOn again to confirm this change', code: 'reauthentication_required',
          reauth_url: '/api/sso/kysignon/login?fresh=1',
        }), { status: 403 });
      }),
    );
    render(<Backup />);
    await screen.findByText('https://recovery.example');
    fireEvent.change(screen.getByLabelText('Back up automatically'), { target: { value: '0' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    const link = await screen.findByRole('link', { name: 'Sign in to KySignOn again' });
    expect(link.getAttribute('href')).toBe('/api/sso/kysignon/login?fresh=1');
  });

  it('shows the message backups section off by default', async () => {
    mockStatus({ ...PAIRED, messages: { interval_sec: 0 } });
    render(<Backup />);
    const section = await screen.findByRole('region', { name: 'Message backups' });
    expect(section.querySelector('.badge')?.textContent).toBe('Off');
    expect((within(section).getByLabelText('Back up messages automatically') as HTMLSelectElement).value).toBe('0');
    expect(within(section).getByText(/Opt-in\. Holds threads/)).toBeTruthy();
    expect(within(section).getByRole('button', { name: 'Back up messages now' })).toBeTruthy();
    expect(within(section).getByRole('button', { name: 'Run message drill' })).toBeTruthy();
  });

  it('rejects a malformed messages status', async () => {
    mockStatus({ ...PAIRED, messages: { interval_sec: 'soon' } });
    render(<Backup />);
    expect((await screen.findByRole('alert')).textContent).toContain('Invalid message backup status');
  });
});
