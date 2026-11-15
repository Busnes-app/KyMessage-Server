// usage: node e2e.mjs prove|refused|compat|noclaim|media|restored
//        node e2e.mjs room|token|fedtoken|disabled|reread|reads|noclaim|owner USER
//        AGENT_MXID=... AGENT_MSG=... AGENT_REPLY=... node e2e.mjs agent USER
//        node e2e.mjs title BRAND
//        MEETING_ROOM=... node e2e.mjs meeting
//        GUEST_MXID=... MEETING_ROOM=... node e2e.mjs guestsignin|guestknock (spikes S1, S2)
//        GUEST_NEXT=... MEETING_ROOM=... node e2e.mjs guestmeeting
//        KYCAL_ACTION=... node e2e.mjs calendar; node e2e.mjs signin USER
// Drives Element for scripts/matrix-acceptance.sh. Every hostname maps to the harness TLS
// proxy on loopback; the harness temporarily trusts its throwaway CA in Chromium's NSS
// database and removes it on exit. Persistent contexts retain test sessions. Writes findings to
// $ACCEPT_DIR/state/<scenario>[-<user>].json; exits non-zero when a UI-level check fails.
import { chromium } from 'playwright';
import fs from 'node:fs';
import { randomBytes } from 'node:crypto';

const { ACCEPT_DIR: dir, ACCEPT_PORT: port, ACCEPT_ARTIFACTS: artifacts } = process.env;
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
    args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', `--host-resolver-rules=MAP turn.kymatrix.test 127.0.0.1, MAP *.kymatrix.test 127.0.0.1:${port}`,
      // The notes step's participant speaks a known sentence, and its page records what it hears.
      ...(process.env.KYMATRIX_ACCEPT_FAKE_AUDIO ? [`--use-file-for-fake-audio-capture=${process.env.KYMATRIX_ACCEPT_FAKE_AUDIO}`, '--autoplay-policy=no-user-gesture-required'] : [])],
  });
  await ctx.addInitScript(({force, turnPort}) => {
    window.kyCallPeers = [];
    const Original = window.RTCPeerConnection;
    const rewrite = (config = {}) => {
      return {...config, ...(force ? {iceTransportPolicy:'relay'} : {}), iceServers:(config.iceServers ?? []).filter(server => !force || (Array.isArray(server.urls) ? server.urls : [server.urls]).some(url => url.startsWith('turns:'))).map(server => ({...server, urls:(Array.isArray(server.urls) ? server.urls : [server.urls]).filter(url => !force || url.startsWith('turns:')).map(url => url?.replace('turns:turn.kymatrix.test:443', `turns:turn.kymatrix.test:${turnPort}`))}))};
    };
    window.RTCPeerConnection = window.webkitRTCPeerConnection = class extends Original {
      constructor(config, ...args) { super(rewrite(config), ...args); window.kyCallPeers.push(this); this.kyIceErrors=[]; this.addEventListener("icecandidateerror", e => (this.kyIceErrors.push({code:e.errorCode,text:e.errorText,url:e.url}))); }
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

// KyIdentity's login form, then MAS's account and consent pages, until back in Element. The
// form is rendered by script; if Chrome aborts it (ERR_NETWORK_CHANGED while Compose brings the
// restored stack up) the page stays blank, so reload, at most twice.
async function kyidentityLogin(page, user) {
  await page.waitForURL(/^https:\/\/id\.kymatrix\.test\/login/);
  for (let i = 0; ; i++) {
    try {
      await page.locator('#username').waitFor({ timeout: 10000 });
      break;
    } catch (err) {
      if (i === 2) throw err;
      console.log("  note: KyIdentity's login form did not render; reloading it");
      await page.reload();
    }
  }
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

// Element's login page: Continue goes to MAS. Element registers its client with MAS once per
// load; if Chrome aborts that request (ERR_NETWORK_CHANGED while Compose adds an interface) it
// shows a homeserver error instead of Continue, so reload, at most twice.
async function startSignIn(page) {
  const go = page.getByRole('button', { name: 'Continue', exact: true });
  const failed = page.getByText('There was a problem communicating with the homeserver');
  await page.goto(`${CHAT}/#/login`);
  for (let i = 0; ; i++) {
    await go.or(failed).first().waitFor();
    if (await go.isVisible()) return go.click();
    if (i === 2) throw new Error('Element could not reach MAS on three loads of its login page');
    console.log('  note: Element could not reach MAS; reloading its login page');
    await page.reload();
  }
}

// Element's native OIDC sign-in: Continue on the login page goes to MAS, then KyIdentity.
async function nativeSignIn(page, user, expectMxid) {
  await startSignIn(page);
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
  await startSignIn(page);
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

// An assigned user (USER, default nadia) whose ID token carries no verified email (the
// client's scopes were narrowed to drop email, or KyIdentity never verified it): the localpart
// template renders empty and MAS's required import refuses the sign-in.
async function noclaim() {
  const who = user || 'nadia';
  const page = await launch(who);
  await startSignIn(page);
  await page.waitForURL(/^https:\/\/id\.kymatrix\.test\/login/);
  await fillLogin(page, who);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL(/^https:\/\/auth\.kymatrix\.test\//);
  await page.waitForLoadState('networkidle');
  const body = await page.locator('body').innerText();
  check(/rendered to an empty string/.test(body) && body.includes('user.email | split'), 'MAS refused: the email localpart template rendered empty');
  check(!page.url().startsWith(CHAT), `${who} never reached Element`);
  out({ url: page.url().replace(/[?#].*/, ''), page: body.slice(0, 500) });
}

// Offboarding scenarios, one KyIdentity user each (USER).
const mxid = () => `@${user}:${SERVER}`;
// NAME's browser session from an earlier scenario's PROFILE.
async function resume(name, profile) {
  const page = await launch(name, profile);
  // Element would otherwise reopen the room prove left open.
  await page.goto(`${CHAT}/#/home`);
  await takeOver(page, page.getByRole('button', { name: 'New conversation' }));
  return page;
}

// Waits for READY on an Element page that may first show the session lock or a dialog.
async function takeOver(page, ready) {
  // Element's session lock outlives the closed browser: "open in another window", whose Continue
  // takes the session over. The lock can also clear on its own between looking and clicking,
  // and then the only Continue left is the "Back up your chats" toast's, which opens settings:
  // click the Continue beside the lock text only, and close any dialog that opened anyway.
  const start = ready;
  const locked = page.getByText(/is open in another window/);
  const continueBtn = page.getByRole('button', { name: 'Continue', exact: true });
  const takeOver = page.locator('div').filter({ has: locked }).filter({ has: continueBtn }).last().getByRole('button', { name: 'Continue', exact: true });
  for (let i = 0; i < 3 && !(await start.isVisible()); i++) {
    await start.or(locked).or(page.getByRole('dialog')).first().waitFor();
    if (await locked.isVisible()) await takeOver.click({ timeout: 5000 }).catch(() => {});
    await closeDialogs(page);
  }
  await start.waitFor();
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
// written 0600 for the harness to probe. The browser closes once it has it. fedtoken is the
// same in a fresh profile, for a user whose token profile the console already signed out.
async function token() {
  const page = await launch(user);
  const authorized = page.waitForRequest(
    async (r) => r.url().startsWith(`${MATRIX}/_matrix/client/`) && /^Bearer /.test((await r.headerValue('authorization')) ?? ''),
    { timeout: 60000 },
  );
  await startSignIn(page);
  await kyidentityLogin(page, user);
  const bearer = (await (await authorized).headerValue('authorization')).slice('Bearer '.length);
  fs.writeFileSync(`${dir}/state/${user}.token`, bearer, { mode: 0o600 });
  console.log(`  ok: captured a live Element token for ${mxid()}`);
}

// A disabled account: KyIdentity refuses the password sign-in, so Element never opens.
async function disabled() {
  const page = await launch(user);
  await startSignIn(page);
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
  await startSignIn(page);
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
  await startSignIn(page);
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

// Agents: USER, an agent's owner, signs in and sets up recovery (owner); then (agent) invites
// $AGENT_MXID to a new room, or reuses it, sends $AGENT_MSG and reads the agent's reply
// $AGENT_REPLY decrypted.
async function owner() {
  const page = await launch(user, 'agent-owner');
  await nativeSignIn(page, user, mxid());
  await setUpRecovery(page);
}

async function agent() {
  const { AGENT_MXID: agentId, AGENT_MSG: msg, AGENT_REPLY: reply } = process.env;
  const page = await resume(user, 'agent-owner');
  let id;
  if (fs.existsSync(stateFile('agent'))) {
    id = JSON.parse(fs.readFileSync(stateFile('agent'), 'utf8')).room;
    // A room opened before the first live sync keeps the cached timeline window, which never
    // shows new events.
    await page.waitForFunction(() => window.mxMatrixClientPeg.get()?.getSyncState() === 'SYNCING', null, { timeout: 60000 });
    await openRoom(page, id, false);
  } else {
    id = await createRoom(page, `kymatrix-agent-${tag}`);
    await invite(page, agentId);
    await page.waitForFunction(({ id, agentId }) => window.mxMatrixClientPeg.get().getRoom(id)?.getMember(agentId)?.membership === 'join',
      { id, agentId }, { timeout: 120000 });
    console.log(`  ok: ${agentId} joined ${id}`);
  }
  await send(page, msg);
  await sees(page, reply);
  out({ room: id });
}

// Element's tab title carries the brand the console set: each page load fetches config.json.
async function title() {
  const page = await launch('element');
  await page.goto(`${CHAT}/#/login`);
  await page.waitForFunction((brand) => document.title.includes(brand), user, { timeout: 30000 });
  console.log(`  ok: Element's title is "${await page.title()}"`);
}

// Element Call helpers: alice and bob are the two participants' pages.
// Inspect all embedded call frames; Element ships its own call app.
async function joinCall(page, initiate, room, video = true) {
  await openRoom(page, room, false);
  if (initiate) await page.getByRole('button', { name: video ? /Video call|Start video call/i : /Voice call|Start voice call|Audio call/i }).first().click();
  else await page.getByRole('button', { name: /Join call|Join video call|Video call|Voice call|Audio call/i }).first().click();
  // Element replaces its call iframe during startup; resolve it again on each action.
  const call = page.frameLocator('iframe[title="Element Call"]');
  for (let i = 0; i < 30; i++) {
    if (await call.getByRole('button', { name: 'End call', exact: true }).isVisible()) return;
    // An incoming call can ask for confirmation in Element before creating its iframe.
    const confirm = page.getByRole('button', { name: /^(Join call|Join|Start call|Start)$/i }).first();
    const join = call.getByRole('button', { name: /^(Join call|Join|Start call|Start)$/i }).first();
    if (await confirm.isVisible()) await confirm.click();
    else if (await join.isVisible()) await join.click();
    await page.waitForTimeout(1000);
  }
  const diagnostics = [];
  for (const frame of page.frames()) diagnostics.push(await frame.evaluate(() => (window.kyCallPeers ?? []).map(pc => ({state:pc.connectionState,ice:pc.iceConnectionState,errors:pc.kyIceErrors,config:pc.getConfiguration().iceTransportPolicy,remote:pc.remoteDescription?.sdp.split('\r\n').filter(line=>line.startsWith('a=candidate:'))}))));
  throw new Error(`Call never reached its connected view: ${JSON.stringify(diagnostics)}`);
}
async function received(page) {
  const stats = [];
  for (const frame of page.frames()) {
    stats.push(...await frame.evaluate(async () => {
      const all = [];
      for (const pc of window.kyCallPeers ?? []) {
        const report = await pc.getStats();
        for (const v of report.values()) {
          if (v.type === 'candidate-pair' && v.state === 'succeeded' && v.nominated) all.push({kind:'transport', policy:pc.getConfiguration().iceTransportPolicy, local:report.get(v.localCandidateId)?.candidateType, relayProtocol:report.get(v.localCandidateId)?.relayProtocol});
          if (v.type === 'inbound-rtp') all.push({kind:v.kind, bytes:v.bytesReceived, frames:v.framesDecoded, samples:v.totalSamplesReceived});
        }
      }
      return all;
    }));
  }
  return stats;
}
async function proveMedia(alice, bob, video) {
  let a, b;
  for (let i = 0; i < 60; i++) {
    a = await received(alice); b = await received(bob);
    const media = (stats) => stats.some(s => s.kind==='audio' && s.bytes>0 && s.samples>0) && (!video || stats.some(s => s.kind==='video' && s.frames>0));
    // Chromium can label the selected TURN candidate prflx after peer discovery.
    const relay = stats => stats.some(s => s.kind==='transport' && s.policy==='relay' && s.relayProtocol==='tls');
    if (media(a) && media(b) && (process.env.KYMATRIX_ACCEPT_FORCE_TURN !== '1' || (relay(a) && relay(b)))) return {alice:a, bob:b};
    await alice.waitForTimeout(1000);
  }
  throw new Error(`Both users must receive ${video ? 'audio and decoded video' : 'audio'}: ${JSON.stringify({a,b})}`);
}
async function hangup(...pages) {
  for (const page of pages) {
    // Controls can briefly hide while the other participant leaves; always hang up.
    const end = page.frameLocator('iframe[title="Element Call"]').getByRole('button', {name:'End call', exact:true});
    await end.click({timeout:5000}).catch(async err => {
      // Ending a DM can also close the other participant's iframe during this click.
      if (await page.locator('iframe[title="Element Call"]').count()) throw err;
    });
    // Leaving is asynchronous; changing rooms early can retain the group-call view.
    await end.waitFor({state:'hidden'});
    for (const frame of page.frames()) await frame.evaluate(() => {window.kyCallPeers = []});
  }
}

async function calls(restored = false) {
  const alice = await resume('alice', restored ? 'restored-alice' : 'prove-alice');
  const force = process.env.KYMATRIX_ACCEPT_FORCE_TURN === '1';
  const bob = restored && !force ? await launch('bob', 'restored-bob') : await resume('bob', restored ? 'restored-bob' : 'prove-bob');
  if (restored && !force) await recoveredSignIn(bob, 'bob', `@bob:${SERVER}`);
  const { rooms } = JSON.parse(fs.readFileSync(`${dir}/state/prove.json`, 'utf8'));
  await joinCall(alice, true, rooms.group);
  await joinCall(bob, false, rooms.group);
  const video = await proveMedia(alice, bob, true);
  check(true, 'both Element clients receive audio and decode group video');
  await hangup(alice, bob);
  await joinCall(alice, true, rooms.dm, false);
  await joinCall(bob, false, rooms.dm, false);
  const voice = await proveMedia(alice, bob, false);
  check(!voice.alice.some(s => s.kind==='video' && s.frames>0) && !voice.bob.some(s => s.kind==='video' && s.frames>0), 'DM voice call sends audio without video');
  check(true, 'both Element clients receive DM voice audio');
  // The next scenario reuses these profiles; leave no active call for it to resume.
  await hangup(alice, bob);
  out({group:rooms.group, dm:rooms.dm, video, voice});
}

// A KyCalendar meeting room the console created (step meetings, MEETING_ROOM): alice and bob
// accept its invites, read each other's encrypted messages, cannot add a room widget, and
// hold a video call in it (three joined members, counting the console).
async function meeting() {
  const id = process.env.MEETING_ROOM;
  const msgs = { a: text('alice'), b: text('bob') };
  // Each profile is opened once and left to the shared close at exit: reopening one quickly can
  // lose a rotated refresh token, which ends that Element session for the later steps.
  const alice = await resume('alice', 'prove-alice');
  const bob = await resume('bob', 'prove-bob');
  // Both join first: history is visible from joining, so nothing sent before is shown.
  for (const [name, page] of [['alice', alice], ['bob', bob]]) {
    await openRoom(page, id, true);
    check(await page.evaluate((id) => window.mxMatrixClientPeg.get().getRoom(id)?.hasEncryptionStateEvent(), id), `${name}'s Element treats the meeting room as encrypted`);
  }
  await send(alice, msgs.a);
  await sees(bob, msgs.a);
  await send(bob, msgs.b);
  await sees(alice, msgs.b);

  // Widgets UI is on so Element shows calls in rooms of three or more; adding a room widget
  // still needs power level 100 here (the organizer has 50), whichever client sends it.
  const { rooms } = JSON.parse(fs.readFileSync(`${dir}/state/prove.json`, 'utf8'));
  const addWidget = (page, room) => page.evaluate(async (room) => {
    const url = 'https://widget.kymatrix.test/w';
    try {
      await window.mxMatrixClientPeg.get().sendStateEvent(room, 'im.vector.modular.widgets', { type: 'm.custom', url, name: 'w', data: {} }, 'kymatrix-widget');
      return 'sent';
    } catch (e) { return e.errcode ?? String(e); }
  }, room);
  for (const [name, page] of [['alice', alice], ['bob', bob]]) {
    check(await addWidget(page, id) === 'M_FORBIDDEN', `Synapse refuses ${name}'s room widget in the meeting room`);
  }
  check(await addWidget(bob, rooms.group) === 'M_FORBIDDEN', 'Synapse refuses a power level 0 member\'s room widget in an Element group room');
  await openRoom(bob, id, false);
  await composer(bob).fill('/addwidget https://widget.kymatrix.test/w');
  await composer(bob).press('Enter');
  await bob.getByRole('dialog').getByText('You cannot modify widgets in this room.').waitFor();
  check(true, "Element's /addwidget is refused for bob in the meeting room");
  await closeDialogs(bob);
  await composer(bob).fill('');

  check(await alice.evaluate((id) => window.mxMatrixClientPeg.get().getRoom(id).getJoinedMemberCount(), id) === 3, 'the meeting room has three joined members (alice, bob, the console)');
  await openRoom(alice, id, false);
  const button = alice.getByRole('button', { name: /Video call|Start video call/i }).first();
  await button.waitFor();
  fs.mkdirSync(`${artifacts}/evidence`, { recursive: true });
  await alice.screenshot({ path: `${artifacts}/evidence/meeting-call-button-alice.png` });
  await joinCall(alice, true, id);
  await joinCall(bob, false, id);
  const video = await proveMedia(alice, bob, true);
  check(true, 'both Element clients receive audio and decode video in the three-member meeting room');
  await alice.screenshot({ path: `${artifacts}/evidence/meeting-call-alice.png` });
  await bob.screenshot({ path: `${artifacts}/evidence/meeting-call-bob.png` });
  // Later steps reuse these profiles; leave no active call for them to resume.
  await hangup(alice, bob);
  out({ room: id, messages: Object.values(msgs), video });
}

// A real KyCalendar (step suite-meetings): alice, in her Element profile, drives the event
// editor. KYCAL_ACTION is create (KYCAL_TITLE, KYCAL_PEOPLE: comma-separated usernames,
// KYCAL_JOIN=1 to open Join), rename (KYCAL_TITLE to KYCAL_NEW), remove (KYCAL_PEOPLE from
// KYCAL_TITLE), off (its video meeting switch) or delete (KYCAL_TITLE). Writes the event's uid,
// calendar and meeting_url.
const CAL = 'https://cal.kymatrix.test';
async function calSignIn(page) {
  await page.goto(`${CAL}/api/sso/kysignon/login`);
  const newEvent = page.getByRole('button', { name: 'New event' });
  // KyIdentity's form, unless alice's KyIdentity session from Element still holds; then any consent.
  const atKyIdentity = () => new URL(page.url()).origin === 'https://id.kymatrix.test';
  for (let i = 0; i < 10 && !(await newEvent.isVisible()); i++) {
    if (atKyIdentity() && (await page.locator('#username').isVisible())) {
      await fillLogin(page, 'AQuinn');
      await page.getByRole('button', { name: 'Sign in' }).click();
    } else {
      const consent = page.getByRole('button', { name: /^(Allow|Approve|Authorize|Continue)$/ }).first();
      if (atKyIdentity() && (await consent.isVisible())) await consent.click();
    }
    await page.waitForTimeout(1000);
  }
  await newEvent.waitFor();
}
const listEvents = (page) => page.evaluate(async () => {
  const day = 864e5, now = Date.now();
  const q = new URLSearchParams({ start: new Date(now - 2 * day).toISOString(), end: new Date(now + 2 * day).toISOString() });
  const r = await fetch(`/api/events?${q}`);
  return r.ok ? r.json() : [];
});
async function findEvent(page, title, ready = () => true) {
  for (let i = 0; i < 90; i++) {
    const ev = (await listEvents(page)).find((e) => e.title === title);
    if (ev && ready(ev)) return ev;
    await page.waitForTimeout(1000);
  }
  throw new Error(`no event "${title}" in the state wanted within 90s`);
}
// From the List view: events sharing a slot in the week view are too narrow to click.
async function openEvent(page, title) {
  await page.locator('.fc-listWeek-button').click();
  await page.locator('.fc-list-event', { hasText: title }).first().click();
  const dlg = page.locator('dialog.kc-dialog[open]');
  await dlg.waitFor();
  return dlg;
}
const shot = async (page, name) => {
  fs.mkdirSync(`${artifacts}/evidence`, { recursive: true });
  await page.screenshot({ path: `${artifacts}/evidence/${name}.png` });
};
async function calendar() {
  const { KYCAL_ACTION: action, KYCAL_TITLE: title, KYCAL_NEW: renamed, KYCAL_PEOPLE: people = '', KYCAL_JOIN: join } = process.env;
  const page = await launch('alice', 'prove-alice');
  await calSignIn(page);
  const save = async (dlg) => {
    await dlg.getByRole('button', { name: 'Save', exact: true }).click();
    await dlg.waitFor({ state: 'detached' });
  };
  if (action === 'create') {
    await page.getByRole('button', { name: 'New event' }).click();
    const dlg = page.locator('dialog.kc-dialog[open]');
    await dlg.locator('#kc-title').fill(title);
    // Today at noon, local time: the calendar opens on today.
    const d = new Date();
    const day = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
    await dlg.locator('#kc-start').fill(`${day}T12:00`);
    await dlg.locator('#kc-end').fill(`${day}T13:00`);
    const sw = dlg.getByRole('switch', { name: 'Video meeting' });
    check(!(await sw.isChecked()), 'the Video meeting switch is shown, off for an event without guests');
    for (const who of people.split(',').filter(Boolean)) {
      await dlg.locator('#kc-invite').fill(`${who}@kymatrix.test`);
      await dlg.getByRole('button', { name: 'Invite', exact: true }).click();
    }
    check(await sw.isChecked(), 'the switch turns on by default once the event has guests');
    // KyCalendar's line for an event without outside attendees (contract L, KyCalendar #146).
    check(await dlg.getByText('People outside your organisation get their own guest link in their invitation when KyMessages lets guests join.').isVisible(),
      'the editor says how people outside the organisation would join');
    if (join) await shot(page, 'kycalendar-editor-video-meeting');
    await save(dlg);
  } else if (action === 'delete') {
    const dlg = await openEvent(page, title);
    page.once('dialog', (d) => d.accept());
    await dlg.getByRole('button', { name: 'Delete', exact: true }).click();
    await dlg.waitFor({ state: 'detached' });
    out({ deleted: title });
    return;
  } else {
    const dlg = await openEvent(page, title);
    if (action === 'rename') await dlg.locator('#kc-title').fill(renamed);
    if (action === 'off') await dlg.getByRole('switch', { name: 'Video meeting' }).uncheck();
    if (action === 'remove') {
      for (const who of people.split(',').filter(Boolean)) await dlg.getByRole('button', { name: `Remove ${who}@kymatrix.test` }).click();
    }
    await save(dlg);
  }
  const want = action === 'rename' ? renamed : title;
  const ev = await findEvent(page, want, (e) => action === 'off' ? !e.meeting : action !== 'create' || !!e.meeting_url);
  if (join) {
    // Join from the event view opens the room in Element, in alice's own Element session. The
    // page loaded the event before KyCalendar wrote the link into it: load it again.
    await page.reload();
    await page.getByRole('button', { name: 'New event' }).waitFor();
    const dlg = await openEvent(page, want);
    const button = dlg.getByRole('button', { name: 'Join video meeting' });
    await button.waitFor();
    await shot(page, 'kycalendar-event-join');
    const [tab] = await Promise.all([page.waitForEvent('popup'), button.click()]);
    await tab.waitForURL((u) => u.href.startsWith(`${CHAT}/#/room/`));
    const id = ev.meeting_url.split('#/room/')[1];
    check(decodeURIComponent(tab.url().split('#/room/')[1]) === decodeURIComponent(id), 'Join opened the meeting room in Element');
    // alice is invited: Element shows the room's invite, which step meeting accepts.
    await takeOver(tab, tab.getByRole('button', { name: 'Accept' }));
    await shot(tab, 'kycalendar-join-opens-element');
  }
  out({ title: ev.title, uid: ev.uid, calendar: ev.calendar_id, meeting: !!ev.meeting, meeting_url: ev.meeting_url ?? '' });
}

// Element sign-in for USER, who has none yet: MAS creates the account.
async function signin() {
  const page = await launch(user);
  await nativeSignIn(page, user, mxid());
}

// Meeting notes (scripts/matrix-acceptance.sh, step notes). USER owns the rooms.
const NOTES = `@kymessages-notes:${SERVER}`;
const memberOf = (page, id, who, membership) => page.waitForFunction(({ id, who, membership }) =>
  window.mxMatrixClientPeg.get().getRoom(id)?.getMember(who)?.membership === membership, { id, who, membership }, { timeout: 120000 });
const synced = (page) => page.waitForFunction(() => window.mxMatrixClientPeg.get()?.getSyncState() === 'SYNCING', null, { timeout: 60000 });

// A member without power level 100 invites the bot: it refuses in the room and leaves.
async function notesinvite() {
  const owner = await resume(user, 'agent-owner');
  const member = await resume('bob', 'prove-bob');
  await Promise.all([synced(owner), synced(member)]);
  const id = await createRoom(owner, `kymatrix-notes-member-${tag}`);
  await invite(owner, `@bob:${SERVER}`);
  await memberOf(member, id, `@bob:${SERVER}`, 'invite');
  await openRoom(member, id, true);
  await invite(member, NOTES);
  await sees(owner, "Only the room's creator or an admin can invite meeting notes");
  await memberOf(owner, id, NOTES, 'leave');
  console.log('  ok: the bot left the room a member invited it to');
  out({ room: id });
}

// USER invites the bot, starts a video call speaking KYMATRIX_ACCEPT_FAKE_AUDIO, pauses and
// resumes it by chat command, hangs up, and reads the notes. Element Call's decoded audio from
// the bot goes to state/notes-heard.wav.
async function notesmeeting() {
  const page = await resume(user, 'agent-owner');
  await synced(page);
  const id = await createRoom(page, `kymatrix-notes-${tag}`);
  await invite(page, NOTES);
  await memberOf(page, id, NOTES, 'join');
  await sees(page, 'Meeting notes will join the next call in this room');
  // A video call: the bot must take the audio and never subscribe to the camera.
  await page.getByRole('button', { name: /Video call|Start video call/i }).first().click();
  const call = page.frameLocator('iframe[title="Element Call"]');
  const end = call.getByRole('button', { name: 'End call', exact: true });
  for (let i = 0; i < 30 && !(await end.isVisible()); i++) {
    for (const b of [page, call].map((p) => p.getByRole('button', { name: /^(Join call|Join|Start call|Start)$/i }).first())) if (await b.isVisible()) await b.click();
    await page.waitForTimeout(1000);
  }
  await end.waitFor();
  console.log('  ok: in the call');
  // Tap each remote audio track as Element Call decrypted and decoded it.
  const tap = () => Promise.all(page.frames().map((f) => f.evaluate(() => {
    window.kyHeard ??= {};
    for (const pc of window.kyCallPeers ?? []) for (const r of pc.getReceivers()) {
      if (r.track?.kind !== 'audio' || window.kyHeard[r.track.id]) continue;
      const ctx = (window.kyAudio ??= new AudioContext());
      const chunks = (window.kyHeard[r.track.id] = { rate: ctx.sampleRate, chunks: [] }).chunks;
      const proc = ctx.createScriptProcessor(4096, 1, 1);
      proc.onaudioprocess = (e) => chunks.push(new Float32Array(e.inputBuffer.getChannelData(0)));
      ctx.createMediaStreamSource(new MediaStream([r.track])).connect(proc);
      proc.connect(ctx.destination);
    }
  }).catch(() => {})));
  const say = (body) => page.evaluate(({ id, body }) => window.mxMatrixClientPeg.get().sendTextMessage(id, body), { id, body });
  for (let i = 0; i < 80; i++) {
    await tap();
    if (i === 40) await say('!notes pause');
    if (i === 56) await say('!notes resume');
    await page.waitForTimeout(500);
  }
  let best;
  for (const f of page.frames()) {
    const tracks = await f.evaluate(() => Object.values(window.kyHeard ?? {}).map(({ rate, chunks }) => {
      const pcm = new Int16Array(chunks.reduce((n, c) => n + c.length, 0));
      let o = 0, energy = 0;
      for (const c of chunks) for (const v of c) { pcm[o++] = Math.max(-1, Math.min(1, v)) * 32767; energy += v * v; }
      let bin = '';
      const bytes = new Uint8Array(pcm.buffer);
      for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
      return { rate, energy, pcm: btoa(bin) };
    })).catch(() => []);
    for (const t of tracks) if (!best || t.energy > best.energy) best = t;
  }
  check(best?.energy > 0, 'Element Call decoded audible audio from the bot');
  const pcm = Buffer.from(best.pcm, 'base64');
  const h = Buffer.alloc(44);
  h.write('RIFF', 0); h.writeUInt32LE(36 + pcm.length, 4); h.write('WAVEfmt ', 8); h.writeUInt32LE(16, 16); h.writeUInt16LE(1, 20); h.writeUInt16LE(1, 22);
  h.writeUInt32LE(best.rate, 24); h.writeUInt32LE(best.rate * 2, 28); h.writeUInt16LE(2, 32); h.writeUInt16LE(16, 34); h.write('data', 36); h.writeUInt32LE(pcm.length, 40);
  fs.writeFileSync(`${dir}/state/notes-heard.wav`, Buffer.concat([h, pcm]));
  // Hang up; alone, the bot ends the meeting, posts the notes and leaves.
  await end.click();
  await end.waitFor({ state: 'hidden' });
  await openRoom(page, id, false);
  await sees(page, 'Meeting notes joined the call. This meeting is being recorded for note taking.');
  await sees(page, 'Note taking paused by');
  await sees(page, 'Note taking resumed by');
  await timeline(page).getByText(/Meeting notes: kymatrix-notes-/).waitFor({ timeout: 180000 });
  const notes = await timeline(page).getByText(/Meeting notes: kymatrix-notes-/).last().innerText();
  console.log(`  notes:\n${notes.replace(/^/gm, '    ')}`);
  check(/budget review moves to tuesday/i.test(notes), "the notes hold the owner's sentence");
  check(/Paused by .+ until \d\d:\d\d/.test(notes), 'the notes mark the pause');
  await memberOf(page, id, NOTES, 'leave');
  console.log('  ok: the bot left the room after the meeting');
  out({ room: id, heardSeconds: pcm.length / 2 / best.rate });
}

// Shared-screen snapshots. Chromium's fake screen never holds still, as a playing video would
// not, so the shared screen here is a canvas of still slides; window.kySlide picks the slide.
async function slidesForScreenShare(page) {
  await page.addInitScript(() => {
    if (!navigator.mediaDevices) return;
    navigator.mediaDevices.getDisplayMedia = async () => {
      const c = document.createElement('canvas');
      c.width = 1280; c.height = 720;
      const x = c.getContext('2d');
      const draw = () => {
        const second = window.kySlide === 2;
        x.fillStyle = second ? '#1d3557' : '#ffffff'; x.fillRect(0, 0, 1280, 720);
        x.fillStyle = second ? '#ffffff' : '#000000'; x.font = '72px sans-serif';
        x.fillText(second ? 'Slide 2: next steps' : 'Slide 1: budget review', 120, 360);
      };
      draw();
      setInterval(draw, 200); // repaint so the stream keeps producing frames
      return c.captureStream(5);
    };
  });
}

// waitNotice: a bot notice containing text arrives in room id (read through Element's client;
// the call view hides the timeline).
const waitNotice = (page, id, text, timeout = 60000) => page.waitForFunction(({ id, text }) =>
  window.mxMatrixClientPeg.get().getRoom(id)?.getLiveTimeline().getEvents().some((e) => e.getContent()?.body?.includes(text)),
  { id, text }, { timeout }).then(() => console.log(`  ok: ${text} decrypted`));

// USER's video call: a share is skipped until snapshots are turned on; then the bot announces
// it and takes slide 1. The harness then turns snapshots off in Settings (state/screens-revoke →
// state/screens-revoked): the bot must say so and take nothing more, from slide 2 or from a new
// share. After the hangup Element decrypts and shows the one snapshot.
async function notesscreens() {
  const page = await resume(user, 'agent-owner');
  await slidesForScreenShare(page);
  await page.reload();
  await synced(page);
  const id = await createRoom(page, `kymatrix-screens-${tag}`);
  await invite(page, NOTES);
  await memberOf(page, id, NOTES, 'join');
  await page.getByRole('button', { name: /Video call|Start video call/i }).first().click();
  const call = page.frameLocator('iframe[title="Element Call"]');
  const end = call.getByRole('button', { name: 'End call', exact: true });
  for (let i = 0; i < 30 && !(await end.isVisible()); i++) {
    for (const b of [page, call].map((p) => p.getByRole('button', { name: /^(Join call|Join|Start call|Start)$/i }).first())) if (await b.isVisible()) await b.click();
    await page.waitForTimeout(1000);
  }
  await end.waitFor();
  await waitNotice(page, id, 'Meeting notes joined the call.');
  const say = (body) => page.evaluate(({ id, body }) => window.mxMatrixClientPeg.get().sendTextMessage(id, body), { id, body });
  // Shared before it is turned on: the bot must skip it.
  // Element Call's share control is a switch, not a button.
  await call.getByRole('switch', { name: 'Share screen' }).click();
  await page.waitForTimeout(5000);
  await say('!notes screens on');
  await waitNotice(page, id, 'Shared-screen snapshots turned on by');
  await waitNotice(page, id, "shared screen for the notes");
  await page.waitForTimeout(8000); // slide 1 settles and is taken
  fs.writeFileSync(`${dir}/state/screens-revoke`, '');
  for (let i = 0; i < 60 && !fs.existsSync(`${dir}/state/screens-revoked`); i++) await page.waitForTimeout(500);
  await waitNotice(page, id, 'Shared-screen snapshots were turned off in Settings', 30000);
  const slide = (n) => Promise.all(page.frames().map((f) => f.evaluate((n) => { window.kySlide = n; }, n).catch(() => {})));
  await slide(2);
  await page.waitForTimeout(8000);
  // A share started after the revoke is a new track: it must be skipped too.
  const share = call.getByRole('switch', { name: /Share screen|Sharing screen/ });
  await share.click();
  await page.waitForTimeout(2000);
  await share.click();
  await page.waitForTimeout(8000);
  await end.click();
  await end.waitFor({ state: 'hidden' });
  await openRoom(page, id, false);
  await timeline(page).getByText(/Meeting notes: kymatrix-screens-/).waitFor({ timeout: 180000 });
  const notes = await timeline(page).getByText(/Meeting notes: kymatrix-screens-/).last().innerText();
  console.log(`  notes:\n${notes.replace(/^/gm, '    ')}`);
  const shots = (notes.match(/Snapshot \d+ of /g) ?? []).length;
  check(shots === 1, 'the notes list one snapshot: slide 1, taken before the revoke');
  // Element decrypts each snapshot and shows it.
  // Decrypted images, by size: the snapshots are 1280 px wide, avatars a few dozen.
  await page.waitForFunction((n) => [...document.querySelectorAll('.mx_RoomView_MessageList img')].filter((i) => i.complete && i.naturalWidth >= 300).length >= n,
    shots, { timeout: 60000 });
  check(true, `Element decrypted and shows ${shots} snapshots`);
  await memberOf(page, id, NOTES, 'leave');
  out({ room: id, shots });
}

// Meeting-guest spikes S1 and S2 (scripts/matrix-acceptance/spikes/). The guest's session is a
// MAS personal session the harness minted; a page on Element's origin writes it where Element
// restores sessions from, as the bootstrap page would. The token is never in a URL.
async function guestsignin() {
  const { GUEST_MXID: mxid, GUEST_DEVICE: device, GUEST_TOKEN_FILE: tokenFile, MEETING_ROOM: room } = process.env;
  const token = fs.readFileSync(tokenFile, 'utf8').trim();
  const page = await launch('guest', 'spike-guest');
  // A plain document on Element's origin that runs none of Element's code.
  await page.goto(`${CHAT}/config.json`);
  await page.evaluate(({ hs, mxid, device, token }) => {
    localStorage.clear();
    localStorage.setItem('mx_hs_url', hs);
    localStorage.setItem('mx_user_id', mxid);
    localStorage.setItem('mx_device_id', device);
    localStorage.setItem('mx_access_token', token);
    localStorage.setItem('mx_is_guest', 'false');
  }, { hs: MATRIX, mxid, device, token });
  await page.goto(`${CHAT}/#/room/${room}`);
  await synced(page);
  const client = await page.evaluate(() => {
    const c = window.mxMatrixClientPeg.get();
    return { user: c.getUserId(), device: c.getDeviceId(), guest: c.isGuest(), crypto: !!c.getCrypto() };
  });
  check(client.user === mxid && client.device === device, `Element restored the minted session as ${mxid} on ${device}`);
  check(!client.guest, 'Element does not treat it as a Matrix guest (guest access stays off)');
  const stored = await page.evaluate(() => ({ tokenInLocalStorage: localStorage.getItem('mx_access_token') !== null, oidc: Object.keys(localStorage).filter((k) => k.startsWith('mx_oidc')) }));
  await page.waitForTimeout(5000);
  const dialogs = await page.getByRole('dialog').allInnerTexts();
  const toasts = await page.locator('.mx_Toast_toast').allInnerTexts();
  fs.mkdirSync(`${artifacts}/evidence`, { recursive: true });
  await page.screenshot({ path: `${artifacts}/evidence/spike-s1-guest-restored.png` });
  out({ client, stored, dialogs: dialogs.map((d) => d.slice(0, 120)), toasts: toasts.map((t) => t.slice(0, 120)) });
}

async function guestknock() {
  const { GUEST_MXID: mxid, MEETING_ROOM: room } = process.env;
  const alice = await resume('alice', 'prove-alice');
  await joinCall(alice, true, room);
  // S1's session, reopened: Element's lock from that window is taken over as a person would.
  const guest = await resume('guest', 'spike-guest');
  await guest.goto(`${CHAT}/#/room/${room}`);
  await synced(guest);
  // Element cannot preview the room, so it offers a plain join; Synapse refuses it (403, the
  // confinement module), and Element then asks to join (RoomViewStore, PromptAskToJoin).
  await guest.getByRole('button', { name: /Join the discussion/i }).click();
  const ask = guest.getByRole('button', { name: /Request access|Ask to join/i });
  await ask.waitFor({ timeout: 60000 });
  await guest.screenshot({ path: `${artifacts}/evidence/spike-s2-guest-ask.png` });
  await ask.click();
  await memberOf(guest, room, mxid, 'knock');
  const bar = alice.locator('.mx_RoomKnocksBar');
  await bar.waitFor({ timeout: 60000 });
  const barText = await bar.innerText();
  await alice.screenshot({ path: `${artifacts}/evidence/spike-s2-alice-knock-in-call.png` });
  await bar.getByRole('button', { name: /^Approve$/i }).click();
  const end = alice.frameLocator('iframe[title="Element Call"]').getByRole('button', { name: 'End call', exact: true });
  const stillInCall = await end.isVisible();
  check(stillInCall, 'alice admitted the guest without leaving the call');
  await memberOf(guest, room, mxid, 'invite');
  await guest.getByRole('button', { name: /^(Accept|Join)$/ }).first().click();
  await memberOf(guest, room, mxid, 'join');
  check(true, 'the guest accepted and joined');
  await guest.screenshot({ path: `${artifacts}/evidence/spike-s2-guest-joined.png` });
  await hangup(alice);
  out({ barText: barText.slice(0, 200), stillInCall });
}

// Meeting guests on the shipped path (step guests): the guest's browser follows the console's
// answer to Element's bootstrap page, which restores the session; the server already knocked.
// alice, in a call in the meeting room, admits the knock from Element's knocks bar; the guest
// accepts and joins the call, and both decode each other's audio and video. The guest then
// hangs up and stays in the room, so the reconciler must remove it. Writes the guest's token
// (harness only) for the step's grant and confinement checks.
async function guestmeeting() {
  const { GUEST_NEXT: next, MEETING_ROOM: room } = process.env;
  const alice = await resume('alice', 'prove-alice');
  await openRoom(alice, room, true);
  await joinCall(alice, true, room);
  const guest = await launch('guest', 'guest-meeting');
  await guest.goto(next);
  await guest.waitForURL((u) => u.href.startsWith(`${CHAT}/#/room/`), { timeout: 30000 });
  await synced(guest);
  const mxid = await guest.evaluate(() => window.mxMatrixClientPeg.get().getUserId());
  check(mxid.startsWith('@guest-'), `the bootstrap page signed the guest in as ${mxid}`);
  const history = await guest.evaluate(() => [location.href, document.referrer]);
  check(!history.some((u) => u.includes('kymessages-guest.html#')), 'the one-time code is gone from the address bar');
  await memberOf(guest, room, mxid, 'knock');
  fs.mkdirSync(`${artifacts}/evidence`, { recursive: true });
  await guest.screenshot({ path: `${artifacts}/evidence/guests-guest-waiting.png` });
  const bar = alice.locator('.mx_RoomKnocksBar');
  await bar.waitFor({ timeout: 60000 });
  check((await bar.innerText()).includes('(guest)'), 'members see the knock with the (guest) suffix');
  await alice.screenshot({ path: `${artifacts}/evidence/guests-alice-knock.png` });
  await bar.getByRole('button', { name: /^Approve$/i }).click();
  await memberOf(guest, room, mxid, 'invite');
  await guest.getByRole('button', { name: /^(Accept|Join)$/ }).first().click();
  await memberOf(guest, room, mxid, 'join');
  await joinCall(guest, false, room);
  const media = await proveMedia(alice, guest, true);
  check(true, 'alice and the guest receive audio and decode each other\'s video');
  await guest.screenshot({ path: `${artifacts}/evidence/guests-guest-in-call.png` });
  fs.writeFileSync(`${dir}/state/guest.token`, await guest.evaluate(() => window.mxMatrixClientPeg.get().getAccessToken()), { mode: 0o600 });
  // Stay through two 5 s guest sweeps: one that never sees the guest in the call gives it the
  // 2-minute first-call grace, not the 20 s departure guests.sh waits for.
  await guest.waitForTimeout(11000);
  await hangup(guest);
  await hangup(alice);
  out({ guest: mxid, media });
}

const scenarios = { guestsignin, guestknock, guestmeeting, calendar, signin, calls, restoredcalls: () => calls(true), meeting, prove, refused, compat, noclaim, room, token, fedtoken: token, sweeptoken: token, rejoined: token, disabled, reread, reads, media, restored, title, owner, agent, notesinvite, notesmeeting, notesscreens };
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
