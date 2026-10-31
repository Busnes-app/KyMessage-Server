import { afterEach, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { AppRail } from './AppRail';
import { appMark } from '../ky-ui/appmarks.js';
import { parseSuiteApps } from '../dto';

afterEach(cleanup);

const apps = [
  { id: 'kypost', name: 'KyPost', url: 'https://mail.example.com/api/auth/oidc/login?x=1&y=2' },
  { id: 'kymessage', name: 'KyMessages', url: 'https://admin.example.com/api/sso/kyidentity/login' },
];

it('links each app exactly as given, with its mark, and marks this console current', () => {
  render(<AppRail apps={apps} />);
  const nav = screen.getByRole('navigation', { name: 'Apps' });
  const links = screen.getAllByRole('link');
  expect(links.map((a) => a.getAttribute('href'))).toEqual(apps.map((a) => a.url));
  expect(links.map((a) => [a.getAttribute('title'), a.getAttribute('target')])).toEqual([['KyPost', null], ['KyMessages', null]]);
  expect(screen.getByRole('link', { name: 'KyMessages' }).getAttribute('aria-current')).toBe('page');
  expect(screen.getByRole('link', { name: 'KyPost' }).getAttribute('aria-current')).toBeNull();
  expect(nav.querySelector('img')?.getAttribute('src')).toBe(appMark('kypost'));
});

it('renders nothing for an empty list', () => {
  const { container } = render(<AppRail apps={[]} />);
  expect(container.innerHTML).toBe('');
});

it('keeps only https launch links', () => {
  expect(parseSuiteApps({ apps: [...apps, { id: 'x', name: 'X', url: 'javascript:alert(1)' }] })).toEqual(apps);
  expect(() => parseSuiteApps({})).toThrow();
});
