// Network and persisted-adapter boundaries. MLS bytes are decoded by ts-mls.
export function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Expected object');
  return value as Record<string, unknown>;
}
export function text(value: unknown): string {
  if (typeof value !== 'string' || value.length > 768 * 1024) throw new Error('Expected bounded string');
  return value;
}
export function accountID(value: unknown): string {
  const id = text(value);
  const bytes = new TextEncoder().encode(id);
  if (!bytes.length || bytes.length > 64 || /\p{Cc}/u.test(id) || new TextDecoder().decode(bytes) !== id) throw new Error('Expected an account ID of 1–64 UTF-8 bytes without control characters');
  return id;
}
export function integer(value: unknown): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) throw new Error('Expected nonnegative integer');
  return value;
}
export function array<T>(value: unknown, parse: (item: unknown) => T): T[] {
  if (!Array.isArray(value) || value.length > 256) throw new Error('Expected bounded array');
  return value.map(parse);
}
export function roster(value: unknown) {
  const devices = array(value, item => {
    const d = object(item);
    return { id: text(d.id), user_id: accountID(d.user_id), public_key: text(d.public_key), generation: integer(d.generation) };
  });
  if (new Set(devices.map(d => d.id)).size !== devices.length || new Set(devices.map(d => d.public_key)).size !== devices.length) throw new Error('Duplicate roster device');
  return devices;
}
export type Roster = ReturnType<typeof roster>;
export function metadata(value: unknown) {
  const m = object(value);
  if (m.domain !== 'KyMessages MLS proof delivery v1' || (m.kind !== 'application' && m.kind !== 'commit')) throw new Error('Invalid delivery metadata');
  return { domain: m.domain, room: text(m.room), id: text(m.id), device_id: text(m.device_id), kind: m.kind, epoch: integer(m.epoch), roster_hash: text(m.roster_hash), devices: roster(m.devices) };
}
export type Metadata = ReturnType<typeof metadata>;
export function event(value: unknown) {
  const e = object(value);
  if (e.kind !== 'application' && e.kind !== 'commit') throw new Error('Invalid event kind');
  return { id: text(e.id), device_id: text(e.device_id), sequence: integer(e.sequence), epoch: integer(e.epoch), kind: e.kind, roster_hash: text(e.roster_hash), payload: text(e.payload), welcome: text(e.welcome) };
}
export type Event = ReturnType<typeof event>;
export function connection(value: unknown) {
  const d = object(value);
  const p = d.pending === null ? null : object(d.pending);
  const publication = d.publication === undefined || d.publication === null ? null : object(d.publication);
  const joinPackages = d.joinPackages === undefined ? [] : array(d.joinPackages,item => {
    const saved = object(item);
    return {payload:text(saved.payload),initPrivateKey:text(saved.initPrivateKey),hpkePrivateKey:text(saved.hpkePrivateKey),signaturePrivateKey:text(saved.signaturePrivateKey)};
  });
  if (joinPackages.length > 16) throw new Error('Too many retained join packages');
  return {
    device: d.device === null ? null : text(d.device), token: text(d.token),
    challenge: d.challenge === undefined || d.challenge === null ? null : text(d.challenge),
    room: d.room === null ? null : text(d.room),
    roster: d.roster === null ? null : roster(d.roster),
    publication: publication === null ? null : {payload:text(publication.payload),expires_at:integer(publication.expires_at)},
    joinPackages,
    rejoinGeneration: d.rejoinGeneration === undefined || d.rejoinGeneration === null ? null : integer(d.rejoinGeneration),
    claims: d.claims === undefined ? [] : array(d.claims,item => {
      const c = object(item);
      return {device:text(c.device),generation:integer(c.generation),request_id:text(c.request_id),payload:c.payload === null ? null : text(c.payload),expires_at:integer(c.expires_at)};
    }),
    messages: d.messages === undefined ? [] : array(d.messages, item => {
      const m = object(item);
      return {id:text(m.id),sender:text(m.sender),text:text(m.text),sequence:integer(m.sequence)};
    }),
    pending: p === null ? null : { request: text(p.request), state: p.state === null ? null : text(p.state), plaintext:p.plaintext === undefined || p.plaintext === null ? null : text(p.plaintext) },
  };
}
export type Connection = ReturnType<typeof connection>;
