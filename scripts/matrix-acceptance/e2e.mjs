// usage: node e2e.mjs prove|refused|compat|noclaim|media|restored
//        node e2e.mjs room|token|disabled|reread|reads USER
//        node e2e.mjs title BRAND
// Drives Element for scripts/matrix-acceptance.sh. Every hostname maps to the harness TLS
// proxy on loopback, and the browser trusts only that proxy's key (SPKI pin, which Chromium
// honours only with a user data dir, hence persistent contexts). Writes its findings to
// $ACCEPT_DIR/state/<scenario>[-<user>].json; exits non-zero when a UI-level check fails.
import { chromium } from 'playwright';
import fs from 'node:fs';
import { randomBytes } from 'node:crypto';

const { ACCEPT_DIR: dir, ACCEPT_PORT: port, ACCEPT_SPKI: spki, ACCEPT_ARTIFACTS: artifacts } = process.env;
const [scenario, user] = process.argv.slice(2);
const CHAT = 'https://chat.kymatrix.test';
const MATRIX = 'https://matrix.kymatrix.test';
const SERVER = 'kymatrix.test';
const contexts = [];

const pass = (user) => fs.readFileSync(`${dir}/state/${user}.pass`, 'utf8').trim();
const stateFile = (name) => `${dir}/state/${name}${user ? `-${user}` : ''}.json`;
const out = (data) => fs.writeFileSync(stateFile(scenario), JSON.stringify(data, null, 2));
const tag = randomBytes(6).toString('hex');
const text = (label) => `kymatrix-${scenario}-${label}-${tag}`;
function check(ok, what) {
  if (!ok) throw new Error(`check failed: ${what}`);
  console.log(`  ok: ${what}`);
}

// A profile reused across runs keeps that Element session: alice's from prove is reused.
async function launch(name, profile = `${scenario}-${name}`) {
  const ctx = await chromium.launchPersistentContext(`${dir}/profiles/${profile}`, {
    viewport: { width: 1280, height: 900 },
    permissions: ['camera', 'microphone'],
    args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', `--host-resolver-rules=MAP turn.kymatrix.test 127.0.0.1, MAP *.kymatrix.test 127.0.0.1:${port}`, `--ignore-certificate-errors-spki-list=${spki}`],
  });
  await ctx.addInitScript(({force, turnPort}) => {
    window.kyCallPeers = [];
    const Original = window.RTCPeerConnection;
    const rewrite = (config = {}) => {
      return {...config, ...(force ? {iceTransportPolicy:'relay'} : {}), iceServers:(config.iceServers ?? []).filter(server => !force || (Array.isArray(server.urls) ? server.urls : [server.urls]).some(url => url.startsWith('turns:'))).map(server => ({...server, urls:(Array.isArray(server.urls) ? server.urls : [server.urls]).filter(url => !force || url.startsWith('turns:')).map(url => url?.replace('turns:turn.kymatrix.test:443', `turns:turn.kymatrix.test:${turnPort}`))}))};
    };
    window.RTCPeerConnection = window.webkitRTCPeerConnection = class extends Original {
      constructor(config, ...args) { super(rewrite(config), ...args); window.kyCallPeers.push(this); console.log("ky-call-peer", force, this.getConfiguration().iceServers?.map(s=>s.urls)); this.kyIceErrors=[]; this.addEventListener("icecandidateerror", e => (this.kyIceErrors.push({code:e.errorCode,text:e.errorText,url:e.url}),console.log("ky-call-ice-error",e.errorCode,e.errorText,e.url))); }
      setConfiguration(config) { return super.setConfiguration(rewrite(config)); }
    };
  }, {force:process.env.KYMATRIX_ACCEPT_FORCE_TURN === '1', turnPort:process.env.KYMATRIX_ACCEPT_TURN_PORT});
  await ctx.tracing.start({ screenshots: true, snapshots: true });
  contexts.push([name, ctx]);
  const page = ctx.pages()[0] ?? (await ctx.newPage());
  page.setDefaultTimeout(30000);
  return page;
}

// KyIdentity's login form renders before it settles and can clear a field filled too early:
// fill once it is idle, and refill until both values held.
async function fillLogin(page, user) {
  await page.waitForLoadState('networkidle');
  for (let i = 0; i < 3; i++) {
    await page.fill('#username', user);
    await page.fill('#password', pass(user));
    if ((await page.inputValue('#username')) === user && (await page.inputValue('#password')) === pass(user)) return;
    await page.waitForTimeout(500);
  }
  throw new Error(`KyIdentity's login form kept losing ${user}'s credentials`);
}

// KyIdentity's login form, then MAS's account and consent pages, until back in Element.
async function kyidentityLogin(page, user) {
  await page.waitForURL(/^https:\/\/id\.kymatrix\.test\/login/);
  await fillLogin(page, user);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL(/^https:\/\/auth\.kymatrix\.test\//);
  for (let i = 0; i < 4 && !page.url().startsWith(CHAT); i++) {
    await page.getByRole('button', { name: /^(Create Account|Continue)$/ }).first().click();
    await page.waitForLoadState('load');
  }
  await page.waitForURL((u) => u.href.startsWith(CHAT));
}

// The room list's button: Element's notifications prompt can cover "Start chat".
async function signedIn(page, expectMxid) {
  await page.getByRole('button', { name: 'New conversation' }).waitFor();
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
  return shown;
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

async function invite(page, who) {
  await page.getByRole('button', { name: 'Invite to this room' }).click();
  const inv = page.getByRole('dialog');
  await inv.getByRole('textbox').fill(who);
  await inv.getByText(who).first().click();
  await inv.getByRole('button', { name: 'Invite', exact: true }).click();
  // A contact with no shared room yet needs a second confirmation.
  await page.waitForFunction(() => {
    const d = document.querySelector('[role="dialog"]');
    return !d || d.innerText.includes('Invite new contacts');
  });
  if (await inv.count()) await inv.getByRole('button', { name: 'Invite', exact: true }).click();
  await inv.waitFor({ state: 'detached' });
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
  fs.writeFileSync(`${dir}/state/bob.recovery`, await setUpRecovery(bob), { mode: 0o600 });
  // The localpart is the email's local part, lowercased and sanitised, never the username:
  // KyIdentity user AQuinn has email Alice.Q+Ky@kymatrix.test.
  const aliceId = await nativeSignIn(alice, 'AQuinn', `@alice.q_ky:${SERVER}`);
  fs.writeFileSync(`${dir}/state/alice.recovery`, await setUpRecovery(alice), { mode: 0o600 });

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
  await invite(alice, bobId);

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
  await fillLogin(page, 'mallory');
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

// An assigned user whose ID token carries no email (the client's scopes were narrowed to drop
// email): the localpart template renders empty and MAS's required import refuses the sign-in.
async function noclaim() {
  const page = await launch('nadia');
  await page.goto(`${CHAT}/#/login`);
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await page.waitForURL(/^https:\/\/id\.kymatrix\.test\/login/);
  await fillLogin(page, 'nadia');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL(/^https:\/\/auth\.kymatrix\.test\//);
  await page.waitForLoadState('networkidle');
  const body = await page.locator('body').innerText();
  check(/rendered to an empty string/.test(body) && body.includes('user.email | split'), 'MAS refused: the email localpart template rendered empty');
  check(!page.url().startsWith(CHAT), 'nadia never reached Element');
  out({ url: page.url().replace(/[?#].*/, ''), page: body.slice(0, 500) });
}

// Offboarding scenarios, one KyIdentity user each (USER).
const mxid = () => `@${user}:${SERVER}`;
// NAME's browser session from an earlier scenario's PROFILE.
async function resume(name, profile) {
  const page = await launch(name, profile);
  // Element would otherwise reopen the room prove left open.
  await page.goto(`${CHAT}/#/home`);
  // Element's session lock outlives the closed browser: "open in another window", whose Continue
  // takes the session over. The lock can also clear on its own between looking and clicking,
  // and then the only Continue left is the "Back up your chats" toast's, which opens settings:
  // click the Continue beside the lock text only, and close any dialog that opened anyway.
  const start = page.getByRole('button', { name: 'New conversation' });
  const locked = page.getByText(/is open in another window/);
  const continueBtn = page.getByRole('button', { name: 'Continue', exact: true });
  const takeOver = page.locator('div').filter({ has: locked }).filter({ has: continueBtn }).last().getByRole('button', { name: 'Continue', exact: true });
  for (let i = 0; i < 3 && !(await start.isVisible()); i++) {
    await start.or(locked).or(page.getByRole('dialog')).first().waitFor();
    if (await locked.isVisible()) await takeOver.click({ timeout: 5000 }).catch(() => {});
    await closeDialogs(page);
  }
  await start.waitFor();
  return page;
}

// Closes any dialog left open (a settings dialog would take a file upload's place).
async function closeDialogs(page) {
  const dialog = page.getByRole('dialog');
  for (let i = 0; i < 3 && (await dialog.count()); i++) {
    await page.keyboard.press('Escape');
    await dialog.first().waitFor({ state: 'detached', timeout: 5000 }).catch(() => {});
  }
  check((await dialog.count()) === 0, 'no dialog open');
}

const aliceSession = () => resume('alice', 'prove-alice');
const roomState = () => JSON.parse(fs.readFileSync(stateFile('room'), 'utf8'));

// Element uploads room keys to the key backup in the background; wait until WHO's backup holds
// the session of every encrypted event in WHO's timeline of the room (at least two: both
// senders'), so a later device can decrypt all of them, the latest included.
async function backedUp(page, id, who = user) {
  const state = () => page.evaluate(async ({ hs, id }) => {
    const client = window.mxMatrixClientPeg.get();
    const auth = { headers: { Authorization: `Bearer ${client.getAccessToken()}` } };
    const { version } = await (await fetch(`${hs}/_matrix/client/v3/room_keys/version`, auth)).json();
    const keys = await (await fetch(`${hs}/_matrix/client/v3/room_keys/keys/${encodeURIComponent(id)}?version=${version}`, auth)).json();
    const used = new Set(client.getRoom(id).getLiveTimeline().getEvents().filter((e) => e.isEncrypted()).map((e) => e.getWireContent().session_id));
    return { used: used.size, missing: [...used].filter((s) => !(s in (keys.sessions ?? {}))).length };
  }, { hs: MATRIX, id });
  let s;
  for (let i = 0; i < 60 && ((s = await state()).used < 2 || s.missing > 0); i++) await page.waitForTimeout(1000);
  check(s.used >= 2 && s.missing === 0, `${who}'s backup holds every room key its timeline uses (${s.used} sessions, ${s.missing} missing)`);
}

// USER signs in and sets up recovery; alice (her session from prove) invites USER to a new
// room; each reads the other's message.
async function room() {
  const peer = await launch(user);
  await nativeSignIn(peer, user, mxid());
  const recoveryKey = await setUpRecovery(peer);
  fs.writeFileSync(`${dir}/state/${user}.recovery`, recoveryKey, { mode: 0o600 });
  const alice = await aliceSession();
  const id = await createRoom(alice, `kymatrix-${user}-${tag}`);
  await invite(alice, mxid());
  await openRoom(peer, id, true);
  const messages = { alice: text('alice'), peer: text(user) };
  await send(alice, messages.alice);
  await sees(peer, messages.alice);
  await send(peer, messages.peer);
  await sees(alice, messages.peer);
  await backedUp(peer, id);
  out({ room: id, messages });
}

// A second Element session for USER: the access token of its first client-server request,
// written 0600 for the harness to probe. The browser closes once it has it.
async function token() {
  const page = await launch(user);
  const authorized = page.waitForRequest(
    async (r) => r.url().startsWith(`${MATRIX}/_matrix/client/`) && /^Bearer /.test((await r.headerValue('authorization')) ?? ''),
    { timeout: 60000 },
  );
  await page.goto(`${CHAT}/#/login`);
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await kyidentityLogin(page, user);
  const bearer = (await (await authorized).headerValue('authorization')).slice('Bearer '.length);
  fs.writeFileSync(`${dir}/state/${user}.token`, bearer, { mode: 0o600 });
  console.log(`  ok: captured a live Element token for ${mxid()}`);
}

// A disabled account: KyIdentity refuses the password sign-in, so Element never opens.
async function disabled() {
  const page = await launch(user);
  await page.goto(`${CHAT}/#/login`);
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await page.waitForURL(/^https:\/\/id\.kymatrix\.test\/login/);
  await fillLogin(page, user);
  const login = page.waitForResponse((r) => r.url() === 'https://id.kymatrix.test/api/auth/login' && r.request().method() === 'POST');
  await page.getByRole('button', { name: 'Sign in' }).click();
  const status = (await login).status();
  check(status === 401, `KyIdentity refused ${user}'s fresh sign-in (${status})`);
  check(!page.url().startsWith(CHAT), `${user} never reached Element`);
  out({ status, url: page.url().replace(/[?#].*/, '') });
}

// After reactivation: a new device signs in, confirms its identity with the recovery key
// from room, and reads alice's earlier message from key backup.
async function reread() {
  const { room: id, messages } = roomState();
  const page = await launch(user);
  await page.goto(`${CHAT}/#/login`);
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await kyidentityLogin(page, user);
  // A new device of an account with cross-signing lands on "Confirm your identity".
  await page.getByRole('button', { name: 'Use recovery key', exact: true }).click();
  const dlg = page.getByRole('dialog');
  await dlg.locator('input, textarea').first().fill(fs.readFileSync(`${dir}/state/${user}.recovery`, 'utf8'));
  await dlg.getByRole('button', { name: 'Continue', exact: true }).click();
  await page.getByRole('button', { name: 'Done', exact: true }).click();
  await signedIn(page, mxid());
  await openRoom(page, id, false);
  await sees(page, messages.alice);
  out({ room: id });
}

// alice still reads USER's message after USER is gone.
async function reads() {
  const { room: id, messages } = roomState();
  const alice = await aliceSession();
  await openRoom(alice, id, false);
  await sees(alice, messages.peer);
  out({ room: id });
}

// A 1x1 PNG: Element uploads it encrypted, so Synapse's local_content holds ciphertext.
const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';

// Element renders only its timeline window: with the live end below it ("Scroll to most recent
// messages" shown), a new image has no tile until the view jumps there.
async function decryptedImage(page) {
  const img = timeline(page).locator('.mx_ImageBody img').last();
  const jump = page.getByRole('button', { name: 'Scroll to most recent messages' });
  await img.or(jump).first().waitFor({ timeout: 60000 });
  if (await jump.isVisible()) await jump.click();
  await img.waitFor({ timeout: 60000 });
  await page.waitForFunction((el) => el.complete && el.naturalWidth > 0, await img.elementHandle(), { timeout: 60000 });
  console.log('  ok: image decrypted and shown');
}

// Bob sends an image into the DM; alice sees it; alice's key backup then holds the DM's keys.
async function media() {
  const { rooms: { dm } } = JSON.parse(fs.readFileSync(`${dir}/state/prove.json`, 'utf8'));
  const bob = await resume('bob', 'prove-bob');
  await openRoom(bob, dm, false);
  await closeDialogs(bob);
  const name = `kymatrix-${tag}.png`;
  await bob.locator('input[type="file"]').first().setInputFiles({ name, mimeType: 'image/png', buffer: Buffer.from(PNG, 'base64') });
  await bob.getByRole('dialog').getByRole('button', { name: 'Upload' }).click();
  // Sent, not a local echo ('$' event ID); read from the client, as the tile may be outside
  // Element's timeline window.
  await bob.waitForFunction(({ dm, name }) => {
    const client = window.mxMatrixClientPeg.get();
    return client.getRoom(dm).getLiveTimeline().getEvents().some((e) =>
      e.getSender() === client.getUserId() && e.getId()?.startsWith('$') && e.getContent().msgtype === 'm.image' && e.getContent().body === name);
  }, { dm, name }, { timeout: 30000 });
  console.log('  ok: bob sent the image');
  const alice = await aliceSession();
  await openRoom(alice, dm, false);
  await decryptedImage(alice);
  await backedUp(alice, dm, 'alice');
  out({ dm });
}

async function recoveredSignIn(page, name, expected) {
  await page.goto(`${CHAT}/#/login`);
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await kyidentityLogin(page, name);
  await page.getByRole('button', { name: 'Use recovery key', exact: true }).click();
  const dlg = page.getByRole('dialog');
  await dlg.locator('input, textarea').first().fill(fs.readFileSync(`${dir}/state/${name === "AQuinn" ? "alice" : name}.recovery`, 'utf8'));
  await dlg.getByRole('button', { name: 'Continue', exact: true }).click();
  await page.getByRole('button', { name: 'Done', exact: true }).click();
  return signedIn(page, expected);
}

// After the restore: alice on a new device confirms her identity with her recovery key, then
// reads bob's earlier message and image from key backup, and sends one message.
async function restored() {
  const { rooms: { dm }, messages } = JSON.parse(fs.readFileSync(`${dir}/state/prove.json`, 'utf8'));
  const page = await launch('alice', 'restored-alice');
  const mxid = await recoveredSignIn(page, 'AQuinn', `@alice.q_ky:${SERVER}`);
  await openRoom(page, dm, false);
  await sees(page, messages[1]); // prove's dm2, bob's DM message
  await decryptedImage(page);
  const after = text('after-restore');
  await send(page, after);
  // The reproduction step's native-session control needs a live token.
  fs.writeFileSync(`${dir}/state/alice.token`, await page.evaluate(() => window.mxMatrixClientPeg.get().getAccessToken()), { mode: 0o600 });
  out({ mxid, after });
}

// Element's tab title carries the brand the console set: each page load fetches config.json.
async function title() {
  const page = await launch('element');
  await page.goto(`${CHAT}/#/login`);
  await page.waitForFunction((brand) => document.title.includes(brand), user, { timeout: 30000 });
  console.log(`  ok: Element's title is "${await page.title()}"`);
}

async function calls(restored = false) {
  const alice = await resume('alice', restored ? 'restored-alice' : 'prove-alice');
  const bob = restored ? await launch('bob', 'restored-bob') : await resume('bob', 'prove-bob');
  if (restored) await recoveredSignIn(bob, 'bob', `@bob:${SERVER}`);
  const { rooms } = JSON.parse(fs.readFileSync(`${dir}/state/prove.json`, 'utf8'));
  // Inspect all embedded call frames; Element ships its own call app.
  async function join(page, initiate, room = rooms.group, video = true) {
    await openRoom(page, room, false);
    if (initiate) await page.getByRole('button', { name: video ? /Video call|Start video call/i : /Voice call|Start voice call|Audio call/i }).first().click();
    else await page.getByRole('button', { name: /Join call|Join video call|Video call|Voice call|Audio call/i }).first().click();
    for (let i = 0; i < 30; i++) {
      for (const frame of page.frames()) {
        const join = frame.getByRole('button', { name: /^(Join call|Join|Start call|Start)$/i }).first();
        if (await join.isVisible()) await join.click();
        if (await frame.getByRole('button', { name: 'End call', exact: true }).isVisible()) return;
      }
      await page.waitForTimeout(1000);
    }
    const diagnostics = [];
    for (const frame of page.frames()) diagnostics.push(await frame.evaluate(() => (window.kyCallPeers ?? []).map(pc => ({state:pc.connectionState,ice:pc.iceConnectionState,errors:pc.kyIceErrors,config:pc.getConfiguration().iceTransportPolicy,remote:pc.remoteDescription?.sdp.split('\r\n').filter(line=>line.startsWith('a=candidate:'))}))));
    throw new Error(`Call never reached its connected view: ${JSON.stringify(diagnostics)}`);
  }
  await join(alice, true);
  await join(bob, false);
  async function received(page) {
    const stats = [];
    for (const frame of page.frames()) {
      stats.push(...await frame.evaluate(async () => {
        const all = [];
        for (const pc of window.kyCallPeers ?? []) {
          const report = await pc.getStats();
          for (const v of report.values()) {
            if (v.type === 'candidate-pair' && v.state === 'succeeded' && v.nominated) all.push({kind:'transport', local:report.get(v.localCandidateId)?.candidateType});
            if (v.type === 'inbound-rtp') all.push({kind:v.kind, bytes:v.bytesReceived, frames:v.framesDecoded, samples:v.totalSamplesReceived});
          }
        }
        return all;
      }));
    }
    return stats;
  }
  async function proveMedia(video) {
    let a, b;
    for (let i = 0; i < 60; i++) {
      a = await received(alice); b = await received(bob);
      const media = (stats) => stats.some(s => s.kind==='audio' && s.bytes>0 && s.samples>0) && (!video || stats.some(s => s.kind==='video' && s.frames>0));
      const relay = stats => stats.some(s => s.kind==='transport' && s.local==='relay');
      if (media(a) && media(b) && (process.env.KYMATRIX_ACCEPT_FORCE_TURN !== '1' || (relay(a) && relay(b)))) return {alice:a, bob:b};
      await alice.waitForTimeout(1000);
    }
    throw new Error(`Both users must receive ${video ? 'audio and decoded video' : 'audio'}: ${JSON.stringify({a,b})}`);
  }
  const video = await proveMedia(true);
  check(true, 'both Element clients receive audio and decode group video');
  for (const page of [alice, bob]) {
    for (const frame of page.frames()) {
      const end = frame.getByRole('button', {name:'End call', exact:true});
      if (await end.isVisible()) await end.click();
    }
    for (const frame of page.frames()) await frame.evaluate(() => {window.kyCallPeers = []});
  }
  await join(alice, true, rooms.dm, false);
  await join(bob, false, rooms.dm, false);
  const voice = await proveMedia(false);
  check(!voice.alice.some(s => s.kind==='video' && s.frames>0) && !voice.bob.some(s => s.kind==='video' && s.frames>0), 'DM voice call sends audio without video');
  check(true, 'both Element clients receive DM voice audio');
  out({group:rooms.group, dm:rooms.dm, video, voice});
}

const scenarios = { calls, restoredcalls: () => calls(true), prove, refused, compat, noclaim, room, token, disabled, reread, reads, media, restored, title };
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
