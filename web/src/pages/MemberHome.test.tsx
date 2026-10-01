import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemberHome } from './MemberHome';

afterEach(cleanup);

it('shows the account, the chat notice and sign out', () => {
  const onLogout = vi.fn();
  render(<MemberHome user={{display_name: 'Alice', username: 'alice'}} onLogout={onLogout} />);
  expect(screen.getByText('Alice')).toBeTruthy();
  expect(screen.getByText("Chat isn't available yet.")).toBeTruthy();
  fireEvent.click(screen.getByRole('button', {name: 'Sign out'}));
  expect(onLogout).toHaveBeenCalledOnce();
});
