import { secureFetch } from '../../web/src/api';
import { object, text, accountID } from './delivery-wire';

export { secureFetch };
export class SessionError extends Error {}

export async function signedInAccount() {
  const response = await fetch('/api/auth/me', {credentials:'same-origin',cache:'no-store',redirect:'error'});
  if (!response.ok) throw new SessionError('Sign in again to connect this device.');
  const raw: unknown = await response.json();
  const value = object(raw);
  if (value.authenticated === false) return null;
  if (value.authenticated !== true) throw new SessionError('Invalid sign-in response.');
  const user = object(value.user);
  if (user.sso_provider !== 'kysignon' || user.status !== 'active' || user.must_change_password !== false || !text(user.sso_subject)) throw new SessionError('Sign in with your suite identity to use messaging.');
  return {id:accountID(user.id),name:text(user.display_name) || text(user.username)};
}
