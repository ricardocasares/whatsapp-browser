// Minimal AsyncLocalStorage for the browser: context is kept for synchronous callbacks
// and promise continuations started inside run(); good enough for Baileys' transaction tracking.
export class AsyncLocalStorage {
  constructor() { this.store = undefined; }
  getStore() { return this.store; }
  run(store, fn, ...args) {
    const prev = this.store;
    this.store = store;
    try {
      const r = fn(...args);
      if (r && typeof r.then === 'function') return r.finally(() => { this.store = prev; });
      this.store = prev;
      return r;
    } catch (e) { this.store = prev; throw e; }
  }
  exit(fn, ...args) { return this.run(undefined, fn, ...args); }
  enterWith(store) { this.store = store; }
  disable() { this.store = undefined; }
}
export default { AsyncLocalStorage };
