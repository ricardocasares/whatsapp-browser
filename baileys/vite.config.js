import { defineConfig } from 'vite';
import { nodePolyfills } from 'vite-plugin-node-polyfills';
import { fileURLToPath } from 'url';

const stub = (name) => fileURLToPath(new URL(`./src/stubs/${name}.js`, import.meta.url));

// Resolves Node built-ins that the polyfill plugin cannot provide, ahead of every other resolver.
const stubs = {
  'stream/promises': 'stream-promises', 'async_hooks': 'async_hooks', 'http': 'http', 'https': 'http', 'vm': 'vm',
  'fs': 'fs', 'fs/promises': 'fs', 'os': 'os', 'child_process': 'child_process',
};
const browserStubs = () => ({
  name: 'browser-stubs',
  enforce: 'pre',
  resolveId(id) {
    const bare = id.replace(/^node:/, '');
    if (bare === 'stream') return fileURLToPath(new URL('./node_modules/stream-browserify/index.js', import.meta.url));
    if (stubs[bare]) return stub(stubs[bare]);
    if (/^(sharp|jimp|audio-decode|link-preview-js|music-metadata)$/.test(bare)) return stub('empty');
    return null;
  },
});

export default defineConfig({
  base: './',
  plugins: [
    browserStubs(),
    nodePolyfills({
      include: ['crypto', 'events', 'util', 'buffer', 'process', 'url', 'path', 'zlib', 'assert', 'string_decoder', 'querystring'],
      globals: { Buffer: true, process: true, global: true },
      protocolImports: true,
    }),
  ],
  resolve: {
    alias: [
      { find: /^ws$/, replacement: fileURLToPath(new URL('./src/ws-shim.js', import.meta.url)) },
      { find: /^(node:)?fs$/, replacement: stub('fs') },
      { find: /^(node:)?fs\/promises$/, replacement: stub('fs') },
      { find: /^(node:)?stream\/promises$/, replacement: stub('stream-promises') },
      { find: /^(node:)?os$/, replacement: stub('os') },
      { find: /^(node:)?child_process$/, replacement: stub('child_process') },
      { find: /^(sharp|jimp|audio-decode|link-preview-js|music-metadata)$/, replacement: stub('empty') },
    ],
  },
  build: { target: 'esnext', sourcemap: false },
});
