package matrixinit

import (
	"crypto/sha256"
	"encoding/base64"
)

// guestScript is the bootstrap page's only script, a constant: its hash is the page's
// script-src, so the page runs nothing else. It reads the console's origin from its own
// data-admin attribute. The code leaves the address bar before anything is fetched. A browser
// already signed in to chat as a member is left alone; an earlier guest's session is replaced.
func guestScript() (script, hash string) {
	script = `(async () => {
  const admin = document.currentScript.dataset.admin;
  const say = (t) => { document.getElementById('m').textContent = t; };
  const code = location.hash.slice(1);
  history.replaceState(null, '', location.pathname);
  if (!/^[A-Za-z0-9_-]{43}$/.test(code)) return say('This sign-in link is not valid. Open your meeting link again.');
  const prev = localStorage.getItem('mx_user_id');
  if (prev && !prev.startsWith('@guest-')) return say('This browser is signed in to chat already. Open your meeting link in a private window.');
  let s;
  try {
    const r = await fetch(admin + '/api/meet/session', { method: 'POST', body: code, credentials: 'omit', headers: { 'Content-Type': 'text/plain' } });
    if (!r.ok) return say('This sign-in has expired. Open your meeting link again.');
    s = await r.json();
  } catch { return say('The server could not be reached. Open your meeting link again.'); }
  localStorage.clear();
  if (prev && indexedDB.databases) {
    for (const db of await indexedDB.databases()) await new Promise((done) => { const q = indexedDB.deleteDatabase(db.name); q.onsuccess = q.onerror = q.onblocked = done; });
  }
  localStorage.setItem('mx_hs_url', s.homeserver);
  localStorage.setItem('mx_user_id', s.user_id);
  localStorage.setItem('mx_device_id', s.device_id);
  localStorage.setItem('mx_access_token', s.access_token);
  localStorage.setItem('mx_is_guest', 'false');
  location.replace('/#/room/' + encodeURIComponent(s.room_id));
})();`
	sum := sha256.Sum256([]byte(script))
	return script, base64.StdEncoding.EncodeToString(sum[:])
}
