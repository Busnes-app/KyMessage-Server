import { proof } from './device';
import { delivery } from './delivery';
import { object, text } from './delivery-wire';
import { signedInAccount, secureFetch, SessionError } from './session';

const cookieMode = new URLSearchParams(location.search).get('auth') === 'oidc';

function element(id: string): HTMLElement {
  const node = document.getElementById(id);
  if (!node) throw new Error(`Missing element ${id}`);
  return node;
}
function field(id: string) {
  const node = element(id);
  if (!(node instanceof HTMLInputElement || node instanceof HTMLTextAreaElement || node instanceof HTMLSelectElement)) throw new Error(`Missing field ${id}`);
  return node;
}
function button(id: string) {
  const node = element(id);
  if (!(node instanceof HTMLButtonElement)) throw new Error(`Missing button ${id}`);
  return node;
}
function line(tag: 'li' | 'p' | 'strong' | 'small', value: string) {
  const node = document.createElement(tag);
  node.textContent = value;
  return node;
}
let busy = false;
let opened = false;
let snapshot: Awaited<ReturnType<typeof delivery.status>> | null = null;
let devices: Awaited<ReturnType<typeof delivery.accountDevices>> = [];
let rooms: Awaited<ReturnType<typeof delivery.rooms>> = [];

function controls() {
  document.querySelectorAll('button, input, textarea, select').forEach(node => {
    if (node instanceof HTMLButtonElement || node instanceof HTMLInputElement || node instanceof HTMLTextAreaElement || node instanceof HTMLSelectElement) node.disabled = busy;
  });
  button('send').disabled = busy || !snapshot?.epoch || snapshot.epoch === '0' || snapshot.pending;
  field('message').disabled = button('send').disabled;
  button('commit').disabled = busy || snapshot?.epoch === null || snapshot?.pending === true;
  button('publish').disabled = busy || snapshot?.epoch !== null;
}
async function action(task: () => Promise<void>, success: string) {
  if (busy) return;
  busy = true;
  controls();
  element('notice').textContent = 'Working…';
  try {
    await task();
    if (opened) await render();
    element('notice').textContent = success;
  } catch (error) {
    if (error instanceof SessionError) {
      lockLocal();
      element('access-form').hidden = cookieMode;
      element('suite-account').textContent = 'Sign in again before unlocking your device.';
    }
    // A network failure can follow a successful vault write. Re-read local status
    // to expose its durable pending request without requiring another network call.
    if (opened) {
      try { await render(); } catch { /* Keep the original failure visible. */ }
    }
    element('notice').textContent = error instanceof Error ? error.message : 'The operation failed. Try again.';
  } finally {
    busy = false;
    controls();
    if (!opened) field('password').focus();
  }
}
function form(id: string, task: (event: SubmitEvent) => Promise<void>, success: string) {
  const node = element(id);
  if (!(node instanceof HTMLFormElement)) throw new Error('Missing form');
  node.addEventListener('submit', event => {
    event.preventDefault();
    void action(() => task(event),success);
  });
}
function click(id: string, task: () => Promise<void>, success: string) {
  button(id).addEventListener('click', () => { void action(task,success); });
}
function options(id: string, items: {id:string;label:string}[]) {
  const select = field(id);
  if (!(select instanceof HTMLSelectElement)) throw new Error('Missing select');
  const previous = select.value;
  select.replaceChildren(new Option('Choose a device',''),...items.map(x => new Option(x.label,x.id)));
  if (items.some(x => x.id === previous)) select.value = previous;
}
async function directory() {
  const peers = await delivery.directory();
  element('peers').replaceChildren(...peers.map(x => line('li',`${x.user_id} · ${x.id} · ${x.approved ? 'Verified locally' : 'Needs verification'}`)));
  // Deliberately do not fill the approval field from the server's own fingerprint.
  options('peer-device',peers.filter(x => !x.approved).map(x => ({id:x.id,label:`${x.user_id} · ${x.id}`})));
}
async function refresh() {
  devices = await delivery.accountDevices();
  const current = await delivery.status();
  const own = devices.find(x => x.id === current.device);
  rooms = own?.status === 'approved' ? await delivery.rooms() : [];
  if (current.room && own?.status === 'approved') await directory();
}
async function render() {
  snapshot = await delivery.status();
  const s = snapshot;
  element('access').hidden = true;
  element('workspace').hidden = false;
  const own = devices.find(x => x.id === s.device);
  element('signed-in').textContent = `${s.identity} · Device ${own?.status ?? 'unknown'}`;
  element('account-devices').replaceChildren(...devices.map(x => line('li',`${x.id} · ${x.status}`)));
  options('pending-device',devices.filter(x => x.status === 'pending').map(x => ({id:x.id,label:x.id})));
  const list = element('rooms');
  list.replaceChildren();
  for (const room of rooms) {
    const row = line('li',room.name + (room.id === s.room ? ' · Current room' : ''));
    if (!s.room) {
      const join = document.createElement('button');
      join.textContent = `${room.membership === 'invited' ? 'Accept' : 'Open'} ${room.name}`;
      join.addEventListener('click', () => { void action(async () => {
        await delivery.selectRoom(room.id);
        await refresh();
      },'Room selected. Prepare to join, then verify your teammates.'); });
      row.append(join);
    }
    list.append(row);
  }
  element('create-form').hidden = s.room !== null || own?.status !== 'approved';
  element('room-tools').hidden = s.room === null;
  const room = rooms.find(x => x.id === s.room);
  element('room-title').textContent = room?.name ?? 'A quieter place to talk';
  element('invite-form').hidden = room?.owner !== s.identity;
  element('room-state').textContent = own?.status !== 'approved'
    ? 'This browser needs approval from an existing device. Compare its fingerprint there, then refresh.'
    : s.room === null ? 'Create a room, or accept an invitation from a teammate.'
    : s.epoch === null ? 'Waiting for an existing member to add this verified device. Prepare to join, then check for messages.'
    : s.epoch === '0' ? 'Apply verified membership to activate this room.'
    : 'Room unlocked. Check for messages to catch up before sending.';
  element('messages').replaceChildren(...s.messages.map(m => {
    const row = document.createElement('li');
    row.append(line('strong',m.sender === s.identity ? 'You' : m.sender),line('p',m.text),line('small',m.sender === s.identity ? 'Accepted by server · Not a read receipt' : 'Received and verified'));
    return row;
  }));
  element('pending').hidden = !s.pending;
  element('pending-text').textContent = s.pendingText === null ? 'Membership change pending. Retry the same delivery.' : `Delivery pending — confirmation unknown\n${s.pendingText}`;
  element('send-form').hidden = s.room === null;
}

form('access-form',async event => {
  const passphrase = field('password').value;
  field('password').value = '';
  try {
    const account = cookieMode ? await signedInAccount() : null;
    if (cookieMode && !account) throw new SessionError('Sign in before unlocking your device.');
    const identity = account?.id ?? field('account').value.trim();
    if (event.submitter instanceof HTMLButtonElement && event.submitter.value === 'create') await proof.initialize(identity,passphrase);
    else await proof.unlock(passphrase);
    if ((await proof.status()).identity !== identity) throw new SessionError('This vault belongs to a different account. Sign in with its original account.');
    if (cookieMode) delivery.connectCookie(identity);
    else {
      const response = await fetch('/proof-fixture/session/' + encodeURIComponent(identity), {method:'POST',credentials:'omit',redirect:'error'});
      if (!response.ok) throw new Error('Test sign-in unavailable. Start the disposable fixture and enable the preview proxy.');
      const value: unknown = await response.json();
      delivery.connect(text(object(value).session));
    }
    await delivery.enroll();
    opened = true;
    element('own-fingerprint').textContent = await delivery.ownFingerprint();
    await refresh();
  } catch (error) {
    opened = false;
    delivery.disconnect();
    proof.lock();
    throw error;
  }
},'Test device connected.');
function lockLocal() {
  proof.lock(); delivery.disconnect(); opened = false; snapshot = null; devices = []; rooms = [];
  for (const id of ['messages','peers','rooms','account-devices','pending-text','own-fingerprint','signed-in','room-title','room-state']) element(id).replaceChildren();
  for (const id of ['message','password','peer-fingerprint','account-fingerprint','invite-account','room-name']) field(id).value = '';
  options('peer-device',[]); options('pending-device',[]);
  element('workspace').hidden = true; element('access').hidden = false;
}
click('lock',async () => { lockLocal(); },'Device locked. Unlock with the original account and local passphrase.');
click('sign-out',async () => {
  lockLocal();
  element('access-form').hidden = true;
  const response = await secureFetch('/api/auth/logout',{method:'POST',credentials:'same-origin',cache:'no-store',redirect:'error'});
  if (!response.ok) throw new Error('Device locked, but suite sign-out failed. Try signing out again.');
  element('suite-account').textContent = 'Signed out. Sign in again to unlock this device.';
},'Signed out of suite. Local encrypted history remains on this browser.');
click('refresh',refresh,'Rooms and devices refreshed.');
form('create-form',async () => { await delivery.createRoom(field('room-name').value.trim()); await refresh(); },'Room created. Apply verified membership to activate it.');
form('invite-form',async () => { await delivery.invite(field('invite-account').value.trim()); field('invite-account').value = ''; },'Invitation sent. Ask your teammate to refresh their rooms.');
form('verify-form',async () => { await delivery.approveDevice(field('peer-device').value,field('peer-fingerprint').value.trim()); field('peer-fingerprint').value = ''; await directory(); },'Teammate verified locally.');
form('account-approval-form',async () => { await delivery.approveAccountDevice(field('pending-device').value,field('account-fingerprint').value.trim()); field('account-fingerprint').value = ''; await refresh(); },'Own device approved. Refresh on that browser.');
click('publish',async () => { await delivery.publishKeyPackage(); },'Ready to join. Ask an existing member to verify this device and apply membership.');
click('sync',async () => { await delivery.sync(); await refresh(); },'Messages checked.');
click('commit',async () => { await delivery.stageCommit(); await delivery.submit(); await delivery.sync(); await refresh(); },'Verified membership applied.');
form('send-form',async () => {
  await delivery.stageSend(field('message').value);
  field('message').value = '';
  await delivery.submit();
  await delivery.sync();
},'Message accepted by server. This is not a read receipt.');
click('retry',async () => { await delivery.submit(); await delivery.sync(); },'Pending delivery confirmed by server.');

const theme = field('theme');
function applyTheme(value: string) {
  if (value === 'busnes-light' || value === 'busnes-dark') document.documentElement.dataset.theme = value;
  else delete document.documentElement.dataset.theme;
}
try { const saved = localStorage.getItem('kymessages-proof-theme'); if (saved === 'busnes-light' || saved === 'busnes-dark') theme.value = saved; } catch { /* OS default when storage is unavailable. */ }
applyTheme(theme.value);
theme.addEventListener('change',() => { applyTheme(theme.value); try { localStorage.setItem('kymessages-proof-theme',theme.value); } catch { /* Theme still applies for this page. */ } });
controls();

if (cookieMode) {
  element('suite-access').hidden = false;
  element('account-label').hidden = true;
  const accountField = field('account');
  if (accountField instanceof HTMLInputElement) { accountField.required = false; accountField.readOnly = true; accountField.removeAttribute('pattern'); }
  element('access-form').hidden = true;
  element('oidc-login').addEventListener('click',() => { lockLocal(); sessionStorage.setItem('kymessages-oidc-return','1'); });
  void action(async () => {
    const account = await signedInAccount();
    if (account) {
      element('suite-account').textContent = `${account.name} · Account ID: ${account.id}`;
      element('access-form').hidden = false;
    }
  },'Sign in with suite identity, then create or unlock this device.');
}
