import { expect, it } from 'vitest';
import { navItemsFor } from './AppHeader';

it('member sees only My account', () => {
  expect(navItemsFor('user').map(i => i.id)).toEqual(['account']);
  expect(navItemsFor('manager').map(i => i.id)).toEqual(['account']);
});
it('admin sees admin navigation plus My account', () => {
  expect(navItemsFor('admin').map(i => i.id)).toEqual(['dashboard', 'scim', 'backup', 'settings', 'account']);
});
