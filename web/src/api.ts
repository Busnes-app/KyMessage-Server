function cookieValue(name: string): string {
  const prefix = `${encodeURIComponent(name)}=`;
  const item = document.cookie.split('; ').find((part) => part.startsWith(prefix));
  return item ? decodeURIComponent(item.slice(prefix.length)) : '';
}

export function secureFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  const csrf = cookieValue('ky_csrf');
  if (csrf) headers.set('X-CSRF-Token', csrf);
  return fetch(input, { ...init, headers });
}

/** A step-up refusal. `url` is a same-origin suite sign-in that forces credentials; local
 * accounts get none and sign in again themselves. */
export interface Reauth { message: string; url: string | null }

let confirmItsYou: ((r: Reauth) => Promise<boolean>) | null = null;

/** Registers the "Confirm it's you" prompt; returns its unregister function. */
export function onReauthRequired(prompt: (r: Reauth) => Promise<boolean>): () => void {
  confirmItsYou = prompt;
  return () => { if (confirmItsYou === prompt) confirmItsYou = null; };
}

async function reauthNeeded(res: Response): Promise<Reauth | null> {
  if (res.status !== 403) return null;
  const body: unknown = await res.clone().json().catch(() => null);
  if (typeof body !== 'object' || body === null) return null;
  const { code, error, reauth_url: url } = body as { code?: unknown; error?: unknown; reauth_url?: unknown };
  if (code !== 'reauthentication_required') return null;
  return {
    message: typeof error === 'string' ? error : "Confirm it's you to continue.",
    url: typeof url === 'string' && url.startsWith('/api/sso/') ? url : null,
  };
}

/** secureFetch for admin calls: a step-up refusal opens "Confirm it's you" and, once the
 * admin has signed in again, retries the same request. Cancelling returns the refusal. */
export async function adminFetch(input: string, init: RequestInit = {}): Promise<Response> {
  for (;;) {
    const res = await secureFetch(input, { credentials: 'same-origin', ...init });
    const reauth = await reauthNeeded(res);
    if (!reauth || !confirmItsYou || !(await confirmItsYou(reauth))) return res;
  }
}

/** The server's JSON `error`, or `fallback (HTTP n)` when the body has none. */
export async function errorMessage(res: Response, fallback: string): Promise<string> {
  const body: unknown = await res.clone().json().catch(() => null);
  const error = typeof body === 'object' && body !== null ? (body as { error?: unknown }).error : undefined;
  return typeof error === 'string' && error ? error : `${fallback} (HTTP ${res.status})`;
}

/** True for the 404 the Matrix routes answer when chat is not set up. */
export async function isMatrixDisabled(res: Response): Promise<boolean> {
  if (res.status !== 404) return false;
  const body: unknown = await res.clone().json().catch(() => null);
  return typeof body === 'object' && body !== null && (body as { code?: unknown }).code === 'matrix_disabled';
}
