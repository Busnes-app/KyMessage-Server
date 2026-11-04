import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { PasswordChange } from './PasswordChange';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

function server(provider: string, change: (body: { current_password: string; new_password: string }) => Response) {
  const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === '/api/auth/me') return json({ authenticated: true, user: { sso_provider: provider } });
    return change(JSON.parse(String(init?.body)));
  });
  vi.stubGlobal('fetch', fetcher);
  return fetcher;
}

const fill = (current: string, next: string, confirmation = next) => {
  fireEvent.change(screen.getByLabelText('Current password'), { target: { value: current } });
  fireEvent.change(screen.getByLabelText('New password'), { target: { value: next } });
  fireEvent.change(screen.getByLabelText('Confirm new password'), { target: { value: confirmation } });
  fireEvent.click(screen.getByRole('button', { name: 'Change password' }));
};

it('changes a local password, shows a refusal, and goes back to sign-in on success', async () => {
  const onChanged = vi.fn();
  const fetcher = server('local', (b) => b.current_password === 'CurrentPassword123!'
    ? json({ password_changed: true, login_required: true })
    : json({ error: 'Current password is incorrect' }, 401));
  render(<PasswordChange onChanged={onChanged} />);
  await screen.findByRole('heading', { name: 'Password' });
  fill('CurrentPassword123!', 'NewPassword456!', 'Mismatch456789!');
  expect(screen.getByRole('alert').textContent).toContain('do not match');
  fill('WrongPassword123!', 'NewPassword456!');
  expect((await screen.findByRole('alert')).textContent).toContain('Current password is incorrect');
  expect(onChanged).not.toHaveBeenCalled();
  fill('CurrentPassword123!', 'NewPassword456!');
  await waitFor(() => expect(onChanged).toHaveBeenCalled());
  expect(fetcher).toHaveBeenCalledWith('/api/auth/change-password', expect.objectContaining({ method: 'POST' }));
});

it('shows nothing to a KyIdentity account', async () => {
  const fetcher = server('kyidentity', () => json({}));
  const { container } = render(<PasswordChange onChanged={vi.fn()} />);
  await waitFor(() => expect(fetcher).toHaveBeenCalled());
  expect(container.innerHTML).toBe('');
});
