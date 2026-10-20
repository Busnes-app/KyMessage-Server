// The bot's crypto store: the whole fake IndexedDB (every database, store, index and record)
// saved to one 0600 file, written to a temp file and renamed so a crash never leaves half.
import fs from 'node:fs';
import v8 from 'node:v8';

const req = (r) => new Promise((ok, no) => { r.onsuccess = () => ok(r.result); r.onerror = () => no(r.error); });

export async function save(file) {
  const dbs = [];
  for (const { name, version } of await indexedDB.databases()) {
    const db = await req(indexedDB.open(name));
    const stores = [];
    for (const s of db.objectStoreNames) {
      const os = db.transaction(s).objectStore(s);
      const indexes = [...os.indexNames].map((n) => { const i = os.index(n); return { name: n, keyPath: i.keyPath, unique: i.unique, multiEntry: i.multiEntry }; });
      const [keys, values] = await Promise.all([req(os.getAllKeys()), req(os.getAll())]);
      stores.push({ name: s, keyPath: os.keyPath, autoIncrement: os.autoIncrement, indexes, keys, values });
    }
    db.close();
    dbs.push({ name, version, stores });
  }
  fs.writeFileSync(`${file}.tmp`, v8.serialize(dbs), { mode: 0o600 });
  fs.renameSync(`${file}.tmp`, file);
}

export async function load(file) {
  for (const d of v8.deserialize(fs.readFileSync(file))) {
    const open = indexedDB.open(d.name, d.version);
    open.onupgradeneeded = () => {
      for (const s of d.stores) {
        const os = open.result.createObjectStore(s.name, { keyPath: s.keyPath ?? undefined, autoIncrement: s.autoIncrement });
        for (const i of s.indexes) os.createIndex(i.name, i.keyPath, { unique: i.unique, multiEntry: i.multiEntry });
      }
    };
    const db = await req(open);
    if (d.stores.length) {
      const tx = db.transaction(d.stores.map((s) => s.name), 'readwrite');
      for (const s of d.stores) {
        const os = tx.objectStore(s.name);
        s.values.forEach((v, i) => (s.keyPath === null ? os.put(v, s.keys[i]) : os.put(v)));
      }
      await new Promise((ok, no) => { tx.oncomplete = ok; tx.onerror = () => no(tx.error); });
    }
    db.close();
  }
}

// wipe drops every database, for a new device.
export async function wipe() {
  for (const { name } of await indexedDB.databases()) await req(indexedDB.deleteDatabase(name));
}
