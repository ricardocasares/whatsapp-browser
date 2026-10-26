// Maps the `ws` package API that Baileys uses onto the browser's native WebSocket.
import { EventEmitter } from 'events';
import { Buffer } from 'buffer';

export default class BrowserWebSocket extends EventEmitter {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;

  constructor(url, _options) {
    super();
    // Browsers set the Origin header themselves; WhatsApp does not enforce it.
    this.ws = new WebSocket(url);
    this.ws.binaryType = 'arraybuffer';
    this.ws.onopen = () => this.emit('open');
    this.ws.onmessage = (e) => this.emit('message', Buffer.from(e.data), true);
    this.ws.onerror = () => this.emit('error', new Error('WebSocket error'));
    this.ws.onclose = (e) => this.emit('close', e.code, Buffer.from(e.reason || ''));
  }

  get readyState() {
    return this.ws.readyState;
  }

  send(data, cb) {
    try {
      this.ws.send(data);
      cb?.();
    } catch (err) {
      if (cb) cb(err);
      else this.emit('error', err);
    }
  }

  close() {
    // Browsers only allow 1000 or 3000-4999 as close codes; use the default.
    try { this.ws.close(); } catch {}
  }

  terminate() {
    this.close();
  }

  ping() {}
  pong() {}
}
