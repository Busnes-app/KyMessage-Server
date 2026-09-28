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
let backgroundWork = false;
let viewGeneration = 0;
let pollTimer: ReturnType<typeof setTimeout> | undefined;
let pollDelay = 10_000;
function stopPolling() { clearTimeout(pollTimer); pollTimer = undefined; }
function schedulePoll() {
  stopPolling();
  if (!opened || document.hidden || !navigator.onLine || devices.find(device => device.id === snapshot?.device)?.status !== 'approved') return;
  pollTimer = setTimeout(() => {
    if (busy || !snapshot?.room || snapshot.pending) { schedulePoll(); return; }
    void action(async () => {
      try { await delivery.sync(); if (roomPaused) await directory(); }
      catch (error) {
        if (!(error instanceof SessionError)) {
          try { await refresh(); } catch (refreshError) { if (refreshError instanceof SessionError) throw refreshError; }
        }
        throw error;
      }
    },'',true);
  },pollDelay);
}
for (const event of ['visibilitychange','online','offline']) {
  (event === 'visibilitychange' ? document : window).addEventListener(event,schedulePoll);
}
let snapshot: Awaited<ReturnType<typeof delivery.status>> | null = null;
let devices: Awaited<ReturnType<typeof delivery.accountDevices>> = [];
let members: Awaited<ReturnType<typeof delivery.members>> = [];
let roomPaused = false;
let rooms: Awaited<ReturnType<typeof delivery.rooms>> = [];

function controls() {
  document.querySelectorAll('button, input, textarea, select').forEach(node => {
    if (node instanceof HTMLButtonElement || node instanceof HTMLInputElement || node instanceof HTMLTextAreaElement || node instanceof HTMLSelectElement) node.disabled = busy && (!backgroundWork || node instanceof HTMLButtonElement);
  });
  const approved = devices.find(device => device.id === snapshot?.device)?.status === 'approved';
  const activeRoom = rooms.some(room => room.id === snapshot?.room && room.membership === 'active');
  button('send').disabled = !activeRoom || roomPaused || busy || !snapshot?.epoch || snapshot.epoch === '0' || snapshot.pending || snapshot.rejoining;
  field('message').disabled = !backgroundWork && button('send').disabled;
  button('lock').disabled = false;
  button('commit').disabled = !activeRoom || busy || snapshot?.epoch === null || snapshot?.pending === true || snapshot?.rejoining === true;
  button('publish').disabled = !approved || busy || (snapshot?.epoch !== null && !snapshot?.rejoining);
  button('sync').disabled = !approved || busy;
  button('retry').disabled = !approved || busy;
  button('approve-own').disabled = !approved || busy;
  button('remove').disabled = busy || snapshot?.pending === true || snapshot?.rejoining === true;
  button('rejoin').disabled = !approved || busy || !snapshot?.epoch || snapshot.pending || snapshot.rejoining;
}
async function action(task: () => Promise<void>, success: string, background = false) {
  if (busy) return;
  const generation = viewGeneration;
  stopPolling();
  busy = true;
  backgroundWork = background;
  controls();
  if (!background) element('notice').textContent = 'Working…';
  try {
    await task();
    if (generation !== viewGeneration) return;
    if (opened) await render();
    if (generation !== viewGeneration) return;
    pollDelay = 10_000;
    if (opened && background) element('poll-state').textContent = 'Automatic checks active.';
    else if (!background) element('notice').textContent = success;
  } catch (error) {
    if (generation !== viewGeneration) return;
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
    if (generation !== viewGeneration && !(error instanceof SessionError)) return;
    const message = error instanceof Error ? error.message : 'The operation failed. Try again.';
    if (background && opened && devices.find(device => device.id === snapshot?.device)?.status === 'revoked') {
      element('poll-state').textContent = 'Automatic checks stopped: device revoked.';
    } else if (background && opened) {
      pollDelay = Math.min(pollDelay*2,60_000);
      element('poll-state').textContent = `Automatic check failed; retrying in ${pollDelay/1000}s. ${message}`;
    } else if (!background || error instanceof SessionError) element('notice').textContent = message;
  } finally {
    busy = false;
    backgroundWork = false;
    controls();
    schedulePoll();
    if (!opened && !background) field('password').focus();
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
function options(id: string, items: {id:string;label:string}[], placeholder = 'Choose a device') {
  const select = field(id);
  if (!(select instanceof HTMLSelectElement)) throw new Error('Missing select');
  const previous = select.value;
  select.replaceChildren(new Option(placeholder,''),...items.map(x => new Option(x.label,x.id)));
  if (items.some(x => x.id === previous)) select.value = previous;
}
async function directory() {
  const generation = viewGeneration;
  const current = await delivery.directory();
  if (!opened || generation !== viewGeneration) return;
  roomPaused = current.paused;
  const peers = current.peers;
  element('peers').replaceChildren(...peers.map(x => line('li',`${x.user_id} · Identity ${x.identity_generation} · ${x.id} · ${x.approved ? 'Verified locally' : 'Needs verification'}`)));
  // Deliberately do not fill the approval field from the server's own fingerprint.
  options('peer-device',peers.filter(x => !x.approved).map(x => ({id:x.id,label:`${x.user_id} · ${x.id}`})));
}
async function refresh() {
  const generation = viewGeneration;
  const listing = await delivery.accountDevices();
  const current = await delivery.status();
  if (!opened || generation !== viewGeneration) return;
  devices = listing;
  const own = devices.find(x => x.id === current.device);
  const available = own?.status === 'approved' ? await delivery.rooms() : [];
  if (!opened || generation !== viewGeneration) return;
  rooms = available;
  if (current.room && own?.status === 'approved' && rooms.some(room => room.id === current.room && room.membership === 'active')) {
    const listing = await delivery.members();
    if (!opened || generation !== viewGeneration) return;
    members = listing;
    await directory();
  } else { members = []; roomPaused = false; element('peers').replaceChildren(); }
}
async function render() {
  const generation = viewGeneration;
  const current = await delivery.status();
  if (!opened || generation !== viewGeneration) return;
  snapshot = current;
  const s = snapshot;
  element('access').hidden = true;
  element('workspace').hidden = false;
  const own = devices.find(x => x.id === s.device);
  const approvedCount = devices.filter(device => device.status === 'approved').length;
  const recovery = approvedCount === 0
    ? 'No approved devices remain. Keep any surviving browser data. A pending replacement can request an identity reset through suite sign-in when the operator enables it. See recovery help below.'
    : own?.status !== 'approved'
    ? 'Use an accessible approved browser to approve a replacement after comparing its fingerprint. If none can be used, identity reset requires fresh suite authentication and new room invitations. See recovery help below.'
    : approvedCount === 1
    ? 'This is your only approved device. Approve another browser before losing access to this one. A replacement receives future messages, not earlier history.'
    : '';
  element('device-recovery').textContent = recovery;
  element('device-recovery').hidden = recovery === '';
  element('identity-reset').hidden = !cookieMode || own?.status !== 'pending' || s.room !== null;
  element('signed-in').textContent = `${s.identity} · Device ${own?.status ?? 'unknown'} · Identity ${own?.identity_generation ?? 'unknown'}`;
  element('account-devices').replaceChildren(...devices.map(x => line('li',`${x.id} · ${x.status}`)));
  options('revoke-device',devices.filter(device => device.status !== 'revoked').map(device => ({id:device.id,label:`${device.id}${device.id === s.device ? ' · This browser' : ''} · ${device.status}`})),'Choose a device to revoke');
  options('pending-device',devices.filter(x => x.status === 'pending').map(x => ({id:x.id,label:x.id})));
  const list = element('rooms');
  list.replaceChildren();
  for (const room of rooms) {
    const row = line('li',room.name + (room.id === s.room ? ' · Current room' : ''));
    if (room.id !== s.room) {
      const join = document.createElement('button');
      join.textContent = `${room.membership === 'invited' ? 'Accept' : 'Open'} ${room.name}`;
      join.addEventListener('click', () => { void action(async () => {
        confirmDraftDiscard();
        await delivery.selectRoom(room.id);
        field('message').value = '';
        await refresh();
      },'Room selected. Prepare to join, then verify your teammates.'); });
      row.append(join);
    }
    list.append(row);
  }
  element('create-form').hidden = own?.status !== 'approved';
  element('room-tools').hidden = s.room === null;
  const room = rooms.find(x => x.id === s.room);
  element('room-title').textContent = room?.name ?? 'A quieter place to talk';
  element('invite-form').hidden = room?.owner !== s.identity;
  element('remove-form').hidden = room?.owner !== s.identity;
  options('remove-member',members.filter(member => member.id !== s.identity && member.status !== 'removed').map(member => ({id:member.id,label:`${member.id} · ${member.status}`})),'Choose a member');
  element('members').replaceChildren(...members.map(member => line('li',`${member.id} · ${member.status}${member.identityGeneration !== member.currentIdentityGeneration ? ` · Server reports identity changed from ${member.identityGeneration} to ${member.currentIdentityGeneration}. New invitation and independent fingerprint verification required.` : ''}`)));
  element('room-state').textContent = own?.status === 'revoked'
    ? 'This messaging device was revoked. Earlier local history remains readable here; sending, receiving and re-enrollment are disabled. Another approved device is needed to approve a replacement.'
    : own?.status !== 'approved'
    ? 'This browser needs approval from an existing device. Compare its fingerprint there, then refresh.'
    : s.room === null ? 'Create a room, or accept an invitation from a teammate.'
    : room?.membership === 'invited' && s.epoch !== null ? 'You have a new invitation. Accept reinvitation to receive future messages; earlier local history remains.'
    : !room ? 'Room access was removed. Earlier local history remains; a new invitation is required.'
    : s.rejoining ? 'Rejoining: prepare to join, then wait for a new verified Welcome. Earlier local history stays; messages during removal are unavailable.'
    : s.epoch === null ? 'Waiting for an existing member to add this verified device. Prepare to join, then check for messages.'
    : roomPaused ? 'Membership changed. Apply verified membership before sending more messages.'
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
  viewGeneration++;
  stopPolling();
  proof.lock(); delivery.disconnect(); opened = false; snapshot = null; devices = []; rooms = []; members = []; roomPaused = false;
  for (const id of ['messages','peers','rooms','account-devices','pending-text','own-fingerprint','signed-in','room-title','room-state','poll-state','members','device-recovery']) element(id).replaceChildren();
  for (const id of ['message','password','peer-fingerprint','account-fingerprint','invite-account','room-name']) field(id).value = '';
  options('peer-device',[]); options('pending-device',[]); options('remove-member',[],'Choose a member'); options('revoke-device',[],'Choose a device to revoke');
  element('workspace').hidden = true; element('access').hidden = false;
}
button('lock').addEventListener('click',() => {
  lockLocal(); controls(); field('password').focus();
  element('notice').textContent = 'Device locked. Unlock with the original account and local passphrase.';
});
window.addEventListener('pagehide',lockLocal);
button('sign-out').addEventListener('click',() => {
  if (busy) return;
  lockLocal();
  void action(async () => {
    element('access-form').hidden = true;
    const response = await secureFetch('/api/auth/logout',{method:'POST',credentials:'same-origin',cache:'no-store',redirect:'error'});
    if (!response.ok) throw new Error('Device locked, but suite sign-out failed. Try signing out again.');
    element('suite-account').textContent = 'Signed out. Sign in again to unlock this device.';
  },'Signed out of suite. Local encrypted history remains on this browser.');
});
click('identity-reset',async () => {
  if (!confirm('Reset your messaging identity using this browser? Every earlier device will be revoked. You lose old room membership and ownership; teammates must reinvite you and independently verify this fingerprint. Earlier messages and lost keys cannot be recovered. Continue to fresh suite authentication?')) throw new Error('Identity reset cancelled.');
  const generation = viewGeneration;
  const url = await delivery.startIdentityReset();
  if (generation !== viewGeneration) return;
  sessionStorage.setItem('kymessages-oidc-return','1');
  lockLocal();
  location.assign(url);
},'Continue with fresh suite authentication.');
function confirmDraftDiscard() {
  if (field('message').value && !confirm('Discard this unsent draft and switch rooms? Pending encrypted sends remain saved in their original room.')) throw new Error('Room change cancelled.');
}
click('refresh',refresh,'Rooms and devices refreshed.');
form('create-form',async () => { confirmDraftDiscard(); await delivery.createRoom(field('room-name').value.trim()); field('message').value = ''; field('room-name').value = ''; await refresh(); },'Room created. Apply verified membership to activate it.');
form('invite-form',async () => { await delivery.invite(field('invite-account').value.trim()); field('invite-account').value = ''; },'Invitation sent. Ask your teammate to refresh their rooms.');
form('remove-form',async () => {
  const target = field('remove-member').value;
  if (!confirm(`Remove ${target} from this room? Their server access ends immediately. Apply verified membership afterward to update encryption. Earlier downloaded messages cannot be recalled.`)) throw new Error('Removal cancelled.');
  try { await delivery.removeMember(target); } finally { await refresh(); }
},'Member removed. If sending is paused, apply verified membership.');
form('verify-form',async () => { await delivery.approveDevice(field('peer-device').value,field('peer-fingerprint').value.trim()); field('peer-fingerprint').value = ''; await directory(); },'Teammate verified locally.');
form('account-revocation-form',async () => {
  const id = field('revoke-device').value;
  const target = devices.find(device => device.id === id);
  if (!target) throw new Error('Refresh devices before revoking');
  const current = id === snapshot?.device ? ' This is your current browser; it will retain local history but lose messaging access.' : '';
  if (!confirm(`Revoke device ${id} (${target.status})?${current} Remaining room members must update encryption. Previously downloaded messages cannot be erased. Revoking every approved device prevents automatic approval of a replacement; identity reset requires fresh suite authentication and loses old room access and ownership.`)) throw new Error('Revocation cancelled.');
  try { await delivery.revokeAccountDevice(id); } finally { await refresh(); }
},'Device revoked. Remaining room members must apply verified membership where sending is paused.');
form('account-approval-form',async () => { await delivery.approveAccountDevice(field('pending-device').value,field('account-fingerprint').value.trim()); field('account-fingerprint').value = ''; await refresh(); },'Own device approved. Refresh on that browser.');
click('rejoin',async () => { await delivery.rejoin(); await delivery.publishKeyPackage(); await refresh(); },'Reinvitation accepted. Ask an existing member to apply verified membership; earlier local history is preserved.');
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
