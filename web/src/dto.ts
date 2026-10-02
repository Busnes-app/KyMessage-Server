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
