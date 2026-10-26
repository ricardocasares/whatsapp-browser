// Browser timers return numbers; Node code calls .unref()/.ref() on them.
for (const name of ['setTimeout', 'setInterval']) {
  const orig = globalThis[name].bind(globalThis);
  globalThis[name] = (...args) => {
    const id = orig(...args);
    const handle = new Number(id);
    handle.unref = () => handle;
    handle.ref = () => handle;
    handle.hasRef = () => true;
    handle.refresh = () => handle;
    handle[Symbol.toPrimitive] = () => id;
    return handle;
  };
}
