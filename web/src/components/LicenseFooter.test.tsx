import { afterEach, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { LicenseFooter } from './LicenseFooter';
import mitLicenseText from '../mit.txt?raw';

afterEach(cleanup);

it('opens the About dialog with the version and licence, and closes it', () => {
  render(<LicenseFooter appVersion="0.5.1" />);
  const year = new Date().getFullYear();
  fireEvent.click(screen.getByRole('button', { name: `© ${year} · MIT License` }));
  const dialog = screen.getByRole('dialog', { name: 'KyMessages' });
  expect(dialog.textContent).toContain('v0.5.1');
  expect(dialog.textContent).toContain('Developed by Busnes.app');
  expect((screen.getByLabelText('MIT License') as HTMLTextAreaElement).value).toBe(mitLicenseText);
  fireEvent.click(screen.getByRole('button', { name: 'Close' }));
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('shows no version badge when the server gave none', () => {
  render(<LicenseFooter />);
  fireEvent.click(screen.getByRole('button', { name: /MIT License/ }));
  expect(screen.getByRole('dialog').textContent).not.toMatch(/\bv\d/);
});
