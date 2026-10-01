import { object, text, accountID } from './delivery-wire';

function cookieValue(name: string): string {
  const prefix = `${encodeURIComponent(name)}=`;
  const item = document.cookie.split('; ').find((part) => part.startsWith(prefix));
  return item ? decodeURIComponent(item.slice(prefix.length)) : '';
}

// Same contract as the console's secureFetch: mirror the ky_csrf cookie into X-CSRF-Token.
export function secureFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  const csrf = cookieValue('ky_csrf');
  if (csrf) headers.set('X-CSRF-Token', csrf);
  return fetch(input, { ...init, headers });
}
export class SessionError extends Error {}

export async function signedInAccount(signal?: AbortSignal) {
  const response = await fetch('/api/auth/me', {signal,credentials:'same-origin',cache:'no-store',redirect:'error'});
  if (!response.ok) throw new SessionError('Sign in again to connect this device.');
  const raw: unknown = await response.json();
  const value = object(raw);
  if (value.authenticated === false) return null;
  if (value.authenticated !== true) throw new SessionError('Invalid sign-in response.');
  const user = object(value.user);
  if (user.sso_provider !== 'kysignon' || user.status !== 'active' || user.must_change_password !== false || !text(user.sso_subject)) throw new SessionError('Sign in with your suite identity to use messaging.');
  return {id:accountID(user.id),name:text(user.display_name) || text(user.username)};
}
