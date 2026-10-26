// Baileys auth state stored in IndexedDB (same shape as useMultiFileAuthState).
import { initAuthCreds, BufferJSON, proto } from '@whiskeysockets/baileys';

const DB = 'baileys-browser';
const STORE = 'auth';

function openDB() {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB, 1);
    req.onupgradeneeded = () => req.result.createObjectStore(STORE);
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

async function read(db, key) {
  return new Promise((resolve, reject) => {
    const req = db.transaction(STORE, 'readonly').objectStore(STORE).get(key);
    req.onsuccess = () => resolve(req.result == null ? null : JSON.parse(req.result, BufferJSON.reviver));
    req.onerror = () => reject(req.error);
  });
}

async function write(db, entries) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite');
    const store = tx.objectStore(STORE);
    for (const [key, value] of entries) {
      if (value == null) store.delete(key);
      else store.put(JSON.stringify(value, BufferJSON.replacer), key);
    }
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

export async function clearAuthState() {
  const db = await openDB();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite');
    tx.objectStore(STORE).clear();
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

export async function useIndexedDBAuthState() {
  const db = await openDB();
  const creds = (await read(db, 'creds')) || initAuthCreds();
  return {
    state: {
      creds,
      keys: {
        get: async (type, ids) => {
          const data = {};
          await Promise.all(ids.map(async (id) => {
            let value = await read(db, `${type}-${id}`);
            if (type === 'app-state-sync-key' && value) {
              value = proto.Message.AppStateSyncKeyData.fromObject(value);
            }
            data[id] = value;
          }));
          return data;
        },
        set: async (data) => {
          const entries = [];
          for (const category in data) {
            for (const id in data[category]) {
              entries.push([`${category}-${id}`, data[category][id]]);
            }
          }
          if (entries.length) await write(db, entries);
        },
      },
    },
    saveCreds: () => write(db, [['creds', creds]]),
  };
}
