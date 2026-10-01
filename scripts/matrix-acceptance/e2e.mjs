// usage: node e2e.mjs prove|refused|compat|noclaim
// Drives Element for scripts/matrix-acceptance.sh. Every hostname maps to the harness TLS
// proxy on loopback, and the browser trusts only that proxy's key (SPKI pin, which Chromium
// honours only with a user data dir, hence persistent contexts). Writes its findings to
// $ACCEPT_DIR/state/<scenario>.json; exits non-zero when a UI-level check fails.
import { chromium } from 'playwright';
import fs from 'node:fs';
import { randomBytes } from 'node:crypto';

const { ACCEPT_DIR: dir, ACCEPT_PORT: port, ACCEPT_SPKI: spki, ACCEPT_ARTIFACTS: artifacts } = process.env;
const scenario = process.argv[2];
const CHAT = 'https://chat.kymatrix.test';
const MATRIX = 'https://matrix.kymatrix.test';
const SERVER = 'kymatrix.test';
const contexts = [];

const pass = (user) => fs.readFileSync(`${dir}/state/${user}.pass`, 'utf8').trim();
const out = (data) => fs.writeFileSync(`${dir}/state/${scenario}.json`, JSON.stringify(data, null, 2));
const tag = randomBytes(6).toString('hex');
const text = (label) => `kymatrix-${scenario}-${label}-${tag}`;
function check(ok, what) {
  if (!ok) throw new Error(`check failed: ${what}`);
  console.log(`  ok: ${what}`);
}

async function launch(name) {
  const ctx = await chromium.launchPersistentContext(`${dir}/profiles/${scenario}-${name}`, {
    viewport: { width: 1280, height: 900 },
    args: [`--host-resolver-rules=MAP *.kymatrix.test 127.0.0.1:${port}`, `--ignore-certificate-errors-spki-list=${spki}`],
  });
  await ctx.tracing.start({ screenshots: true, snapshots: true });
  contexts.push([name, ctx]);
  const page = ctx.pages()[0] ?? (await ctx.newPage());
  page.setDefaultTimeout(30000);
  return page;
}

// KyIdentity's login form, then MAS's account and consent pages, until back in Element.
async function kyidentityLogin(page, user) {
  await page.waitForURL(/^https:\/\/id\.kymatrix\.test\/login/);
  await page.fill('#username', user);
  await page.fill('#password', pass(user));
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL(/^https:\/\/auth\.kymatrix\.test\//);
  for (let i = 0; i < 4 && !page.url().startsWith(CHAT); i++) {
    await page.getByRole('button', { name: /^(Create Account|Continue)$/ }).first().click();
    await page.waitForLoadState('load');
  }
  await page.waitForURL((u) => u.href.startsWith(CHAT));
}

async function signedIn(page, expectMxid) {
  await page.getByRole('button', { name: 'Start chat' }).waitFor();
  const mxid = await page.evaluate(() => localStorage.getItem('mx_user_id'));
  check(mxid === expectMxid, `signed in as ${expectMxid} (got ${mxid})`);
  return mxid;
}

// Element's native OIDC sign-in: Continue on the login page goes to MAS, then KyIdentity.
async function nativeSignIn(page, user, expectMxid) {
  await page.goto(`${CHAT}/#/login`);
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await kyidentityLogin(page, user);
  return signedIn(page, expectMxid);
}

// Element's key setup: it bootstraps cross-signing and key backup itself; the "Back up your
// chats" toast then creates the recovery key, which the user confirms by typing it back.
async function setUpRecovery(page) {
  const toast = page.locator('.mx_Toast_toast').filter({ hasText: 'Back up your chats' });
  await toast.getByRole('button', { name: 'Continue', exact: true }).click();
  const dlg = page.getByRole('dialog');
  await dlg.getByRole('button', { name: 'Get recovery key' }).click();
  await dlg.getByRole('button', { name: 'Continue', exact: true }).click();
  const shown = await dlg.getByText(/^(?:[A-Za-z0-9]{4} ){11}[A-Za-z0-9]{4}$/).innerText();
  await dlg.getByRole('button', { name: 'Continue', exact: true }).click();
  await dlg.locator('input, textarea').first().fill(shown);
  await dlg.getByRole('button', { name: /^(Finish set up|Continue)$/ }).first().click();
  await dlg.getByText(/Change recovery key/).waitFor();
  await page.keyboard.press('Escape');
  await dlg.waitFor({ state: 'detached' });
  console.log('  ok: recovery key set up');
}

const composer = (page) => page.locator('div[role="textbox"][contenteditable="true"]').first();
const timeline = (page) => page.locator('.mx_RoomView_MessageList');
const roomId = (page) => decodeURIComponent(page.url().split('#/room/')[1] ?? '');

async function send(page, body) {
  await composer(page).fill(body);
  await composer(page).press('Enter');
  // Sent, not a local echo: the server's event ID starts with '$' (local echoes use '~').
  await page.locator('.mx_EventTile[data-event-id^="$"]').filter({ hasText: body }).waitFor();
}

// The other side must read it: the message was encrypted to its devices, not just sealed.
async function sees(page, body) {
  await timeline(page).getByText(body).waitFor({ timeout: 60000 });
  console.log(`  ok: ${body} decrypted and shown`);
}

async function createRoom(page, name) {
  const before = page.url();
  await page.getByRole('button', { name: 'New conversation' }).click();
  await page.getByRole('menuitem', { name: 'New room' }).click();
  await page.getByLabel('Name').fill(name);
  await page.getByRole('button', { name: 'Create room' }).click();
  await page.waitForURL((u) => u.href !== before && u.hash.startsWith('#/room/!'));
  await composer(page).waitFor();
  return roomId(page);
}

async function openRoom(page, id, invited) {
  await page.goto(`${CHAT}/#/room/${id}`);
  if (invited) {
    // "Start chatting" for a DM, "Accept" for a room.
    await page.getByRole('button', { name: /^(Accept|Start chatting)$/ }).click();
  }
  await composer(page).waitFor();
}

async function prove() {
  const bob = await launch('bob');
  const alice = await launch('alice');
  const bobId = await nativeSignIn(bob, 'bob', `@bob:${SERVER}`);
  await setUpRecovery(bob);
  // Mixed-case KyIdentity username: the localpart is its lowercased, sanitised form.
  const aliceId = await nativeSignIn(alice, 'Alice.Q@Ky', `@alice.q_ky:${SERVER}`);
  await setUpRecovery(alice);

  const msgs = { dm1: text('dm-alice'), dm2: text('dm-bob'), g1: text('group-alice'), g2: text('group-bob') };

  // DM: Element creates the room on the first message.
  await alice.getByRole('button', { name: 'Start chat' }).click();
  const dlg = alice.getByRole('dialog');
  await dlg.getByRole('textbox').fill(bobId);
  await dlg.getByText(bobId).first().click();
  await dlg.getByRole('button', { name: 'Go' }).click();
  // "Start a chat with this new contact?" when the two share no room yet.
  const confirm = alice.getByRole('dialog').getByRole('button', { name: 'Continue', exact: true });
  await confirm.or(composer(alice)).first().waitFor();
  if (await confirm.isVisible()) await confirm.click();
  await send(alice, msgs.dm1);
  await alice.waitForURL(/#\/room\/!/);
  const dm = roomId(alice);

  const group = await createRoom(alice, `kymatrix-group-${tag}`);
  await alice.getByRole('button', { name: 'Invite to this room' }).click();
  const inv = alice.getByRole('dialog');
  await inv.getByRole('textbox').fill(bobId);
  await inv.getByText(bobId).first().click();
  await inv.getByRole('button', { name: 'Invite', exact: true }).click();
  await inv.waitFor({ state: 'detached' });

  await openRoom(bob, dm, true);
  await sees(bob, msgs.dm1);
  await send(bob, msgs.dm2);
  await openRoom(bob, group, true);
  await send(alice, msgs.g1);
  await sees(bob, msgs.g1);
  await send(bob, msgs.g2);

  await sees(alice, msgs.g2);
  await openRoom(alice, dm, false);
  await sees(alice, msgs.dm2);

  // For the reproduction step's native-session control (scratch only, 0600 by umask).
  const token = await alice.evaluate(() => window.mxMatrixClientPeg.get().getAccessToken());
  fs.writeFileSync(`${dir}/state/alice.token`, token, { mode: 0o600 });
  out({ users: [aliceId, bobId], rooms: { dm, group }, messages: Object.values(msgs) });
}

// An unassigned KyIdentity user: KyIdentity answers MAS with access_denied.
async function refused() {
  const page = await launch('mallory');
  const seen = [];
  page.on('request', (r) => seen.push(r.url()));
  await page.goto(`${CHAT}/#/login`);
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await page.waitForURL(/^https:\/\/id\.kymatrix\.test\/login/);
  await page.fill('#username', 'mallory');
  await page.fill('#password', pass('mallory'));
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL(/^https:\/\/auth\.kymatrix\.test\/upstream\/callback\/.*error=access_denied/);
  await page.waitForLoadState('load');
  const denied = seen.filter((u) => /\/upstream\/callback\/.*[?&]error=access_denied/.test(u));
  check(denied.length > 0, 'KyIdentity returned access_denied to MAS');
  const body = await page.locator('body').innerText();
  check(!page.url().startsWith(CHAT), 'mallory never reached Element');
  out({ callback: denied[0].replace(/state=[^&]+/, 'state=…'), page: body.slice(0, 300) });
}

// Reproduction: Element's SSO sign-in, which MAS serves through its compatibility layer
// (m.login.sso, then m.login.token). Element takes it when it does not use native OIDC; here
// the harness starts it the way Element's "Continue with SSO" does: remember the homeserver,
// then go to the SSO redirect with Element as the return URL.
async function compat() {
  const page = await launch('rita');
  await page.goto(`${CHAT}/#/login`);
  await page.evaluate((hs) => localStorage.setItem('mx_sso_hs_url', hs), MATRIX);
  await page.goto(`${MATRIX}/_matrix/client/v3/login/sso/redirect?redirectUrl=${encodeURIComponent(`${CHAT}/`)}`);
  const kyid = page.getByRole('link', { name: /KyIdentity/ }).or(page.getByRole('button', { name: /KyIdentity/ }));
  await page.locator('#username').or(kyid).first().waitFor();
  if (!page.url().startsWith('https://id.kymatrix.test')) await kyid.first().click();
  await kyidentityLogin(page, 'rita');
  const mxid = await signedIn(page, `@rita:${SERVER}`);
  const session = await page.evaluate(() => ({ device: localStorage.getItem('mx_device_id'), oidc: Object.keys(localStorage).filter((k) => k.startsWith('mx_oidc')) }));
  const room = await createRoom(page, `kymatrix-compat-${tag}`);
  const body = text('rita-element');
  await send(page, body);
  // Control: the same compat session's token sending through the plain client-server API,
  // which encrypts nothing. Shows what Synapse stores when a client does not encrypt.
  const raw = text('rita-raw-api');
  const rawStatus = await page.evaluate(async ({ hs, room, raw }) => {
    const token = window.mxMatrixClientPeg.get().getAccessToken();
    const url = `${hs}/_matrix/client/v3/rooms/${encodeURIComponent(room)}/send/m.room.message/kymatrix-raw`;
    const r = await fetch(url, { method: 'PUT', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ msgtype: 'm.text', body: raw }) });
    return r.status;
  }, { hs: MATRIX, room, raw });
  check(rawStatus === 200, `raw API send with the compat token accepted (${rawStatus})`);
  out({ users: [mxid], device: session.device, oidcKeys: session.oidc, rooms: { room }, element: body, raw });
}

// An assigned user whose ID token carries no preferred_username (the client's scopes were
// narrowed to drop profile): MAS's required localpart import must refuse the sign-in.
async function noclaim() {
  const page = await launch('nadia');
  await page.goto(`${CHAT}/#/login`);
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await page.waitForURL(/^https:\/\/id\.kymatrix\.test\/login/);
  await page.fill('#username', 'nadia');
  await page.fill('#password', pass('nadia'));
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL(/^https:\/\/auth\.kymatrix\.test\//);
  await page.waitForLoadState('networkidle');
  const body = await page.locator('body').innerText();
  check(/rendered to an empty string/.test(body), 'MAS refused: localpart template rendered empty');
  check(!page.url().startsWith(CHAT), 'nadia never reached Element');
  out({ url: page.url().replace(/[?#].*/, ''), page: body.slice(0, 500) });
}

const scenarios = { prove, refused, compat, noclaim };
let failed = false;
try {
  if (!scenarios[scenario]) throw new Error(`unknown scenario ${scenario}`);
  await scenarios[scenario]();
} catch (err) {
  failed = true;
  console.error(`e2e ${scenario}: ${err.message}`);
}
for (const [name, ctx] of contexts) {
  const path = failed ? `${artifacts}/trace-${scenario}-${name}.zip` : undefined;
  if (failed) {
    fs.mkdirSync(artifacts, { recursive: true });
    for (const [i, pg] of ctx.pages().entries()) await pg.screenshot({ path: `${artifacts}/${scenario}-${name}-${i}.png` }).catch(() => {});
  }
  await ctx.tracing.stop({ path }).catch(() => {});
  await ctx.close().catch(() => {});
}
process.exit(failed ? 1 : 0);
