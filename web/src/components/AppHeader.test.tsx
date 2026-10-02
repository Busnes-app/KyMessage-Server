import { expect, it } from 'vitest';
import { navItemsFor } from './AppHeader';

it('member gets no admin navigation', () => {
  expect(navItemsFor('user')).toEqual([]);
  expect(navItemsFor('manager')).toEqual([]);
});
it('admin sees the admin pages', () => {
  expect(navItemsFor('admin').map(i => i.id)).toEqual(['dashboard', 'users', 'scim', 'backup', 'settings']);
});
