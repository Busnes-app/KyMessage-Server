/** Boundary checks for admin DTOs: each returns the narrowed value or throws. */
export class InvalidResponse extends Error {
  constructor() { super('Invalid response from the server'); }
}
export function obj(v: unknown): Record<string, unknown> {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) throw new InvalidResponse();
  return v as Record<string, unknown>;
}
export function arr(v: unknown): unknown[] {
  if (!Array.isArray(v)) throw new InvalidResponse();
  return v;
}
export function str(v: unknown, max = 1024): string {
  if (typeof v !== 'string' || v.length > max) throw new InvalidResponse();
  return v;
}
export function bool(v: unknown): boolean {
  if (typeof v !== 'boolean') throw new InvalidResponse();
  return v;
}
export function count(v: unknown): number {
  if (typeof v !== 'number' || !Number.isSafeInteger(v) || v < 0) throw new InvalidResponse();
  return v;
}
export function iso(v: unknown): string {
  const s = str(v, 64);
  if (!Number.isFinite(Date.parse(s))) throw new InvalidResponse();
  return s;
}
export function oneOf<T extends string>(v: unknown, allowed: readonly T[]): T {
  if (typeof v !== 'string' || !(allowed as readonly string[]).includes(v)) throw new InvalidResponse();
  return v as T;
}
/** `f(v)` when the field is present; Go omits empty fields or sends null for them. */
export function opt<T>(v: unknown, f: (v: unknown) => T): T | undefined {
  return v === undefined || v === null ? undefined : f(v);
}

export interface SessionUser { username: string; display_name: string; role: string; must_change_password: boolean }
/** The `user` of /api/auth/me and the sign-in replies. */
export function parseUser(v: unknown): SessionUser {
  const o = obj(v);
  return {
    username: str(o.username, 255),
    display_name: opt(o.display_name, str) ?? '',
    role: str(o.role, 32),
    must_change_password: bool(o.must_change_password),
  };
}

export interface SiteSettings { app_name: string; chat_url: string; sso_enabled: boolean; logo_custom: boolean; db_driver?: string; app_version?: string }
/** The /api/settings fields the shell uses; the rest is tiered by role and not read here. */
export function parseSettings(v: unknown): SiteSettings {
  const o = obj(v);
  return { app_name: str(o.app_name), chat_url: opt(o.chat_url, str) ?? '', sso_enabled: o.sso_enabled === true, logo_custom: o.logo_custom !== false, db_driver: opt(o.db_driver, (d) => str(d, 32)), app_version: opt(o.app_version, (d) => str(d, 32)) };
}

export interface SuiteApp { id: string; name: string; url: string }
/** /api/suite/apps: KyIdentity's launch links, https only (a non-https entry is dropped). */
export function parseSuiteApps(v: unknown): SuiteApp[] {
  return arr(obj(v).apps).map((a) => {
    const o = obj(a);
    return { id: str(o.id, 128), name: str(o.name, 255), url: str(o.url, 2048) };
  }).filter((a) => a.url.startsWith('https://'));
}
