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

it('links to the chat client when one is configured', () => {
  render(<MemberHome user={{username: 'alice'}} chatUrl="https://chat.example.com" onLogout={() => {}} />);
  const link = screen.getByRole('link', {name: 'Open chat'});
  expect(link.getAttribute('href')).toBe('https://chat.example.com');
  expect(link.getAttribute('rel')).toBe('noopener noreferrer');
  expect(screen.queryByText("Chat isn't available yet.")).toBeNull();
});
