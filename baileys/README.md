# Baileys edition

Same idea as the main app, but with [Baileys](https://github.com/WhiskeySockets/Baileys),
the TypeScript implementation of the WhatsApp Web protocol, bundled for the
browser with Vite. No server; the page connects straight to WhatsApp.

```sh
npm install
npm run build        # dist/
npm run dev          # http://localhost:5173
```

What it took to run a Node library in a page (`vite.config.js`, `src/`):

- `ws` is aliased to `src/ws-shim.js`, a wrapper around the browser WebSocket
  with the event-emitter API Baileys expects.
- Node `crypto`, `events`, `util`, `zlib`, `buffer`, `process` come from
  `vite-plugin-node-polyfills`; `stream` is resolved to stream-browserify by
  hand so that `stream/promises` can be stubbed.
- `fs`, `os`, `child_process`, `http(s)`, `vm`, `async_hooks` are stubbed in
  `src/stubs/`; the optional media peers (`sharp`, `jimp`, …) are empty modules.
- `src/polyfills.js` gives browser timers the `.unref()` method Node code calls.
- Auth state lives in IndexedDB via `src/auth-idb.js`, the same shape as
  Baileys' `useMultiFileAuthState`.

Bundle: about 5.5 MB of JavaScript (1.5 MB gzipped). Baileys itself ships a
small Rust-compiled wasm for hashing and LTHash, so it is not strictly
wasm-free either.
