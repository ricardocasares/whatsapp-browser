#!/bin/sh
# Builds the WebAssembly module and copies the Go JS runtime shim into web/.
set -e
cd "$(dirname "$0")"
export PATH="$(mise where go)/bin:$PATH"
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/wasm_exec.js
GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o web/main.wasm .
ls -lh web/main.wasm
