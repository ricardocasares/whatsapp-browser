// Web Worker that runs the whatsmeow WebAssembly module.
// State (keys, sessions, contacts) is persisted in IndexedDB.
importScripts('wasm_exec.js');

const DB_NAME = 'whatsmeow-browser';
const STORE = 'kv';

function openDB() {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => req.result.createObjectStore(STORE);
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

async function kvGet(key) {
  const db = await openDB();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readonly');
    const req = tx.objectStore(STORE).get(key);
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

async function kvPut(key, value) {
  const db = await openDB();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite');
    tx.objectStore(STORE).put(value, key);
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

// WASM_PARTS lets a host with small per-file limits serve the module in pieces.
const WASM_PARTS = self.WASM_PARTS || ['main.wasm'];
async function loadWasm(importObject) {
  if (WASM_PARTS.length === 1) {
    const resp = await fetch(WASM_PARTS[0]);
    if (!resp.ok) throw new Error('failed to fetch wasm: ' + resp.status);
    const isWasmMime = (resp.headers.get('content-type') || '').includes('application/wasm');
    if (WebAssembly.instantiateStreaming && isWasmMime) return WebAssembly.instantiateStreaming(resp, importObject);
    return WebAssembly.instantiate(await resp.arrayBuffer(), importObject);
  }
  const buffers = await Promise.all(WASM_PARTS.map(async (p) => {
    const r = await fetch(p);
    if (!r.ok) throw new Error('failed to fetch ' + p + ': ' + r.status);
    return new Uint8Array(await r.arrayBuffer());
  }));
  const total = buffers.reduce((n, b) => n + b.length, 0);
  const all = new Uint8Array(total);
  let off = 0;
  for (const b of buffers) { all.set(b, off); off += b.length; }
  return WebAssembly.instantiate(all, importObject);
}

let goReady = false;
const pending = [];

self.waEmit = (json) => {
  const evt = JSON.parse(json);
  if (evt.type === 'ready') {
    goReady = true;
    for (const c of pending) self.waCommand(JSON.stringify(c));
    pending.length = 0;
  }
  self.postMessage(evt);
};

self.waPersist = (json) => {
  kvPut('state', json).catch((e) => console.error('persist failed', e));
};

self.onmessage = (e) => {
  const cmd = e.data;
  if (goReady) self.waCommand(JSON.stringify(cmd));
  else pending.push(cmd);
};

(async () => {
  let state = '';
  try {
    state = (await kvGet('state')) || '';
  } catch (e) {
    console.error('failed to load state', e);
  }
  const debug = /debug=1/.test(self.location.search || '') || !!self.WA_DEBUG;
  pending.unshift({ type: 'start', state, debug });

  const go = new Go();
  const result = await loadWasm(go.importObject);
  go.run(result.instance).then(() => {
    self.postMessage({ type: 'status', status: 'disconnected', reason: 'wasm module exited' });
  });
})();
