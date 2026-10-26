# WhatsApp Browser

A WhatsApp Web client that runs **entirely in the browser**. The
[whatsmeow](https://github.com/tulir/whatsmeow) Go library is compiled to
WebAssembly and talks directly to WhatsApp's servers over a WebSocket from a
Web Worker. There is no backend: the only thing you need is a static file host.

```
browser ──wss──▶ web.whatsapp.com
   │
   ├─ web/index.html + app.js   UI (main thread)
   ├─ web/worker.js             Web Worker, loads the wasm, persists state in IndexedDB
   └─ web/main.wasm             whatsmeow + memstore compiled with GOOS=js GOARCH=wasm
```

## Build

Go is pinned with [mise](https://mise.jdx.dev) in `mise.toml`.

```sh
mise install
./build.sh          # writes web/main.wasm and web/wasm_exec.js
```

## Run

Serve the `web/` directory with any static file server. Browsers refuse to load
wasm modules and Web Workers from `file://`, so a static host is required, but
it serves files only. Examples:

```sh
cd web && python3 -m http.server 8787        # http://127.0.0.1:8787/
```

or push `web/` to GitHub Pages, Netlify, S3, etc.

Add `?debug=1` to the URL to get whatsmeow debug logs in the worker console.

## Pairing

1. Open the page. The wasm module connects to WhatsApp and shows a QR code.
2. On your phone: WhatsApp → Settings → Linked devices → Link a device → scan.
3. After pairing, keys and sessions are saved in IndexedDB, so a reload
   reconnects without a new scan. "Log out" unlinks the device and clears the
   stored state.

QR codes are renewed automatically when WhatsApp expires them.

## What works

- QR pairing, reconnect on reload, logout
- Incoming text and media placeholders (images, video, audio, documents,
  stickers, polls, locations, contacts, deleted messages)
- History sync on first pairing, group names, contact names, push names
- Sending text, delivery/read ticks, typing indicators, read receipts
- Chat list with search, unread counters, desktop notifications

## Layout

- `main_js.go` – wasm entrypoint; bridges whatsmeow to JS via `waCommand` /
  `waEmit` / `waPersist`
- `memstore/` – in-memory implementation of every whatsmeow store interface
  with JSON export/import (whatsmeow only ships an SQL store, which can't run
  in a browser). `go test ./memstore/` covers the round trip.
- `web/` – static site
- `third_party/coder-websocket/` – copy of coder/websocket with a one-line
  fix in its wasm build (see `PATCHED.md` there), wired in with a `replace`
  directive in `go.mod`

## Notes

- WhatsApp's WebSocket endpoint does not enforce the `Origin` header, which is
  what makes a cross-origin browser connection possible.
- All cryptographic state lives in the browser's IndexedDB for this origin.
  Anyone with access to the browser profile can read it, same as WhatsApp Web.
