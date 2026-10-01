# Client Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove every release-step-4 blocker except the independent review, without putting the unreviewed chat client in the deployed binary.

**Architecture:**
- The chat client's non-UI core moves from `mls-proof/src` to a top-level `chat-core/` package that a CI gate keeps out of `web/` and the image.
- The console gains a members' "My account" page (account, device list with revoke, browser support state), an admin network self-check, and a proxy Compose overlay that follows KyPost's per-app-network model.

**Tech Stack:** Go 1.x (net/http), React 19 + TypeScript + Vite + vitest + @testing-library/react, Playwright, Docker Compose v2.24+, ts-mls 1.6.4.

**Spec:** `docs/superpowers/specs/2026-10-01-client-readiness-design.md`

## Global Constraints

- No change to the messaging protocol, the messaging HTTP API or any IndexedDB key, record layout or wire format.
- Nothing under `web/` may import `chat-core` or `ts-mls`. `web/package.json` must not list `ts-mls`. `.dockerignore` excludes `/chat-core/`.
- Members' notice text, verbatim: `Encrypted chat is not available on this server yet. It ships after an independent security review.`
- Non-suite account text, verbatim: `Messaging needs a KySignOn account.`
- Proxy network: `${KY_NETWORK:-kymessages-net}`, subnet `${KY_NETWORK_SUBNET:-10.91.0.0/24}`. The proxy overlay publishes no port.
- Declared browsers: current desktop Chrome, Edge and Firefox. Safari, iOS and Android are "not yet verified".
- Authenticated state-changing requests use `secureFetch`. DTOs are validated at the HTTP boundary.
- Commits end with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- DOX: read root `AGENTS.md` and each touched directory's `AGENTS.md` before editing, and update them after.

## Rulings made while planning

- **`chat-core` owns its CSRF fetch.**
  - The spec asks for "a CSRF-fetch function as a parameter". Instead, `chat-core/src/session.ts` carries its own 10-line `secureFetch`, the same code as `web/src/api.ts`.
  - Why: it removes the `web/` dependency with no injection plumbing, and it keeps the review target self-contained.
  - Cost if wrong: two copies of 10 lines.
- **Member browser regression.**
  - The console's browser harness has no OIDC issuer, and a KySignOn member cannot be created there. So the member-only navigation and the device revoke flow are pinned by vitest (Tasks 3 and 4).
  - The browser regression signs in as the bootstrap admin. It asserts that "My account" renders, that it shows the non-suite text, and that the browser support state is shown, on Chromium and Firefox.
  - Cost: the member path has no real-browser test until a fixture issuer exists for `web/`.
- **Origin is not part of the self-check.** A same-origin GET carries no `Origin` header. The self-check therefore reports `host_matches` (request `Host` against `KY_APP_URL`) and the trusted `X-Forwarded-Proto` instead.

## Review Focus

1. **An admin who is also a messaging user** must keep every admin page and also get "My account". Covered by the Task 4 vitest `admin sees admin navigation plus My account`.
2. **A local-password account** (messaging 403 `Suite OIDC sign-in required`) must see the non-suite text, never a Revoke button. Covered by the Task 3 vitest `non-suite account sees no device actions`.
3. **The proxy overlay** must not break the `static-ip` and `build` overlay chains. Covered by the Task 6 compose check, which runs the proxy overlay with `docker-compose.static-ip.yml`.
4. **An untrusted peer sending `X-Forwarded-For` and `X-Forwarded-Proto`** must be reported as untrusted, with the peer address as the client IP. Covered by the Task 5 Go test `TestNetworkCheckIgnoresForwardedHeadersFromUntrustedPeer`.
5. **Vaults saved before the move must still unlock.** Covered in Task 1 by the rename-similarity check and the unchanged mls-proof suites, which include reload, unlock and saved-room tests.

---

### Task 1: `chat-core` package split and gate

**Files:**
- Create: `chat-core/package.json`, `chat-core/tsconfig.json`, `chat-core/AGENTS.md`, `scripts/check-chat-gate.sh`
- Move with `git mv`, no content edits except where stated: `mls-proof/src/{delivery,delivery-wire,vault,device,markdown,session}.ts` → `chat-core/src/`
- Modify:
  - `chat-core/src/session.ts` (own `secureFetch`)
  - `mls-proof/src/chat.ts`, `mls-proof/src/main.ts`, `mls-proof/tests/*.ts` (import paths)
  - `mls-proof/package.json` (drop `ts-mls`, `markdown-it`)
  - `mls-proof/tsconfig.json`
  - `.dockerignore`, `.github/workflows/ci.yml`, `Makefile`
  - `AGENTS.md`, `mls-proof/AGENTS.md`

**Interfaces:**
- Produces: `chat-core/src/*`, with the same exports as today's modules. `chat-core/src/session.ts` exports `secureFetch`, `SessionError`, `signedInAccount`.
- Produces: `scripts/check-chat-gate.sh`, which exits 0 when the gate holds and 1 otherwise.

- [ ] **Step 1: Write the gate script (it is the test).**

```bash
#!/usr/bin/env bash
# The unreviewed chat client stays out of the deployed console and image until the
# independent review passes. Lifting this gate is a deliberate, reviewed edit.
set -u
root=$(git rev-parse --show-toplevel)
fail=0
if grep -rEn "chat-core|from ['\"]ts-mls" "$root/web/src" "$root/web/vite.config.ts" 2>/dev/null; then
  echo "gate: web/ must not import chat-core or ts-mls"; fail=1
fi
if grep -q '"ts-mls"' "$root/web/package.json"; then
  echo "gate: web/package.json must not list ts-mls"; fail=1
fi
if ! grep -qx '/chat-core/' "$root/.dockerignore"; then
  echo "gate: .dockerignore must exclude /chat-core/"; fail=1
fi
if [ ! -f "$root/chat-core/package.json" ]; then
  echo "gate: chat-core/ package missing"; fail=1
fi
exit $fail
```

- [ ] **Step 2: Run it and see it fail.** `bash scripts/check-chat-gate.sh; echo $?`. Expected: `gate: .dockerignore must exclude /chat-core/`, `gate: chat-core/ package missing`, exit 1. Check `ls web/vite.config.*` first and adjust that path in the script to the real file name.

- [ ] **Step 3: Move the modules.**

```bash
mkdir -p chat-core/src
git mv mls-proof/src/delivery.ts mls-proof/src/delivery-wire.ts mls-proof/src/vault.ts mls-proof/src/device.ts mls-proof/src/markdown.ts mls-proof/src/session.ts chat-core/src/
```

Replace the first line of `chat-core/src/session.ts` (`import { secureFetch } from '../../web/src/api';`), and the `export { secureFetch };` line, with:

```ts
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
```

`chat-core/package.json`:

```json
{
  "name": "kymessages-chat-core",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": { "typecheck": "tsc --noEmit" },
  "dependencies": { "markdown-it": "15.0.2", "ts-mls": "1.6.4" },
  "devDependencies": { "@types/markdown-it": "<copy the exact version from mls-proof/package.json if listed, else omit>", "typescript": "<copy the exact version from mls-proof/package.json>" }
}
```

Copy the exact devDependency versions from `mls-proof/package.json`. Drop a devDependency that mls-proof does not have. Run `cd chat-core && npm install` to create `package-lock.json`, then confirm `npm ci` works.

`chat-core/tsconfig.json`: copy `mls-proof/tsconfig.json` with `"include": ["src"]`.

- [ ] **Step 4: Repoint mls-proof.**
  - In `mls-proof/src/chat.ts` and `mls-proof/src/main.ts`, change `from './device'`, `'./vault'`, `'./markdown'`, `'./delivery'`, `'./delivery-wire'` and `'./session'` to `from '../../chat-core/src/<same name>'`.
  - In `mls-proof/tests/*.ts`, change `from '../src/<moved>'` to `from '../../chat-core/src/<moved>'`.
  - In `mls-proof/package.json`, remove `ts-mls` and `markdown-it` from `dependencies`, then run `cd mls-proof && npm install` to refresh the lockfile.
  - In `mls-proof/tsconfig.json`, set `"include": ["src", "tests", "../chat-core/src"]`.
  - `ts-mls` must resolve from `chat-core/node_modules`. Run `npm ci` in `chat-core` before building mls-proof.

- [ ] **Step 5: Prove the move changed nothing.**
  - Run `git diff --cached -M --stat` (after `git add -A chat-core mls-proof`). Every moved file except `session.ts` must show as a 100% rename. `session.ts` changes only the `secureFetch` lines.
  - Run `cd chat-core && npm ci && npm run typecheck`.
  - Run `cd mls-proof && npm ci && npm run build`.
  - Run `npm test -- --project=chromium --project=firefox`, `npm run test:delivery -- --project=chromium --project=firefox`, `npm run test:oidc -- --project=chromium --project=firefox`.
  - Expected: the same pass counts as on master (default 22, delivery 67 + 1 skipped, OIDC 18), with no failures.

- [ ] **Step 6: Gate and CI.**
  - Append `/chat-core/` to `.dockerignore` with the comment `# Unreviewed chat core; lifted only after the independent review`.
  - In `.github/workflows/ci.yml`'s MLS proof job, before the mls-proof `npm ci`, add a step `npm ci` with `working-directory: chat-core`, followed by `npm run typecheck`. Add `chat-core/package-lock.json` to that job's `cache-dependency-path`.
  - Add `bash scripts/check-chat-gate.sh` to the lint step of ci.yml and to the Makefile `lint` target.
  - Run `bash scripts/check-chat-gate.sh; echo $?`. Expected: exit 0.

- [ ] **Step 7: Docs.**
  - `chat-core/AGENTS.md` (DOX shape):
    - Purpose: the non-UI encrypted-chat core and the independent review target.
    - Ownership: delivery, wire validation, vault, device and MLS calls, markdown, and the session.
    - Local Contracts: no imports from `web/`; nothing in `web/` imports this package until the gate is lifted; no IndexedDB key, record or wire-format change without a migration plan; versions are pinned and `npm ci` is used.
    - Verification: `npm run typecheck` here, the mls-proof suites and `scripts/check-chat-gate.sh`.
  - Root `AGENTS.md`: add `chat-core/AGENTS.md` to the Child DOX Index. Change "The isolated browser experiment lives in `mls-proof/`" to say mls-proof's UI and harness consume `chat-core/`. Add the gate script to Verification.
  - `mls-proof/AGENTS.md`: the core modules now live in `chat-core/`, and mls-proof keeps the UI, harness, fixture server and tests.

- [ ] **Step 8: Commit.** `chat-core: split the encrypted-chat core out of mls-proof behind a CI gate`

---

### Task 2: Browser support check and declaration

**Files:**
- Create: `web/src/browserSupport.ts`, `web/src/browserSupport.test.ts`, `docs/BROWSER-SUPPORT.md`

**Interfaces:**
- Produces: `export type BrowserSupport = {state: 'supported' | 'unverified' | 'unsupported'; missing: string[]}` and `export async function browserSupport(env?: SupportEnv): Promise<BrowserSupport>`. `SupportEnv` defaults to the real `globalThis` values and is injectable for tests.
- Produces: `export const supportedBrowsers = 'current desktop Chrome, Edge and Firefox'`.

- [ ] **Step 1: Write the failing tests** (`web/src/browserSupport.test.ts`):

```ts
import { expect, it } from 'vitest';
import { browserSupport, type SupportEnv } from './browserSupport';

const ed25519: SubtleCrypto = {
  generateKey: async () => ({privateKey: {} as CryptoKey, publicKey: {} as CryptoKey}),
  sign: async () => new ArrayBuffer(64),
} as unknown as SubtleCrypto;
const base = (over: Partial<SupportEnv> = {}): SupportEnv => ({
  subtle: ed25519, indexedDB: {} as IDBFactory, locks: {} as LockManager,
  secureContext: true, brands: ['Google Chrome'], userAgent: '', ...over,
});

it('supports a declared browser with every feature', async () => {
  expect(await browserSupport(base())).toEqual({state: 'supported', missing: []});
});
it('marks an undeclared browser with every feature unverified', async () => {
  expect((await browserSupport(base({brands: [], userAgent: 'Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Version/18.0 Safari/605.1.15'}))).state).toBe('unverified');
});
it('recognises Firefox and Edge from the user agent', async () => {
  expect((await browserSupport(base({brands: [], userAgent: 'Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0'}))).state).toBe('supported');
  expect((await browserSupport(base({brands: ['Microsoft Edge']}))).state).toBe('supported');
});
it('names each missing feature', async () => {
  const failing = {generateKey: async () => { throw new Error('NotSupportedError'); }} as unknown as SubtleCrypto;
  const result = await browserSupport(base({subtle: failing, indexedDB: undefined, locks: undefined, secureContext: false}));
  expect(result.state).toBe('unsupported');
  expect(result.missing).toEqual(['Ed25519 signing', 'IndexedDB', 'Web Locks', 'secure context (HTTPS)']);
});
```

- [ ] **Step 2: Run it and see it fail.** `cd web && npx vitest run src/browserSupport.test.ts`. Expected: FAIL, cannot resolve `./browserSupport`.

- [ ] **Step 3: Implement** (`web/src/browserSupport.ts`):

```ts
// Feature checks decide whether chat can run; the declared list only decides whether
// we have verified it. The user agent picks wording, never a security decision.
export const supportedBrowsers = 'current desktop Chrome, Edge and Firefox';

export type BrowserSupport = {state: 'supported' | 'unverified' | 'unsupported'; missing: string[]};
export type SupportEnv = {
  subtle?: SubtleCrypto; indexedDB?: IDBFactory; locks?: LockManager;
  secureContext: boolean; brands: string[]; userAgent: string;
};

function realEnv(): SupportEnv {
  const nav = globalThis.navigator as Navigator & {userAgentData?: {brands: {brand: string}[]}};
  return {
    subtle: globalThis.crypto?.subtle, indexedDB: globalThis.indexedDB, locks: nav?.locks,
    secureContext: globalThis.isSecureContext === true,
    brands: nav?.userAgentData?.brands.map(b => b.brand) ?? [], userAgent: nav?.userAgent ?? '',
  };
}

async function ed25519(subtle?: SubtleCrypto): Promise<boolean> {
  if (!subtle) return false;
  try {
    const pair = await subtle.generateKey({name: 'Ed25519'}, false, ['sign', 'verify']) as CryptoKeyPair;
    await subtle.sign({name: 'Ed25519'}, pair.privateKey, new Uint8Array(1));
    return true;
  } catch { return false; }
}

function declared(env: SupportEnv): boolean {
  if (env.brands.some(b => b === 'Google Chrome' || b === 'Microsoft Edge')) return true;
  const ua = env.userAgent;
  if (/Mobile|Android|iPhone|iPad/.test(ua)) return false;
  return /Firefox\/\d+/.test(ua) || /Edg\/\d+/.test(ua) || (/Chrome\/\d+/.test(ua) && !/OPR\/|Brave/.test(ua));
}

export async function browserSupport(env: SupportEnv = realEnv()): Promise<BrowserSupport> {
  const missing: string[] = [];
  if (!(await ed25519(env.subtle))) missing.push('Ed25519 signing');
  if (!env.indexedDB) missing.push('IndexedDB');
  if (!env.locks) missing.push('Web Locks');
  if (!env.secureContext) missing.push('secure context (HTTPS)');
  if (missing.length) return {state: 'unsupported', missing};
  return {state: declared(env) ? 'supported' : 'unverified', missing};
}
```

- [ ] **Step 4: Run the tests.** Same command. Expected: 4 passed.

- [ ] **Step 5: Write `docs/BROWSER-SUPPORT.md`.**
  - Supported: current desktop Chrome, Edge and Firefox.
  - Not yet verified: Safari (macOS), iOS and Android. Link `docs/BROWSER-EVIDENCE.md` for the WebKit Ed25519 failure.
  - Explain the three states: what each means, that chat refuses `unsupported` and warns on `unverified`, and that the user agent only changes the wording.
  - CI coverage: the chat suites run on Chromium and Firefox, and the console browser regressions on Chromium and Firefox (Task 7).
  - Add one line to `web/AGENTS.md` Local Contracts naming `browserSupport.ts` and its rule.

- [ ] **Step 6: Commit.** `web: check and declare supported browsers for chat`

---

### Task 3: "My account" page

**Files:**
- Create: `web/src/pages/MyAccount.tsx`, `web/src/pages/MyAccount.test.tsx`

**Interfaces:**
- Consumes: `browserSupport()` and `supportedBrowsers` from Task 2. `secureFetch` from `web/src/api`.
- Produces: `export function MyAccount(props: {user: {display_name?: string; username: string; sso_provider: string; sso_subject?: string}; onLogout: () => void})`.

- [ ] **Step 1: Write the failing tests** (`web/src/pages/MyAccount.test.tsx`):

```tsx
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MyAccount } from './MyAccount';

vi.mock('../browserSupport', () => ({
  supportedBrowsers: 'current desktop Chrome, Edge and Firefox',
  browserSupport: async () => ({state: 'supported', missing: []}),
}));
const user = {display_name: 'Alice', username: 'alice', sso_provider: 'kysignon', sso_subject: 'sub-1'};
const device = {id: 'dev-1', name: '<img src=x onerror=alert(1)>', fingerprint: 'ab'.repeat(32), status: 'approved', created_at: 1790000000};
function respond(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), {status, headers: {'Content-Type': 'application/json'}});
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it('shows the account, the chat notice and browser support', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({devices: []})));
  render(<MyAccount user={user} onLogout={() => {}} />);
  expect(screen.getByText('Alice')).toBeTruthy();
  expect(screen.getByText('Encrypted chat is not available on this server yet. It ships after an independent security review.')).toBeTruthy();
  expect(await screen.findByText(/This browser can run KyMessages chat/)).toBeTruthy();
  expect(await screen.findByText('No messaging devices.')).toBeTruthy();
});

it('lists devices safely and revokes one after confirmation', async () => {
  const fetch = vi.fn()
    .mockResolvedValueOnce(respond({devices: [device]}))
    .mockResolvedValueOnce(respond({revoked: true}))
    .mockResolvedValueOnce(respond({devices: [{...device, status: 'revoked'}]}));
  vi.stubGlobal('fetch', fetch);
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
  const {container} = render(<MyAccount user={user} onLogout={() => {}} />);
  expect(await screen.findByText(new RegExp(device.fingerprint))).toBeTruthy();
  expect(container.querySelector('img')).toBeNull();
  fireEvent.click(screen.getByRole('button', {name: /Revoke/}));
  expect(await screen.findByText(/revoked/i)).toBeTruthy();
  expect(confirm).toHaveBeenCalledOnce();
  expect(String(fetch.mock.calls[1][0])).toBe('/api/messaging/devices/dev-1');
  expect(fetch.mock.calls[1][1].method).toBe('DELETE');
  expect(screen.queryByRole('button', {name: /Revoke/})).toBeNull();
});

it('shows a suspended device with its automatic revocation date', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({devices: [{...device, status: 'suspended', expires_at: 1792592000}]})));
  render(<MyAccount user={user} onLogout={() => {}} />);
  expect(await screen.findByText(/Revoked automatically on/)).toBeTruthy();
});

it('non-suite account sees no device actions', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({error: 'Suite OIDC sign-in required'}, 403)));
  render(<MyAccount user={{username: 'admin', sso_provider: 'local'}} onLogout={() => {}} />);
  expect(await screen.findByText('Messaging needs a KySignOn account.')).toBeTruthy();
  expect(screen.queryByRole('button', {name: /Revoke/})).toBeNull();
});

it('rejects a malformed device list', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({devices: [{id: 7}]})));
  render(<MyAccount user={user} onLogout={() => {}} />);
  expect(await screen.findByText('Devices unavailable. Refresh or sign in again.')).toBeTruthy();
});
```

- [ ] **Step 2: Run them and see them fail.** `cd web && npx vitest run src/pages/MyAccount.test.tsx`. Expected: FAIL, cannot resolve `./MyAccount`.

- [ ] **Step 3: Implement `web/src/pages/MyAccount.tsx`.** Follow `SuspendedDevices.tsx`: boundary validators, an AbortController on unmount, `window.confirm` before revoking, a refresh after a revoke, and text nodes only.

```tsx
import { useEffect, useState } from 'react';
import { secureFetch } from '../api';
import { browserSupport, supportedBrowsers, type BrowserSupport } from '../browserSupport';

type Device = {id: string; name: string; fingerprint: string; status: 'pending' | 'approved' | 'suspended' | 'revoked'; createdAt: number; expiresAt?: number};
type Devices = {kind: 'loading'} | {kind: 'ready'; devices: Device[]} | {kind: 'not-suite'} | {kind: 'error'};
const statuses = new Set(['pending', 'approved', 'suspended', 'revoked']);
const invalid = () => { throw new Error('Invalid device list'); };
const text = (v: unknown) => typeof v === 'string' && v && v.length <= 255 ? v : invalid();
const whole = (v: unknown) => typeof v === 'number' && Number.isSafeInteger(v) && v >= 0 ? v : invalid();
const date = (v: unknown) => { const n = whole(v); return Number.isNaN(new Date(n * 1000).getTime()) ? invalid() : n; };

function devicesResponse(value: unknown): Device[] {
  const body = value as {devices?: unknown} | null;
  if (body === null || typeof body !== 'object' || !Array.isArray(body.devices)) invalid();
  return (body as {devices: unknown[]}).devices.map(raw => {
    const d = (raw ?? {}) as Record<string, unknown>;
    if (typeof d.status !== 'string' || !statuses.has(d.status)) invalid();
    return {id: text(d.id), name: text(d.name), fingerprint: text(d.fingerprint), status: d.status as Device['status'],
      createdAt: whole(d.created_at), expiresAt: d.expires_at === undefined ? undefined : date(d.expires_at)};
  });
}
const day = (s: number) => new Date(s * 1000).toLocaleDateString();

// Members' landing page: account, their own messaging devices and browser support.
// Chat itself ships only after the independent review.
export function MyAccount({user, onLogout}: {user: {display_name?: string; username: string; sso_provider: string; sso_subject?: string}; onLogout: () => void}) {
  const [devices, setDevices] = useState<Devices>({kind: 'loading'});
  const [support, setSupport] = useState<BrowserSupport | null>(null);
  const [refresh, setRefresh] = useState(0);

  useEffect(() => { let live = true; void browserSupport().then(s => { if (live) setSupport(s); }); return () => { live = false; }; }, []);
  useEffect(() => {
    const controller = new AbortController();
    void (async () => {
      try {
        const response = await fetch('/api/messaging/devices', {signal: controller.signal, cache: 'no-store'});
        if (response.status === 403) { if (!controller.signal.aborted) setDevices({kind: 'not-suite'}); return; }
        if (!response.ok) throw new Error('unavailable');
        const list = devicesResponse(await response.json());
        if (!controller.signal.aborted) setDevices({kind: 'ready', devices: list});
      } catch { if (!controller.signal.aborted) setDevices({kind: 'error'}); }
    })();
    return () => controller.abort();
  }, [refresh]);

  const revoke = async (device: Device) => {
    if (!window.confirm(`Revoke ${device.name}? It can no longer read or send messages.`)) return;
    const response = await secureFetch('/api/messaging/devices/' + encodeURIComponent(device.id), {method: 'DELETE'});
    if (!response.ok) { setDevices({kind: 'error'}); return; }
    setRefresh(n => n + 1);
  };

  return <section className="card" aria-labelledby="my-account">
    <h2 id="my-account">My account</h2>
    <p>{user.display_name || user.username}</p>
    <p>Signed in as {user.username}{user.sso_provider === 'kysignon' ? ' with KySignOn' : ''}.</p>
    <button type="button" className="btn-secondary" onClick={onLogout}>Sign out</button>
    <p>Encrypted chat is not available on this server yet. It ships after an independent security review.</p>
    <h3>Browser</h3>
    {support === null ? <p role="status">Checking this browser…</p>
      : support.state === 'supported' ? <p>This browser can run KyMessages chat.</p>
      : support.state === 'unverified' ? <p>This browser has every feature chat needs but has not been verified. Supported: {supportedBrowsers}.</p>
      : <p>This browser isn't supported for chat yet. Missing: {support.missing.join(', ')}. Supported: {supportedBrowsers}.</p>}
    <h3>My messaging devices</h3>
    {devices.kind === 'loading' && <p role="status">Loading devices…</p>}
    {devices.kind === 'not-suite' && <p>Messaging needs a KySignOn account.</p>}
    {devices.kind === 'error' && <p role="alert">Devices unavailable. Refresh or sign in again.</p>}
    {devices.kind === 'ready' && (devices.devices.length === 0 ? <p>No messaging devices.</p> :
      <ul style={{listStyle: 'none', padding: 0}}>
        {devices.devices.map(d => <li key={d.id} style={{borderTop: '1px solid var(--line)', padding: '12px 0', overflowWrap: 'anywhere'}}>
          <strong>{d.name}</strong> — {d.status}{d.status === 'suspended' && d.expiresAt ? `. Revoked automatically on ${day(d.expiresAt)}` : ''}
          <div>Fingerprint {d.fingerprint}</div>
          <div>Added {day(d.createdAt)}</div>
          {d.status !== 'revoked' && <button type="button" className="btn-secondary" onClick={() => void revoke(d)}>Revoke {d.name}</button>}
        </li>)}
      </ul>)}
  </section>;
}
```

- [ ] **Step 4: Run the tests.** Same command. Expected: 5 passed. Run `npx tsc -b` and expect no errors.

- [ ] **Step 5: Commit.** `web: My account page with own messaging devices and browser support`

---

### Task 4: Navigation, landing page and role gating

**Files:**
- Modify: `web/src/App.tsx`, `web/src/components/AppHeader.tsx`, `web/AGENTS.md`
- Create: `web/src/components/AppHeader.test.tsx`

**Interfaces:**
- Consumes: `MyAccount` from Task 3.
- Produces: `export function navItemsFor(role: string): {id: string; label: string}[]`, exported from `AppHeader.tsx`. Tab id `'account'`, label `'My account'`.

- [ ] **Step 1: Write the failing tests** (`web/src/components/AppHeader.test.tsx`):

```tsx
import { expect, it } from 'vitest';
import { navItemsFor } from './AppHeader';

it('member sees only My account', () => {
  expect(navItemsFor('user').map(i => i.id)).toEqual(['account']);
  expect(navItemsFor('manager').map(i => i.id)).toEqual(['account']);
});
it('admin sees admin navigation plus My account', () => {
  expect(navItemsFor('admin').map(i => i.id)).toEqual(['dashboard', 'scim', 'backup', 'settings', 'account']);
});
```

- [ ] **Step 2: Run them and see them fail.** `cd web && npx vitest run src/components/AppHeader.test.tsx`. Expected: FAIL, `navItemsFor` is not exported.

- [ ] **Step 3: Implement.**
  - In `AppHeader.tsx`, replace the inline `navItems` array with the code below, and use `navItemsFor(user?.role ?? '')` where the array was used. Import `UserCircle` from `lucide-react`.

    ```tsx
    const adminItems = [
      { id: 'dashboard', label: 'Overview', icon: LayoutDashboard },
      { id: 'scim', label: 'Directory & SCIM', icon: Users },
      { id: 'backup', label: 'Backup & recovery', icon: Archive },
      { id: 'settings', label: 'Settings & DB', icon: SettingsIcon },
    ];
    const accountItem = { id: 'account', label: 'My account', icon: UserCircle };
    // Admin pages are refused server-side for non-admins; navigation only mirrors that.
    export function navItemsFor(role: string) {
      return role === 'admin' ? [...adminItems, accountItem] : [accountItem];
    }
    ```

  - In `App.tsx`, import `MyAccount`, and compute the tab a non-admin is allowed to see:

    ```tsx
    const tab = user.role === 'admin' ? activeTab : 'account';
    ```

    Then render with `tab` instead of `activeTab`. Pass `activeTab={tab}` to `AppHeader`, and add `{tab === 'account' && <MyAccount user={user} onLogout={handleLogout} />}`.
  - `web/AGENTS.md` Local Contracts: add a "My account" bullet. It is the non-admin landing and only page; it validates the device DTO; it revokes through `secureFetch` DELETE after `window.confirm`; a messaging 403 shows `Messaging needs a KySignOn account.`; it is tested by `MyAccount.test.tsx` and `AppHeader.test.tsx`.

- [ ] **Step 4: Run.** `cd web && npm test && npm run build`. Expected: every test passes. The build writes `web/dist`; commit it.

- [ ] **Step 5: Commit.** `web: members land on My account; admin navigation stays admin-only`

---

### Task 5: Admin network self-check

**Files:**
- Modify: `internal/auth/clientip.go` (export helpers), `internal/api/server.go` (route), `web/src/pages/Settings.tsx` (render), `internal/api/AGENTS.md`, `internal/auth/AGENTS.md`
- Create: `internal/api/network_check.go`, `internal/api/network_check_test.go`, `web/src/components/NetworkCheck.tsx`, `web/src/components/NetworkCheck.test.tsx`

**Interfaces:**
- Produces: `auth.PeerIP(r *http.Request) string` (rename of `peerIP`; update its callers) and `auth.TrustedPeer(r *http.Request, trusted []netip.Prefix) bool`.
- Produces: `GET /api/admin/network-check` (requireAdmin, `Cache-Control: no-store`). It returns `{peer_ip, client_ip, forwarded_trusted, forwarded_proto, app_url_https, host_matches}`. `forwarded_proto` is `""` unless the peer is trusted.

- [ ] **Step 1: Write the failing Go tests** (`internal/api/network_check_test.go`). Reuse the package's existing helpers: `setupSQLiteServer`, `loginAs`, and the request helper the backup tests use for admin GETs.

```go
func TestNetworkCheckTrustsForwardedHeadersOnlyFromTrustedPeer(t *testing.T) {
	srv, st, cfg := setupSQLiteServer(t)
	cfg.Server.AppURL = "https://chat.example.com"
	cfg.Security.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.91.0.10/32")}
	session := loginAs(t, srv, st, "net-admin", "admin")
	got := networkCheck(t, srv, session, "10.91.0.10:5555", "chat.example.com", map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"})
	want := map[string]any{"peer_ip": "10.91.0.10", "client_ip": "203.0.113.9", "forwarded_trusted": true, "forwarded_proto": "https", "app_url_https": true, "host_matches": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestNetworkCheckIgnoresForwardedHeadersFromUntrustedPeer(t *testing.T) {
	srv, st, cfg := setupSQLiteServer(t)
	cfg.Server.AppURL = "http://localhost:8080"
	session := loginAs(t, srv, st, "net-admin2", "admin")
	got := networkCheck(t, srv, session, "192.0.2.7:4444", "evil.example", map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"})
	want := map[string]any{"peer_ip": "192.0.2.7", "client_ip": "192.0.2.7", "forwarded_trusted": false, "forwarded_proto": "", "app_url_https": false, "host_matches": false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
```

Write the `networkCheck` helper in the same file. It builds `httptest.NewRequest("GET", "/api/admin/network-check", nil)`, sets `RemoteAddr`, `Host` and the headers, attaches `session`'s cookie the same way the existing admin GET helper does, serves the request through `srv`, requires 200 and a `Cache-Control: no-store` header, and decodes the body to `map[string]any`. Also add `"GET /api/admin/network-check"` to the admin-only table in `internal/api/authz_test.go`, which must give 401 when anonymous and 403 for a non-admin.

- [ ] **Step 2: Run them and see them fail.** `go test ./internal/api/ -run 'NetworkCheck|Authz' -count=1`. Expected: FAIL with 404s.

- [ ] **Step 3: Implement.**
  - In `internal/auth/clientip.go`, rename `peerIP` to `PeerIP` (fix the callers with `gofmt -r 'peerIP -> PeerIP'` or by hand), then add:

    ```go
    // TrustedPeer reports whether the direct peer is a configured proxy, so its forwarded
    // headers are honoured.
    func TrustedPeer(r *http.Request, trusted []netip.Prefix) bool {
    	addr, err := netip.ParseAddr(PeerIP(r))
    	return err == nil && isTrusted(addr, trusted)
    }
    ```

  - Create `internal/api/network_check.go`:

    ```go
    package api

    import (
    	"net/http"
    	"net/url"
    	"strings"

    	"github.com/Busnes-app/ky_server_base/internal/auth"
    )

    // handleNetworkCheck shows an admin how a request reached the server, so a proxy setup
    // can be confirmed instead of assumed. Admin-only: it describes the deployment's wiring.
    func (s *Server) handleNetworkCheck(w http.ResponseWriter, r *http.Request) {
    	w.Header().Set("Cache-Control", "no-store")
    	trusted := auth.TrustedPeer(r, s.config.Security.TrustedProxies)
    	proto := ""
    	if trusted {
    		proto = r.Header.Get("X-Forwarded-Proto")
    	}
    	app, err := url.Parse(s.config.Server.AppURL)
    	s.writeJSON(w, http.StatusOK, map[string]any{
    		"peer_ip":           auth.PeerIP(r),
    		"client_ip":         s.requestIP(r),
    		"forwarded_trusted": trusted,
    		"forwarded_proto":   proto,
    		"app_url_https":     err == nil && strings.EqualFold(app.Scheme, "https"),
    		"host_matches":      err == nil && strings.EqualFold(r.Host, app.Host),
    	})
    }
    ```

  - Register it in `server.go` next to the other admin routes:

    ```go
    s.mux.HandleFunc("GET /api/admin/network-check", s.requireAdmin(s.handleNetworkCheck))
    ```

- [ ] **Step 4: Run.** `go test ./internal/... -count=1`. Expected: ok.

- [ ] **Step 5: Web panel. Write the failing vitest** (`web/src/components/NetworkCheck.test.tsx`):

```tsx
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { NetworkCheck } from './NetworkCheck';

const ok = {peer_ip: '10.91.0.10', client_ip: '203.0.113.9', forwarded_trusted: true, forwarded_proto: 'https', app_url_https: true, host_matches: true};
const respond = (v: unknown) => new Response(JSON.stringify(v), {status: 200, headers: {'Content-Type': 'application/json'}});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('passes a correctly proxied request', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond(ok)));
  render(<NetworkCheck />);
  expect(await screen.findByText(/Your address as seen by the server: 203.0.113.9/)).toBeTruthy();
  expect(screen.queryAllByText(/^Warn:/)).toHaveLength(0);
});
it('warns on an untrusted proxy, plain HTTP and a host mismatch', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({...ok, forwarded_trusted: false, forwarded_proto: '', app_url_https: false, host_matches: false})));
  render(<NetworkCheck />);
  expect(await screen.findAllByText(/^Warn:/)).toHaveLength(3);
});
it('rejects a malformed response', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond({peer_ip: 1})));
  render(<NetworkCheck />);
  expect(await screen.findByText('Network check unavailable.')).toBeTruthy();
});
```

- [ ] **Step 6: Run it and see it fail**, then implement `web/src/components/NetworkCheck.tsx`:

```tsx
import { useEffect, useState } from 'react';

type Check = {peer_ip: string; client_ip: string; forwarded_trusted: boolean; forwarded_proto: string; app_url_https: boolean; host_matches: boolean};
function parse(v: unknown): Check {
  const c = (v ?? {}) as Record<string, unknown>;
  const s = (x: unknown) => { if (typeof x !== 'string' || x.length > 64) throw new Error('bad'); return x; };
  const b = (x: unknown) => { if (typeof x !== 'boolean') throw new Error('bad'); return x; };
  return {peer_ip: s(c.peer_ip), client_ip: s(c.client_ip), forwarded_trusted: b(c.forwarded_trusted), forwarded_proto: s(c.forwarded_proto), app_url_https: b(c.app_url_https), host_matches: b(c.host_matches)};
}
const mark = (pass: boolean, good: string, bad: string) => <li>{pass ? `Pass: ${good}` : `Warn: ${bad}`}</li>;

// Confirms how this request reached the server; see docs/Reverse_Proxy_Networking.md.
export function NetworkCheck() {
  const [check, setCheck] = useState<Check | null | 'error'>(null);
  useEffect(() => {
    const controller = new AbortController();
    fetch('/api/admin/network-check', {signal: controller.signal, cache: 'no-store'})
      .then(async r => { if (!r.ok) throw new Error('unavailable'); setCheck(parse(await r.json())); })
      .catch(() => { if (!controller.signal.aborted) setCheck('error'); });
    return () => controller.abort();
  }, []);
  if (check === null) return <p role="status">Checking the network path…</p>;
  if (check === 'error') return <p role="alert">Network check unavailable.</p>;
  return <section aria-labelledby="network-check"><h3 id="network-check">Network path</h3>
    <p>Your address as seen by the server: {check.client_ip} (direct peer {check.peer_ip}).</p>
    <ul>
      {mark(check.forwarded_trusted, 'the request came through a trusted proxy', 'the direct peer is not in KY_TRUSTED_PROXIES; forwarded headers are ignored')}
      {mark(check.app_url_https, 'KY_APP_URL is https', 'KY_APP_URL is not https')}
      {mark(check.host_matches, 'the request host matches KY_APP_URL', 'the request host does not match KY_APP_URL')}
    </ul></section>;
}
```

  Render `<NetworkCheck />` at the end of the JSX that `Settings.tsx` returns. Run `cd web && npx vitest run src/components/NetworkCheck.test.tsx` and expect 3 passed, then `npm test && npm run build`.

- [ ] **Step 7: Docs.**
  - `internal/api/AGENTS.md`: the route, its admin-only rationale, and that forwarded headers are reported only from a trusted peer.
  - `internal/auth/AGENTS.md`: the exported `PeerIP` and `TrustedPeer`.
  - `web/AGENTS.md`: the `NetworkCheck` panel on Settings.

- [ ] **Step 8: Commit.** `api, web: admin network self-check for proxy deployments`. Include the rebuilt `web/dist`.

---

### Task 6: Proxy Compose overlay and networking guide

**Files:**
- Create: `docker-compose.proxy.yml`, `scripts/check-compose-proxy.sh`, `docs/Reverse_Proxy_Networking.md`
- Modify: `.github/workflows/ci.yml`, `Makefile`, `README.md`, `AGENTS.md`

**Interfaces:**
- Produces: `docker-compose.proxy.yml` and `scripts/check-compose-proxy.sh`, which exits 0 when the overlay is correct.

- [ ] **Step 1: Write the check (the test).**

```bash
#!/usr/bin/env bash
# The proxy overlay must publish nothing, name the network, and keep composing with the
# static-IP overlay. Uses throwaway values; contacts nothing.
set -u
root=$(git rev-parse --show-toplevel)
export KY_ADMIN_PASSWORD=check-only KY_APP_URL=https://chat.example.com KY_CONTAINER_IP=10.91.0.20
render() { docker compose --project-directory "$root" "$@" config --format json; }
fail=0
out=$(render -f "$root/docker-compose.yml" -f "$root/docker-compose.proxy.yml") || exit 1
[ "$(jq '.services.app.ports // [] | length' <<<"$out")" = 0 ] || { echo "proxy overlay publishes a port"; fail=1; }
[ "$(jq -r '.networks.default.name' <<<"$out")" = kymessages-net ] || { echo "network not named kymessages-net"; fail=1; }
[ "$(jq -r '.services.app.environment.KY_ENV' <<<"$out")" = production ] || { echo "KY_ENV not production"; fail=1; }
both=$(render -f "$root/docker-compose.yml" -f "$root/docker-compose.proxy.yml" -f "$root/docker-compose.static-ip.yml") || { echo "proxy + static-ip does not compose"; exit 1; }
[ "$(jq -r '.services.app.networks.default.ipv4_address' <<<"$both")" = 10.91.0.20 ] || { echo "static IP lost with proxy overlay"; fail=1; }
unset KY_APP_URL
if render -f "$root/docker-compose.yml" -f "$root/docker-compose.proxy.yml" >/dev/null 2>&1; then echo "proxy overlay accepted a missing KY_APP_URL"; fail=1; fi
exit $fail
```

- [ ] **Step 2: Run it and see it fail.** `bash scripts/check-compose-proxy.sh; echo $?`. Expected: a non-zero exit, because `docker-compose.proxy.yml` is missing.

- [ ] **Step 3: Write `docker-compose.proxy.yml`.**

```yaml
# Behind a reverse proxy (cloudflared, nginx) on this app's own network. Append to
# COMPOSE_FILE in .env; see docs/Reverse_Proxy_Networking.md. Publishes nothing: the
# proxy joins kymessages-net and reaches http://kymessages:8080 by name.
services:
  app:
    ports: !reset []
    environment:
      - KY_ENV=production
      - KY_APP_URL=${KY_APP_URL:?Set KY_APP_URL to the public https origin}

networks:
  default:
    name: ${KY_NETWORK:-kymessages-net}
    ipam:
      config:
        - subnet: ${KY_NETWORK_SUBNET:-10.91.0.0/24}
```

  Check the combination with the static-IP overlay. That overlay also sets `networks.default.ipam`, and Compose merges both. If `docker compose config` with both files reports a conflict, remove the `ipam` block from the static-IP overlay's `default` network only when the proxy overlay already supplies it. If that's not possible, document that the static-IP overlay replaces the subnet. Make the check script pass either way, and record the choice in the guide.

- [ ] **Step 4: Run the check.** Expected: exit 0. Add `bash scripts/check-compose-proxy.sh` to the CI lint job (ubuntu runners have `docker compose` and `jq`) and to the Makefile `lint` target.

- [ ] **Step 5: Write `docs/Reverse_Proxy_Networking.md`.** Model it on `kypost-server/docs/Reverse_Proxy_Networking.md` and verify every line against this repo. Sections:
  1. Why the proxy shares a network with the app (peer address and IP-keyed lockouts), and why each app has its own network, so apps cannot reach each other.
  2. Setup: add `docker-compose.proxy.yml` to `COMPOSE_FILE`, preserving existing overlays. Set `KY_APP_URL=https://<host>` and `KY_TRUSTED_PROXIES=<proxy /32 on kymessages-net>`, and bring KyMessages up first.
  3. **cloudflared** (primary), joining a second network. It is already on `kypost-net`:

     ```yaml
     services:
       cloudflared:
         networks:
           kypost-net:
             ipv4_address: 10.89.0.10
           kymessages-net:
             ipv4_address: 10.91.0.10
     networks:
       kypost-net:
         external: true
       kymessages-net:
         external: true
     ```

     Tunnel ingress: `service: http://kymessages:8080`, then set `KY_TRUSTED_PROXIES=10.91.0.10/32`. WebSockets need no extra settings.
  4. **nginx** variant: `server` block with TLS, `proxy_pass http://kymessages:8080;`, `proxy_http_version 1.1;`, `proxy_set_header Upgrade $http_upgrade;`, `proxy_set_header Connection $connection_upgrade;` (with the standard `map`), `proxy_set_header Host $host;`, `X-Forwarded-For $proxy_add_x_forwarded_for`, `X-Forwarded-Proto $scheme`, and a `proxy_read_timeout` long enough for live messaging.
  5. Verify: Settings → Network path shows your public address and three Pass marks.
  6. Recovering from `network kymessages-net declared as external, but could not be found`, `incorrect label` and `Pool overlaps` (change `KY_NETWORK_SUBNET`).

  Link it from `README.md` (deployment section) and from root `AGENTS.md`, next to the static-IP overlay bullet. Add one bullet there: the proxy overlay names the network, publishes no port, and is checked by `scripts/check-compose-proxy.sh`.

- [ ] **Step 6: Commit.** `compose: proxy overlay on a per-app network, with a networking guide`

---

### Task 7: Console browser regressions on Firefox, plan docs, full verification

**Files:**
- Modify: `web/playwright.config.mjs`, `web/browser/ui.spec.mjs`, `.github/workflows/ci.yml`, `web/browser/AGENTS.md`, `docs/FIRST-RELEASE-PLAN.md`

- [ ] **Step 1: Add the "My account" assertions (failing first).** In `web/browser/ui.spec.mjs`, after the existing backup steps, add:

```js
  await nav.getByRole('button', { name: 'My account' }).click();
  await expect(page.getByText('Encrypted chat is not available on this server yet. It ships after an independent security review.')).toBeVisible();
  await expect(page.getByText('Messaging needs a KySignOn account.')).toBeVisible();
  await expect(page.getByText(/This browser can run KyMessages chat\./)).toBeVisible();
  await fits(page);
```

  Run `go build -o .browser/server ./cmd/server && cd web && npm run build && npx playwright test`. It passes once Tasks 2–4 are in. If run before them, it fails on the missing button.

- [ ] **Step 2: Add Firefox.** In `web/playwright.config.mjs`, append two projects to the generated array:

```js
  projects: [
    ...['light', 'dark'].flatMap(colorScheme => [390, 1280].map(width => ({
      name: `${colorScheme}-${width}`,
      use: { browserName: 'chromium', colorScheme, viewport: { width, height: 900 } },
    }))),
    { name: 'firefox-light-1280', use: { browserName: 'firefox', colorScheme: 'light', viewport: { width: 1280, height: 900 } } },
    { name: 'firefox-dark-390', use: { browserName: 'firefox', colorScheme: 'dark', viewport: { width: 390, height: 900 } } },
  ],
```

  In ci.yml's browser regressions job, change `npx playwright install --with-deps chromium` to `npx playwright install --with-deps chromium firefox`. Run `npx playwright test` locally and expect 6 passed. If a Chromium-only expectation fails on Firefox (CSP console wording, for example), make the assertion browser-neutral; do not skip the test.

- [ ] **Step 3: Docs.**
  - `web/browser/AGENTS.md`: the Firefox projects and the "My account" assertions.
  - `docs/FIRST-RELEASE-PLAN.md` step 4: the members' "My account" page, the proxy overlay and guide, the browser declaration and check, and the `chat-core` package behind its gate are implemented. Integrating the reviewed client, real-device browser verification and deployed HTTPS evidence remain open. Keep it as short as the neighbouring steps.

- [ ] **Step 4: Full verification.**
  - `make ci`. Expected: `==> Local CI checks passed`.
  - `go test ./...` against a disposable Postgres 17 (your own uniquely named container on a free loopback port; stop it by that name).
  - `bash scripts/check-chat-gate.sh`, `bash scripts/check-compose-proxy.sh`.
  - The mls-proof suites (as in Task 1 Step 5).
  - `scripts/restore-messages-rehearsal.sh`.
  - Commit `web/dist` if the build changed it.

- [ ] **Step 5: Commit.** `web: console browser regressions on Firefox; release plan step 4 status`
