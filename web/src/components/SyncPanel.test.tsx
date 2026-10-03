import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import { SyncPanel, UNCERTAIN, parseSyncStatus, syncCard, syncHints, syncState } from './SyncPanel';

const NOW = '2026-10-02T10:00:00Z';
const OK = {
  webhook: { at: NOW, kind: 'user.updated' },
  rejected: { count: 0, last_at: null, last_reason: '' },
  sweep: { finished_at: NOW, ok: true, error: '', applied: 1, failed: 0, failing_since: null },
  kyidentity_url: 'https://id.example.com',
};
const SECRETS_DIFFER = 'Deliveries fail the signature check: the secret in KyIdentity and KY_KYIDENTITY_HMAC_SECRET differ.';
const USERNAME_CONFLICT = 'A KyIdentity username matches a local KyMessages account, so that user was not created here. Rename the local account (KY_ADMIN_USERNAME only applies to an empty database) or change the KyIdentity user, then press Resync Directory on the KyMessages system in KyIdentity: KyIdentity does not resend it on its own.';
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe('syncState, syncHints and syncCard', () => {
  it('is ok when a webhook was accepted and the last sweep passed', () => {
    const s = parseSyncStatus(OK);
    expect(syncState(s)).toBe('ok');
    expect(syncHints(s)).toEqual([UNCERTAIN]);
    expect(syncCard(s).tone).toBe('success');
    expect(syncCard(s).text).toMatch(/^Last webhook .*; last sweep ok$/);
  });
  it('warns when no webhook was ever accepted, naming both settings', () => {
    const s = parseSyncStatus({ ...OK, webhook: null });
    expect(syncState(s)).toBe('warning');
    expect(syncHints(s)[0]).toBe('No webhook has been accepted yet: check the suite_webhook system in KyIdentity and KY_KYIDENTITY_HMAC_SECRET.');
    expect(syncCard(s)).toEqual({ text: `Needs attention: ${syncHints(s)[0]}`, tone: 'warning' });
  });
  it('warns that the secrets differ after bad signatures', () => {
    const s = parseSyncStatus({ ...OK, rejected: { count: 3, last_at: NOW, last_reason: 'bad_signature' } });
    expect(syncState(s)).toBe('warning');
    expect(syncHints(s)).toContain(SECRETS_DIFFER);
  });
  it('is ok when a bad signature predates the last accepted webhook', () => {
    const s = parseSyncStatus({ ...OK, rejected: { count: 3, last_at: '2026-10-02T09:00:00Z', last_reason: 'bad_signature' } });
    expect(syncState(s)).toBe('ok');
    expect(syncHints(s)).toEqual([UNCERTAIN]);
  });
  it('warns on a bad signature when no webhook was ever accepted', () => {
    const s = parseSyncStatus({ ...OK, webhook: null, rejected: { count: 1, last_at: NOW, last_reason: 'bad_signature' } });
    expect(syncState(s)).toBe('warning');
    expect(syncHints(s)).toContain(SECRETS_DIFFER);
  });
  it('treats a stale delivery as a hint, not a warning', () => {
    const s = parseSyncStatus({ ...OK, rejected: { count: 1, last_at: NOW, last_reason: 'stale' } });
    expect(syncState(s)).toBe('ok');
    expect(syncHints(s).some((h) => h.includes('check both clocks'))).toBe(true);
  });
  it('warns when a KyIdentity username matches a local account, until a webhook is accepted after it', () => {
    const s = parseSyncStatus({ ...OK, rejected: { count: 1, last_at: NOW, last_reason: 'username_conflict' } });
    expect(syncState(s)).toBe('warning');
    expect(syncHints(s)[0]).toBe(USERNAME_CONFLICT);
    expect(syncCard(s)).toEqual({ text: `Needs attention: ${USERNAME_CONFLICT}`, tone: 'warning' });
    const fixed = parseSyncStatus({ ...OK, rejected: { count: 1, last_at: '2026-10-02T09:00:00Z', last_reason: 'username_conflict' } });
    expect(syncState(fixed)).toBe('ok');
  });
  it('warns before the first sweep', () => {
    expect(syncState(parseSyncStatus({ ...OK, sweep: null }))).toBe('warning');
  });
  it('fails while the sweep fails, saying since when and why', () => {
    const s = parseSyncStatus({ ...OK, sweep: { ...OK.sweep, ok: false, error: 'MAS token: HTTP 401', failing_since: '2026-10-02T09:00:00Z' } });
    expect(syncState(s)).toBe('failing');
    expect(syncHints(s)[0]).toMatch(/^The offboarding sweep has failed since .+: MAS token: HTTP 401$/);
    expect(syncCard(s).tone).toBe('danger');
    expect(syncCard(s).text).toMatch(/^Offboarding sweep failing since /);
  });
  it('refuses a malformed status', () => {
    expect(() => parseSyncStatus({ ...OK, rejected: { count: -1, last_at: null, last_reason: '' } })).toThrow();
    expect(() => parseSyncStatus({ ...OK, sweep: { ...OK.sweep, finished_at: 'yesterday' } })).toThrow();
  });
});

describe('SyncPanel', () => {
  it('renders nothing when chat is not set up', async () => {
    const fetchMock = vi.fn(async () => json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404));
    vi.stubGlobal('fetch', fetchMock);
    const { container } = render(<SyncPanel />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(container.innerHTML).toBe('');
  });
  it('shows the records, the hints and the KyIdentity link', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ ...OK, rejected: { count: 2, last_at: NOW, last_reason: 'bad_signature' } })));
    render(<SyncPanel />);
    const panel = await screen.findByRole('region', { name: 'KyIdentity sync' });
    expect(within(panel).getByText(/\(user\.updated\)$/)).toBeTruthy();
    expect(within(panel).getByText(/\(bad signature\)$/)).toBeTruthy();
    expect(within(panel).getByText(SECRETS_DIFFER)).toBeTruthy();
    const hint = within(panel).getByText(UNCERTAIN);
    expect([hint.tagName, hint.className]).toEqual(['P', 'dr-hint']);
    expect(within(panel).getByText('Needs attention')).toBeTruthy();
    expect(within(panel).getByRole('link', { name: /Open KyIdentity/ }).getAttribute('href')).toBe('https://id.example.com');
  });
  it('links KyIdentity only over https', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ ...OK, kyidentity_url: 'javascript:alert(1)' })));
    render(<SyncPanel />);
    const panel = await screen.findByRole('region', { name: 'KyIdentity sync' });
    expect(within(panel).queryByRole('link')).toBeNull();
  });
});
